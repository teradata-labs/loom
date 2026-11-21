// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package observability

import (
	"bytes"
	"context"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// OTelTracer exports Loom spans to any OTLP HTTP endpoint.
// It bridges Loom's internal span model to the OTel SDK, translating
// attributes to GenAI semantic conventions on export.
type OTelTracer struct {
	provider    *sdktrace.TracerProvider
	tracer      oteltrace.Tracer
	activeSpans sync.Map // loom SpanID (string) → oteltrace.Span
	privacy     PrivacyConfig
}

// NewOTelTracer creates a tracer that exports via OTLP HTTP.
// Returns an error if the endpoint is empty or the exporter cannot be created.
func NewOTelTracer(cfg OTelConfig) (*OTelTracer, error) {
	cfg = resolveOTelConfig(cfg)
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("otlp_endpoint is required (set OTEL_EXPORTER_OTLP_TRACES_ENDPOINT or observability.otlp_endpoint)")
	}

	exporterOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(cfg.Endpoint),
		otlptracehttp.WithTimeout(cfg.Timeout),
	}
	if len(cfg.Headers) > 0 {
		exporterOpts = append(exporterOpts, otlptracehttp.WithHeaders(cfg.Headers))
	}
	if cfg.Insecure {
		exporterOpts = append(exporterOpts, otlptracehttp.WithInsecure())
	}

	exporter, err := otlptracehttp.New(context.Background(), exporterOpts...)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP exporter: %w", err)
	}

	resAttrs := []attribute.KeyValue{
		attribute.String("service.name", cfg.ServiceName),
	}
	if cfg.ServiceVersion != "" {
		resAttrs = append(resAttrs, attribute.String("service.version", cfg.ServiceVersion))
	}
	res, err := resource.New(context.Background(), resource.WithAttributes(resAttrs...))
	if err != nil {
		return nil, fmt.Errorf("creating OTel resource: %w", err)
	}

	batchProcessor := sdktrace.NewBatchSpanProcessor(exporter,
		sdktrace.WithMaxExportBatchSize(cfg.MaxBatchSize),
		sdktrace.WithBatchTimeout(cfg.FlushInterval),
	)
	processor := newFilteringSpanProcessor(batchProcessor, cfg.SpanFilter)

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	return &OTelTracer{
		provider: provider,
		tracer:   provider.Tracer("loom"),
		privacy:  cfg.Privacy,
	}, nil
}

// StartSpan creates a Loom span and a paired OTel span.
// The Loom span is placed in the returned context for downstream context propagation.
func (t *OTelTracer) StartSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, *Span) {
	// Build Loom span — same logic as NoOpTracer to preserve context propagation.
	traceID := uuid.New().String()
	if override := traceIDFromContextOverride(ctx); override != "" {
		traceID = override
	}
	loomSpan := &Span{
		TraceID:    traceID,
		SpanID:     uuid.New().String(),
		Name:       name,
		StartTime:  time.Now(),
		Attributes: make(map[string]interface{}),
	}
	for _, opt := range opts {
		opt(loomSpan)
	}

	// Link to parent Loom span if present.
	otelCtx := ctx
	if parent := SpanFromContext(ctx); parent != nil {
		loomSpan.TraceID = parent.TraceID
		loomSpan.ParentID = parent.SpanID
		if len(parent.ResourceAttributes) > 0 && len(loomSpan.ResourceAttributes) == 0 {
			loomSpan.ResourceAttributes = make(map[string]string, len(parent.ResourceAttributes))
			for k, v := range parent.ResourceAttributes {
				loomSpan.ResourceAttributes[k] = v
			}
		}
		// Prefer the live SDK span. Synthetic context is only valid when the
		// parent originated outside this tracer and is not active locally.
		if raw, ok := t.activeSpans.Load(parent.SpanID); ok {
			if activeParent, ok := raw.(oteltrace.Span); ok {
				otelCtx = oteltrace.ContextWithSpan(ctx, activeParent)
			}
		} else if psc, ok := buildOTelSpanContext(parent.TraceID, parent.SpanID); ok {
			otelCtx = oteltrace.ContextWithRemoteSpanContext(ctx, psc)
		}
	}

	otelCtx, otelSpan := t.tracer.Start(otelCtx, name,
		oteltrace.WithTimestamp(loomSpan.StartTime),
		oteltrace.WithSpanKind(spanKindFor(name)),
	)
	t.activeSpans.Store(loomSpan.SpanID, otelSpan)

	return ContextWithSpan(otelCtx, loomSpan), loomSpan
}

