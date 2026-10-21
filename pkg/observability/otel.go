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
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
	recorder    *EvalRunRecorder // optional local eval_runs record (OTelConfig.Recorder)
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
		sdktrace.WithRawSpanLimits(otelSpanLimits()),
	)

	return &OTelTracer{
		provider: provider,
		tracer:   provider.Tracer("loom"),
		privacy:  cfg.Privacy,
		recorder: cfg.Recorder,
	}, nil
}

// otelAttributeCountLimit is the per-span attribute limit OTelTracer uses
// unless OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT sets one. The SDK default, 128,
// silently drops the attributes past it, and an LLM span that records its
// messages (otlp_capture_llm_messages) has more than that.
const otelAttributeCountLimit = 4096

// otelSpanLimits returns the SDK's span limits (environment variables
// included) with the attribute count limit raised to otelAttributeCountLimit
// when the environment does not set it.
func otelSpanLimits() sdktrace.SpanLimits {
	limits := sdktrace.NewSpanLimits()
	if os.Getenv("OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT") == "" {
		limits.AttributeCountLimit = otelAttributeCountLimit
	}
	return limits
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

	// Adopt the exported IDs so TraceIDFromContext, response trace IDs and
	// late children (spans started after their parent ended, which take the
	// synthetic-parent path above) all name the trace the backend stores.
	// This replaces any ContextWithTraceID override for this tracer.
	sc := otelSpan.SpanContext()
	loomSpan.TraceID = sc.TraceID().String()
	loomSpan.SpanID = sc.SpanID().String()
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

	// Local record first, from the span as Loom recorded it: the same row
	// mode: embedded writes (unredacted, Loom-native keys, unfiltered).
	t.recorder.Record(span)

	redacted := redactOTelSpan(span, t.privacy)

	// Translate span attributes and add the derived OpenInference attributes.
	otelSpan.SetAttributes(exportAttributes(span.Name, redacted.Attributes)...)

	// Forward resource attributes (service.name, agent.id, session.id, etc.).
	for k, v := range redacted.ResourceAttributes {
		otelSpan.SetAttributes(attribute.String(k, v))
	}

	// Forward span events.
	for _, ev := range redacted.Events {
		evAttrs := make([]attribute.KeyValue, 0, len(ev.Attributes))
		for k, v := range ev.Attributes {
			evAttrs = append(evAttrs, toOTelAttr(k, v))
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

// Flush forces immediate export of all buffered spans and blocks until done,
// then updates the local recorder's eval_metrics when one is configured.
func (t *OTelTracer) Flush(ctx context.Context) error {
	if err := t.provider.ForceFlush(ctx); err != nil {
		return err
	}
	if t.recorder != nil {
		return t.recorder.Flush(ctx)
	}
	return nil
}

// Shutdown gracefully drains buffered spans and stops the exporter, then
// closes the local recorder when one is configured.
// Call this on server shutdown after Flush.
func (t *OTelTracer) Shutdown(ctx context.Context) error {
	err := t.provider.Shutdown(ctx)
	if t.recorder != nil {
		if rerr := t.recorder.Close(); rerr != nil && err == nil {
			err = rerr
		}
	}
	return err
}

// buildOTelSpanContext constructs an OTel SpanContext from Loom span IDs.
//
// It accepts both ID forms Loom produces. Spans from OTelTracer carry the
// exported OTel IDs as hex (32-char trace ID, 16-char span ID), which decode
// to the same IDs. Spans from other tracers carry UUID strings
// (e.g. "550e8400-e29b-41d4-a716-446655440000"): stripped of dashes a UUID is
// 32 hex chars = 16 bytes, exactly an OTel TraceID, and the SpanID uses the
// first 16 hex chars = 8 bytes.
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

// redactOTelSpan applies privacy rules before attributes are exported, to
// span and event attributes alike: credential-like keys are dropped, and PII
// patterns (emailPattern, phonePattern, etc., defined in privacy.go) are
// replaced in string values, in each element of string slices, and in maps and
// lists, which are exported as JSON. With RedactPII the status message is
// redacted too. Keys in AllowedAttributes are exported unchanged. The caller's
// span is not modified.
// RedactSpan returns a copy of span cleaned the way OTLP export cleans it:
// attributes whose key names a credential are dropped, credentials inside
// attribute values (JSON documents, embedded JSON, header lines, Bearer/Basic
// credentials, well-known key formats, JWTs, URL parameters) are replaced with
// RedactedValue, and PII patterns are replaced, as cfg selects. Events and the
// status message are cleaned too. span is not modified.
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
	redacted.Attributes = redactAttributes(span.Attributes, cfg, allowed)
	redacted.Events = make([]Event, len(span.Events))
	for i, event := range span.Events {
		redacted.Events[i] = event
		redacted.Events[i].Attributes = redactAttributes(event.Attributes, cfg, allowed)
	}
	if cfg.RedactCredentials {
		redacted.Status.Message = redactCredentialString(redacted.Status.Message)
	}
	if cfg.RedactPII {
		redacted.Status.Message = redactPIIString(redacted.Status.Message)
	}
	return &redacted
}

// redactAttributes returns a redacted copy of attrs (see redactOTelSpan).
func redactAttributes(attrs map[string]interface{}, cfg PrivacyConfig, allowed map[string]bool) map[string]interface{} {
	out := make(map[string]interface{}, len(attrs))
	for k, v := range attrs {
		switch {
		case allowed[k]:
			out[k] = v
		case cfg.RedactCredentials && isCredentialKey(k):
			// dropped
		default:
			if cfg.RedactCredentials {
				v = redactCredentialValue(v)
			}
			if cfg.RedactPII {
				v = redactPIIValue(v)
			}
			out[k] = v
		}
	}
	return out
}

// credentialKeys are attribute keys dropped by RedactCredentials.
var credentialKeys = map[string]bool{ //nolint:gochecknoglobals
	"password": true, "api_key": true, "token": true, "secret": true, "authorization": true,
	"access_token": true, "refresh_token": true, "bearer": true, "apikey": true,
	"client_secret": true, "private_key": true, "ssh_key": true, "aws_secret": true,
}

// isCredentialKey reports whether an attribute key names a credential: one
// of credentialKeys, or a key containing "password", "secret", or both "api"
// and "key" (case-insensitive).
func isCredentialKey(k string) bool {
	if credentialKeys[k] {
		return true
	}
	kl := strings.ToLower(k)
	return strings.Contains(kl, "password") || strings.Contains(kl, "secret") ||
		(strings.Contains(kl, "key") && strings.Contains(kl, "api"))
}

// redactPIIValue replaces PII patterns in a string, in each element of a
// string slice (copied before it is changed), and in a map or list, which is
// returned as the JSON string toOTelAttr would export. Other values are
// returned as they are.
func redactPIIValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		return redactPIIString(val)
	case []string:
		var out []string
		for i, s := range val {
			if r := redactPIIString(s); r != s {
				if out == nil {
					out = append([]string(nil), val...)
				}
				out[i] = r
			}
		}
		if out == nil {
			return val
		}
		return out
	case map[string]interface{}, map[string]string, []interface{}:
		b, err := json.Marshal(val)
		if err != nil {
			return v
		}
		return redactPIIString(string(b))
	default:
		return v
	}
}

func redactPIIString(s string) string {
	r := emailPattern.ReplaceAllString(s, "[EMAIL_REDACTED]")
	r = phonePattern.ReplaceAllString(r, "[PHONE_REDACTED]")
	r = ssnPattern.ReplaceAllString(r, "[SSN_REDACTED]")
	return creditCardPattern.ReplaceAllString(r, "[CARD_REDACTED]")
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
