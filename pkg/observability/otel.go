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
	"fmt"
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
		// Prefer the live in-process OTel span so SDK propagation assigns the
		// same SDK-generated trace/span IDs to the child — not the UUID-derived
		// ones that would create a dangling remote parent.  Fall back to the
		// UUID-derived remote context only when no live span is registered (i.e.
		// the parent was created by a different process / tracer instance).
		if raw, ok := t.activeSpans.Load(parent.SpanID); ok {
			if parentOTelSpan, ok := raw.(oteltrace.Span); ok {
				otelCtx = oteltrace.ContextWithSpan(ctx, parentOTelSpan)
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

// redactOTelSpan applies privacy rules before attributes are exported.
// PII patterns (emailPattern, phonePattern, etc.) are defined in privacy.go.
func redactOTelSpan(span *Span, cfg PrivacyConfig) *Span {
	if !cfg.RedactCredentials && !cfg.RedactPII {
		return span
	}

	allowed := make(map[string]bool, len(cfg.AllowedAttributes))
	for _, k := range cfg.AllowedAttributes {
		allowed[k] = true
	}

	if cfg.RedactCredentials {
		credKeys := []string{
			"password", "api_key", "token", "secret", "authorization",
			"access_token", "refresh_token", "bearer", "apikey",
			"client_secret", "private_key", "ssh_key", "aws_secret",
		}
		for _, k := range credKeys {
			if !allowed[k] {
				delete(span.Attributes, k)
			}
		}
		for k := range span.Attributes {
			kl := strings.ToLower(k)
			if !allowed[k] {
				if strings.Contains(kl, "password") || strings.Contains(kl, "secret") ||
					(strings.Contains(kl, "key") && strings.Contains(kl, "api")) {
					delete(span.Attributes, k)
				}
			}
		}
	}

	if cfg.RedactPII {
		for k, v := range span.Attributes {
			if allowed[k] {
				continue
			}
			if s, ok := v.(string); ok {
				r := emailPattern.ReplaceAllString(s, "[EMAIL_REDACTED]")
				r = phonePattern.ReplaceAllString(r, "[PHONE_REDACTED]")
				r = ssnPattern.ReplaceAllString(r, "[SSN_REDACTED]")
				r = creditCardPattern.ReplaceAllString(r, "[CARD_REDACTED]")
				if r != s {
					span.Attributes[k] = r
				}
			}
		}
		for i := range span.Events {
			for k, v := range span.Events[i].Attributes {
				if allowed[k] {
					continue
				}
				if s, ok := v.(string); ok {
					r := emailPattern.ReplaceAllString(s, "[EMAIL_REDACTED]")
					r = phonePattern.ReplaceAllString(r, "[PHONE_REDACTED]")
					r = ssnPattern.ReplaceAllString(r, "[SSN_REDACTED]")
					r = creditCardPattern.ReplaceAllString(r, "[CARD_REDACTED]")
					if r != s {
						span.Events[i].Attributes[k] = r
					}
				}
			}
		}
	}

	return span
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