// EndSpan completes the span: calculates duration, applies privacy redaction,
// translates attributes to gen_ai.* semconv, and ends the OTel span.
func (t *OTelTracer) EndSpan(span *Span) {
	if span == nil {
		return
	}
	span.EndTime = time.Now()
	span.Duration = span.EndTime.Sub(span.StartTime)

	raw, ok := t.activeSpans.LoadAndDelete(span.SpanID)
	if !ok {
		return
	}
	otelSpan, _ := raw.(oteltrace.Span)

	redacted := redactOTelSpan(span, t.privacy)

	// Translate span attributes.
	translateAttrs(otelSpan, redacted.Attributes)

	// Forward resource attributes (service.name, agent.id, session.id, etc.).
	for k, v := range redacted.ResourceAttributes {
		otelSpan.SetAttributes(attribute.String(k, v))
	}

	// Forward span events.
	for _, ev := range redacted.Events {
		evAttrs := make([]attribute.KeyValue, 0, len(ev.Attributes))
		for k, v := range ev.Attributes {
			attr, err := toOTelAttr(k, v)
			if err != nil {
				reportInvalidAttribute("event", v)
				continue
			}
			evAttrs = append(evAttrs, attr)
		}
		otelSpan.AddEvent(ev.Name,
			oteltrace.WithTimestamp(ev.Timestamp),
			oteltrace.WithAttributes(evAttrs...),
		)
	}

	// Set span status.
	switch redacted.Status.Code {
	case StatusError:
		otelSpan.SetStatus(codes.Error, redacted.Status.Message)
	case StatusOK:
		otelSpan.SetStatus(codes.Ok, "")
	}

	otelSpan.End(oteltrace.WithTimestamp(span.EndTime))
}

// RecordMetric is a no-op. Metrics export via OTLP is not in scope for this release.
// Backends can derive aggregate metrics from span attributes (tokens, cost, latency).
func (t *OTelTracer) RecordMetric(_ string, _ float64, _ map[string]string) {}

// RecordEvent is a no-op. Point-in-time events should be attached to spans via span.AddEvent.
func (t *OTelTracer) RecordEvent(_ context.Context, _ string, _ map[string]interface{}) {}

// Flush forces immediate export of all buffered spans and blocks until done.
func (t *OTelTracer) Flush(ctx context.Context) error {
	return t.provider.ForceFlush(ctx)
}

// Shutdown gracefully drains buffered spans and stops the exporter.
// Call this on server shutdown after Flush.
func (t *OTelTracer) Shutdown(ctx context.Context) error {
	return t.provider.Shutdown(ctx)
}

// buildOTelSpanContext constructs an OTel SpanContext from Loom UUID-based IDs.
//
// Loom uses UUID strings (e.g. "550e8400-e29b-41d4-a716-446655440000").
// A UUID stripped of dashes yields 32 hex chars = 16 bytes, exactly an OTel TraceID.
// The SpanID uses the first 16 hex chars of the UUID = 8 bytes.
func buildOTelSpanContext(loomTraceID, loomSpanID string) (oteltrace.SpanContext, bool) {
	tHex := strings.ReplaceAll(loomTraceID, "-", "")
	sHex := strings.ReplaceAll(loomSpanID, "-", "")

	if len(tHex) < 32 || len(sHex) < 16 {
		return oteltrace.SpanContext{}, false
	}

	tBytes, err := hex.DecodeString(tHex[:32])
	if err != nil {
		return oteltrace.SpanContext{}, false
	}
	sBytes, err := hex.DecodeString(sHex[:16])
	if err != nil {
		return oteltrace.SpanContext{}, false
	}

	var traceID oteltrace.TraceID
	var spanID oteltrace.SpanID
	copy(traceID[:], tBytes)
	copy(spanID[:], sBytes)

	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     false,
	})
	return sc, sc.IsValid()
}

// RedactSpan returns span with the configured privacy rules applied to a copy
// of its attributes, events and status. Allowed and resource attributes are
// left unchanged. Unsupported values are omitted and reported to OpenTelemetry.
func RedactSpan(span *Span, cfg PrivacyConfig) *Span {
	if span == nil {
		return nil
	}
	return redactOTelSpan(span, cfg)
}

func redactOTelSpan(span *Span, cfg PrivacyConfig) *Span {
	if !cfg.RedactCredentials && !cfg.RedactPII {
		return span
	}

	allowed := make(map[string]bool, len(cfg.AllowedAttributes))
	for _, k := range cfg.AllowedAttributes {
		allowed[k] = true
	}

	redacted := *span
	redacted.Attributes = redactAttributes(span.Attributes, cfg, allowed, "span")
	redacted.Events = make([]Event, len(span.Events))
	for i, event := range span.Events {
		redacted.Events[i] = event
		redacted.Events[i].Attributes = redactAttributes(event.Attributes, cfg, allowed, "event")
	}
	redacted.Status.Message = redactConfiguredString(span.Status.Message, cfg)
	return &redacted
}

func redactAttributes(attrs map[string]interface{}, cfg PrivacyConfig, allowed map[string]bool, scope string) map[string]interface{} {
	out := make(map[string]interface{}, len(attrs))
	for key, value := range attrs {
		if allowed[key] {
			out[key] = value
			continue
		}
		if cfg.RedactCredentials && isCredentialField(key) {
			continue
		}
		clean, err := redactAttributeValue(value, cfg)
		if err != nil {
			reportInvalidAttribute(scope, value)
			continue
		}
		out[key] = clean
	}
	return out
}

func redactConfiguredString(s string, cfg PrivacyConfig) string {
	switch {
	case cfg.RedactCredentials && cfg.RedactPII:
		return RedactText(s)
	case cfg.RedactCredentials:
		return redactCredentialString(s)
	case cfg.RedactPII:
		return redactPIIString(s)
	default:
		return s
	}
}

// redactAttributeValue normalizes JSON-marshalable composite values before
// walking them, so typed maps and slices are inspected at every depth.
func redactAttributeValue(value interface{}, cfg PrivacyConfig) (interface{}, error) {
	switch v := value.(type) {
	case nil, bool, int, int32, int64:
		return value, nil
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, fmt.Errorf("non-finite float")
		}
		return value, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("non-finite float")
		}
		return value, nil
	case string:
		return redactConfiguredString(v, cfg), nil
	case []string:
		out := make([]string, len(v))
		for i, s := range v {
			out[i] = redactConfiguredString(s, cfg)
		}
		return out, nil
	default:
		if err := inspectAttributeValue(reflect.ValueOf(value), make(map[attributeVisit]struct{}), 0); err != nil {
			return nil, err
		}
		encoded, err := marshalAttributeJSON(value)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var decoded interface{}
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		return redactJSONValue(decoded, cfg)
	}
}

type attributeVisit struct {
	typ    reflect.Type
	ptr    uintptr
	length int
}

const maxAttributeInspectionDepth = 10000

// Bytes would otherwise be base64-encoded before the redactor can inspect
// them. RawMessage is different: its contents are decoded as JSON below.
func inspectAttributeValue(value reflect.Value, seen map[attributeVisit]struct{}, depth int) error {
	if !value.IsValid() {
		return nil
	}
	if depth > maxAttributeInspectionDepth {
		return errors.New("attribute nesting exceeds inspection limit")
	}
	kind := value.Kind()
	if kind == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return inspectAttributeValue(value.Elem(), seen, depth+1)
	}
	if (kind == reflect.Slice || kind == reflect.Array) && value.Type().Elem().Kind() == reflect.Uint8 {
		if value.Type() == reflect.TypeOf(json.RawMessage(nil)) || value.Len() == 0 {
			return nil
		}
		return errors.New("cannot inspect opaque bytes")
	}
	switch kind {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if value.IsNil() {
			return nil
		}
		visit := attributeVisit{typ: value.Type(), ptr: value.Pointer()}
		if kind == reflect.Slice {
			visit.length = value.Len()
		}
		if _, ok := seen[visit]; ok {
			return nil
		}
		seen[visit] = struct{}{}
	}
	switch kind {
	case reflect.Pointer:
		return inspectAttributeValue(value.Elem(), seen, depth+1)
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if err := inspectAttributeValue(iter.Key(), seen, depth+1); err != nil {
				return err
			}
			if err := inspectAttributeValue(iter.Value(), seen, depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := inspectAttributeValue(value.Index(i), seen, depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		typ := value.Type()
		jsonMarshaler := reflect.TypeOf((*json.Marshaler)(nil)).Elem()
		textMarshaler := reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
		ptrType := reflect.PointerTo(typ)
		custom := typ.Implements(jsonMarshaler) || ptrType.Implements(jsonMarshaler) ||
			typ.Implements(textMarshaler) || ptrType.Implements(textMarshaler)
		for i := 0; i < value.NumField(); i++ {
			field := typ.Field(i)
			if !custom && ((!field.IsExported() && !field.Anonymous) || field.Tag.Get("json") == "-") {
				continue
			}
			if err := inspectAttributeValue(value.Field(i), seen, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func redactJSONValue(value interface{}, cfg PrivacyConfig) (interface{}, error) {
	switch v := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, child := range v {
			cleanKey := redactConfiguredString(key, cfg)
			if _, exists := out[cleanKey]; exists {
				return nil, errors.New("redacted attribute map keys collide")
			}
			if cfg.RedactCredentials && isCredentialField(key) {
				out[cleanKey] = RedactedValue
			} else {
				cleanChild, err := redactJSONValue(child, cfg)
				if err != nil {
					return nil, err
				}
				out[cleanKey] = cleanChild
			}
		}
		return out, nil
	case []interface{}:
		for i, child := range v {
			cleanChild, err := redactJSONValue(child, cfg)
			if err != nil {
				return nil, err
			}
			v[i] = cleanChild
		}
		return v, nil
	case string:
		return redactConfiguredString(v, cfg), nil
	}
	return value, nil
}

func reportInvalidAttribute(scope string, value interface{}) {
	kind := "nil"
	if typ := reflect.TypeOf(value); typ != nil {
		kind = typ.Kind().String()
	}
	otel.Handle(fmt.Errorf("observability: omitted unsupported %s attribute (kind %s)", scope, kind))
}

// Ensure OTelTracer implements Tracer.
var _ Tracer = (*OTelTracer)(nil)

// filteringSpanProcessor wraps an inner SpanProcessor and only forwards spans
// whose names match the configured include prefixes.
// When IncludePrefixes is empty every span passes through unchanged.
type filteringSpanProcessor struct {
	inner    sdktrace.SpanProcessor
	prefixes []string
}

func newFilteringSpanProcessor(inner sdktrace.SpanProcessor, cfg SpanFilterConfig) sdktrace.SpanProcessor {
	if len(cfg.IncludePrefixes) == 0 {
		return inner
	}
	return &filteringSpanProcessor{inner: inner, prefixes: cfg.IncludePrefixes}
}

func (f *filteringSpanProcessor) matches(name string) bool {
	for _, p := range f.prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func (f *filteringSpanProcessor) OnStart(parent context.Context, s sdktrace.ReadWriteSpan) {
	if f.matches(s.Name()) {
		f.inner.OnStart(parent, s)
	}
}

func (f *filteringSpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	if f.matches(s.Name()) {
		f.inner.OnEnd(s)
	}
}

func (f *filteringSpanProcessor) Shutdown(ctx context.Context) error {
	return f.inner.Shutdown(ctx)
}

func (f *filteringSpanProcessor) ForceFlush(ctx context.Context) error {
	return f.inner.ForceFlush(ctx)
}
