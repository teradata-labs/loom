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
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// buildOTelSpanContext
// ---------------------------------------------------------------------------

func TestBuildOTelSpanContext(t *testing.T) {
	t.Run("valid UUIDs produce valid span context", func(t *testing.T) {
		traceID := "550e8400-e29b-41d4-a716-446655440000"
		spanID := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

		sc, ok := buildOTelSpanContext(traceID, spanID)
		if !ok {
			t.Fatal("expected valid span context")
		}
		if !sc.IsValid() {
			t.Error("span context should be valid")
		}
		if !sc.IsSampled() {
			t.Error("span context should have sampled flag")
		}
	})

	t.Run("empty trace ID returns false", func(t *testing.T) {
		_, ok := buildOTelSpanContext("", "6ba7b810-9dad-11d1-80b4-00c04fd430c8")
		if ok {
			t.Error("expected false for empty trace ID")
		}
	})

	t.Run("empty span ID returns false", func(t *testing.T) {
		_, ok := buildOTelSpanContext("550e8400-e29b-41d4-a716-446655440000", "")
		if ok {
			t.Error("expected false for empty span ID")
		}
	})

	t.Run("non-hex IDs return false", func(t *testing.T) {
		_, ok := buildOTelSpanContext("not-a-uuid-at-all-zzzzzzzzzzzzzzzz", "also-not-hex-zzzzzzzz")
		if ok {
			t.Error("expected false for non-hex IDs")
		}
	})

	t.Run("same UUID produces deterministic output", func(t *testing.T) {
		traceID := "550e8400-e29b-41d4-a716-446655440000"
		spanID := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

		sc1, _ := buildOTelSpanContext(traceID, spanID)
		sc2, _ := buildOTelSpanContext(traceID, spanID)
		if sc1.TraceID() != sc2.TraceID() {
			t.Error("same input should produce same TraceID")
		}
		if sc1.SpanID() != sc2.SpanID() {
			t.Error("same input should produce same SpanID")
		}
	})
}

// ---------------------------------------------------------------------------
// spanKindFor
// ---------------------------------------------------------------------------

func TestSpanKindFor(t *testing.T) {
	tests := []struct {
		name     string
		spanName string
		want     oteltrace.SpanKind
	}{
		{"llm prefix", "llm.completion", oteltrace.SpanKindClient},
		{"backend prefix", "backend.query", oteltrace.SpanKindClient},
		{"mcp prefix", "mcp.tool_call", oteltrace.SpanKindClient},
		{"internal agent", "agent.step", oteltrace.SpanKindInternal},
		{"internal generic", "my.custom.op", oteltrace.SpanKindInternal},
		{"empty", "", oteltrace.SpanKindInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := spanKindFor(tc.spanName)
			if got != tc.want {
				t.Errorf("spanKindFor(%q) = %v, want %v", tc.spanName, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// loomToGenAI attribute translation
// ---------------------------------------------------------------------------

func TestLoomToGenAIMappings(t *testing.T) {
	expected := map[string]string{
		// LLM span — required OTel GenAI spec attributes
		"llm.provider":  "gen_ai.system",
		"llm.model":     "gen_ai.request.model",
		"llm.operation": "gen_ai.operation.name",
		// Token usage
		"llm.tokens.input":       "gen_ai.usage.input_tokens",
		"llm.tokens.output":      "gen_ai.usage.output_tokens",
		"llm.tokens.total":       "gen_ai.usage.total_tokens",
		"llm.tokens.cache_read":  "gen_ai.usage.cache_read_input_tokens",
		"llm.tokens.cache_write": "gen_ai.usage.cache_creation_input_tokens",
		"llm.stop_reason":        "gen_ai.response.finish_reasons",
		"llm.cost.usd":           "gen_ai.usage.cost",
		// Input/Output columns (Opik + standard content representation)
		"message.preview":  "gen_ai.prompt",
		"response.preview": "gen_ai.completion",
		// Conversation span token aggregates
		"conversation.tokens.input":  "gen_ai.usage.input_tokens",
		"conversation.tokens.output": "gen_ai.usage.output_tokens",
		"conversation.tokens.total":  "gen_ai.usage.total_tokens",
		"conversation.cost.usd":      "gen_ai.usage.cost",
		"conversation.stop_reason":   "gen_ai.response.finish_reasons",
		// Tool/MCP
		"tool.name":     "gen_ai.tool.name",
		"mcp.tool.name": "gen_ai.tool.name",
		"mcp.tool.args": "gen_ai.tool.call.arguments",
		// MCP server (OTel RPC semconv)
		"mcp.server.name":    "server.address",
		"mcp.server.version": "server.version",
		// Agent identity (OTel GenAI agent semconv, draft)
		"agent_id": "gen_ai.agent.id",
		// Error / exception semconv
		"error.message": "exception.message",
		"error.type":    "exception.type",
		"error.stack":   "exception.stacktrace",
		// Session
		"session.id": "session.id",
		"user.id":    "user.id",
	}
	for loomKey, otelKey := range expected {
		if got, ok := loomToGenAI[loomKey]; !ok || got != otelKey {
			t.Errorf("loomToGenAI[%q] = %q, want %q", loomKey, got, otelKey)
		}
	}
}

func TestGenAISystemNormalization(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"anthropic", "anthropic"},         // already matches spec — no normalization
		{"openai", "openai"},               // already matches spec
		{"ollama", "ollama"},               // community convention, kept as-is
		{"mistral", "mistral"},             // kept as-is
		{"bedrock", "aws.bedrock"},         // normalized
		{"bedrock-mantle", "aws.bedrock"},  // normalized (Mantle endpoint of the same service)
		{"azure-openai", "azure_openai"},   // normalized
		{"gemini", "google.generative_ai"}, // normalized
		{"huggingface", "hugging_face"},    // normalized
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			got := tc.provider
			if norm, ok := genAISystemNorm[tc.provider]; ok {
				got = norm
			}
			if got != tc.want {
				t.Errorf("normalize(%q) = %q, want %q", tc.provider, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// redactOTelSpan
// ---------------------------------------------------------------------------

func TestRedactOTelSpan(t *testing.T) {
	t.Run("no redaction when both flags false", func(t *testing.T) {
		span := &Span{Attributes: map[string]interface{}{
			"password": "s3cr3t",
			"email":    "user@example.com",
		}}
		out := redactOTelSpan(span, PrivacyConfig{})
		if out.Attributes["password"] != "s3cr3t" {
			t.Error("password should not be redacted when RedactCredentials=false")
		}
	})

	t.Run("credential keys removed when RedactCredentials=true", func(t *testing.T) {
		span := &Span{Attributes: map[string]interface{}{
			"password":     "s3cr3t",
			"api_key":      "key123",
			"llm.model":    "claude-3",
			"token":        "tok",
			"access_token": "at",
		}}
		out := redactOTelSpan(span, PrivacyConfig{RedactCredentials: true})
		for _, key := range []string{"password", "api_key", "token", "access_token"} {
			if _, exists := out.Attributes[key]; exists {
				t.Errorf("expected %q to be removed", key)
			}
		}
		if _, ok := out.Attributes["llm.model"]; !ok {
			t.Error("llm.model should not be removed")
		}
	})

	t.Run("PII redacted in string values when RedactPII=true", func(t *testing.T) {
		span := &Span{Attributes: map[string]interface{}{
			"user.query": "My email is john@example.com and SSN 123-45-6789",
		}}
		out := redactOTelSpan(span, PrivacyConfig{RedactPII: true})
		val, _ := out.Attributes["user.query"].(string)
		if strings.Contains(val, "john@example.com") {
			t.Error("email should be redacted")
		}
		if strings.Contains(val, "123-45-6789") {
			t.Error("SSN should be redacted")
		}
	})

	t.Run("AllowedAttributes skip redaction", func(t *testing.T) {
		span := &Span{Attributes: map[string]interface{}{
			"session.id": "session@special",
			"password":   "s3cr3t",
		}}
		cfg := PrivacyConfig{
			RedactCredentials: true,
			RedactPII:         true,
			AllowedAttributes: []string{"session.id"},
		}
		out := redactOTelSpan(span, cfg)
		if out.Attributes["session.id"] != "session@special" {
			t.Error("allowlisted attribute should not be redacted")
		}
		if _, exists := out.Attributes["password"]; exists {
			t.Error("password should be removed")
		}
	})

	t.Run("PII redacted in event attributes", func(t *testing.T) {
		span := &Span{
			Attributes: map[string]interface{}{},
			Events: []Event{
				{
					Name:      "tool.result",
					Timestamp: time.Now(),
					Attributes: map[string]interface{}{
						"content": "Contact: user@test.org",
					},
				},
			},
		}
		out := redactOTelSpan(span, PrivacyConfig{RedactPII: true})
		if strings.Contains(out.Events[0].Attributes["content"].(string), "user@test.org") {
			t.Error("email in event attribute should be redacted")
		}
	})

	t.Run("redaction does not mutate the caller span", func(t *testing.T) {
		span := &Span{
			Attributes: map[string]interface{}{"password": "s3cr3t"},
			Events:     []Event{{Attributes: map[string]interface{}{"content": "user@test.org"}}},
		}
		out := redactOTelSpan(span, PrivacyConfig{RedactCredentials: true, RedactPII: true})

		if _, exists := out.Attributes["password"]; exists {
			t.Error("redacted copy should remove credentials")
		}
		if span.Attributes["password"] != "s3cr3t" {
			t.Error("caller attributes were mutated")
		}
		if span.Events[0].Attributes["content"] != "user@test.org" {
			t.Error("caller event attributes were mutated")
		}
	})
}

// ---------------------------------------------------------------------------
// NewOTelTracer validation
// ---------------------------------------------------------------------------

func TestNewOTelTracerValidation(t *testing.T) {
	t.Run("returns error for empty endpoint", func(t *testing.T) {
		_, err := NewOTelTracer(OTelConfig{})
		if err == nil {
			t.Fatal("expected error for empty endpoint")
		}
		if !strings.Contains(err.Error(), "otlp_endpoint") {
			t.Errorf("error should mention otlp_endpoint, got: %v", err)
		}
	})

	t.Run("creates tracer with valid endpoint", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		tr, err := NewOTelTracer(OTelConfig{
			Endpoint:    srv.URL,
			ServiceName: "test-svc",
			Insecure:    true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = tr.Shutdown(context.Background())
	})
}

// ---------------------------------------------------------------------------
// StartSpan / EndSpan round-trip
// ---------------------------------------------------------------------------

func TestOTelTracerStartEndSpan(t *testing.T) {
	var requestCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr, err := NewOTelTracer(OTelConfig{
		Endpoint:    srv.URL,
		ServiceName: "test",
		Insecure:    true,
	})
	if err != nil {
		t.Fatalf("NewOTelTracer: %v", err)
	}
	t.Cleanup(func() { _ = tr.Shutdown(context.Background()) })

	t.Run("StartSpan returns non-nil span and context", func(t *testing.T) {
		ctx, span := tr.StartSpan(context.Background(), "llm.completion")
		if span == nil {
			t.Fatal("span should not be nil")
		}
		if span.Name != "llm.completion" {
			t.Errorf("span name = %q, want %q", span.Name, "llm.completion")
		}
		if span.TraceID == "" {
			t.Error("trace ID should be set")
		}
		if SpanFromContext(ctx) == nil {
			t.Error("span should be stored in context")
		}
		tr.EndSpan(span)
	})

	t.Run("EndSpan removes span from activeSpans", func(t *testing.T) {
		_, span := tr.StartSpan(context.Background(), "backend.query")
		spanID := span.SpanID

		// Should be in activeSpans after Start.
		if _, ok := tr.activeSpans.Load(spanID); !ok {
			t.Error("span should be in activeSpans after StartSpan")
		}

		tr.EndSpan(span)

		// Should be removed after End.
		if _, ok := tr.activeSpans.Load(spanID); ok {
			t.Error("span should be removed from activeSpans after EndSpan")
		}
	})

	t.Run("EndSpan nil is a no-op", func(t *testing.T) {
		// Should not panic.
		tr.EndSpan(nil)
	})

	t.Run("parent-child span links via context", func(t *testing.T) {
		parentCtx, parent := tr.StartSpan(context.Background(), "agent.step")
		_, child := tr.StartSpan(parentCtx, "llm.completion")

		if child.TraceID != parent.TraceID {
			t.Errorf("child should inherit parent TraceID: got %q, want %q", child.TraceID, parent.TraceID)
		}
		if child.ParentID != parent.SpanID {
			t.Errorf("child ParentID = %q, want %q", child.ParentID, parent.SpanID)
		}

		tr.EndSpan(child)
		tr.EndSpan(parent)
	})

	t.Run("span attributes set before EndSpan are exported", func(t *testing.T) {
		_, span := tr.StartSpan(context.Background(), "llm.completion")
		span.SetAttribute("llm.model", "claude-3-sonnet")
		span.SetAttribute("llm.tokens.input", int64(150))
		span.SetAttribute("llm.tokens.output", int64(50))

		// Verify attributes are recorded on the span before export.
		if got, ok := span.Attributes["llm.model"].(string); !ok || got != "claude-3-sonnet" {
			t.Errorf("span.Attributes[llm.model] = %v, want \"claude-3-sonnet\"", span.Attributes["llm.model"])
		}
		if got, ok := span.Attributes["llm.tokens.input"].(int64); !ok || got != 150 {
			t.Errorf("span.Attributes[llm.tokens.input] = %v, want 150", span.Attributes["llm.tokens.input"])
		}

		countBefore := atomic.LoadInt32(&requestCount)
		tr.EndSpan(span)
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tr.Flush(flushCtx); err != nil {
			t.Errorf("Flush error: %v", err)
		}
		if atomic.LoadInt32(&requestCount) <= countBefore {
			t.Error("expected at least one OTLP export request after EndSpan+Flush")
		}
	})

	t.Run("Flush triggers export", func(t *testing.T) {
		countBefore := atomic.LoadInt32(&requestCount)
		_, span := tr.StartSpan(context.Background(), "llm.completion")
		tr.EndSpan(span)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tr.Flush(ctx); err != nil {
			t.Errorf("Flush returned error: %v", err)
		}
		if atomic.LoadInt32(&requestCount) <= countBefore {
			t.Error("expected at least one OTLP export request after Flush")
		}
	})
}

// ---------------------------------------------------------------------------
// Parent-child span linkage — regression tests for blocking bug from review
// ---------------------------------------------------------------------------

// newInMemoryOTelTracer builds an OTelTracer backed by an InMemoryExporter so
// exported spans can be inspected without a live OTLP collector.
func newInMemoryOTelTracer(t *testing.T) (*OTelTracer, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp), // synchronous export so spans are available immediately
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return &OTelTracer{
		provider: provider,
		tracer:   provider.Tracer("loom"),
		privacy:  PrivacyConfig{},
	}, exp
}

// TestParentChildSameTrace verifies that a child span exported by OTelTracer
// shares the same TraceID as its parent and correctly references the parent's
// SpanID — not a UUID-derived orphan.
func TestParentChildSameTrace(t *testing.T) {
	tr, exp := newInMemoryOTelTracer(t)

	// Start parent span and keep it active while the child is created.
	ctx, parent := tr.StartSpan(context.Background(), "parent.op")

	// Start child with the parent context while parent is still active.
	_, child := tr.StartSpan(ctx, "child.op")
	tr.EndSpan(child)
	tr.EndSpan(parent)

	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 exported spans, got %d", len(spans))
	}

	byName := make(map[string]tracetest.SpanStub, 2)
	for _, s := range spans {
		byName[s.Name] = s
	}

	parentStub, ok := byName["parent.op"]
	if !ok {
		t.Fatal("parent.op span not found in exported spans")
	}
	childStub, ok := byName["child.op"]
	if !ok {
		t.Fatal("child.op span not found in exported spans")
	}

	// Both must share the same TraceID.
	if parentStub.SpanContext.TraceID() != childStub.SpanContext.TraceID() {
		t.Errorf("trace ID mismatch: parent=%s child=%s — child was orphaned into a separate trace",
			parentStub.SpanContext.TraceID(), childStub.SpanContext.TraceID())
	}

	// Child must reference parent's SDK-assigned SpanID.
	if childStub.Parent.SpanID() != parentStub.SpanContext.SpanID() {
		t.Errorf("parent span ID mismatch: child.ParentSpanID=%s parent.SpanID=%s",
			childStub.Parent.SpanID(), parentStub.SpanContext.SpanID())
	}
}

// TestThreeGenerationChain verifies the fix holds for a three-level A → B → C chain.
func TestThreeGenerationChain(t *testing.T) {
	tr, exp := newInMemoryOTelTracer(t)

	ctxA, spanA := tr.StartSpan(context.Background(), "A")
	ctxB, spanB := tr.StartSpan(ctxA, "B")
	_, spanC := tr.StartSpan(ctxB, "C")
	tr.EndSpan(spanC)
	tr.EndSpan(spanB)
	tr.EndSpan(spanA)

	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	spans := exp.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("expected 3 exported spans, got %d", len(spans))
	}

	byName := make(map[string]tracetest.SpanStub, 3)
	for _, s := range spans {
		byName[s.Name] = s
	}

	traceID := byName["A"].SpanContext.TraceID()
	for _, name := range []string{"A", "B", "C"} {
		if byName[name].SpanContext.TraceID() != traceID {
			t.Errorf("span %q has different TraceID %s; want %s",
				name, byName[name].SpanContext.TraceID(), traceID)
		}
	}

	if byName["B"].Parent.SpanID() != byName["A"].SpanContext.SpanID() {
		t.Errorf("B.ParentSpanID=%s != A.SpanID=%s",
			byName["B"].Parent.SpanID(), byName["A"].SpanContext.SpanID())
	}
	if byName["C"].Parent.SpanID() != byName["B"].SpanContext.SpanID() {
		t.Errorf("C.ParentSpanID=%s != B.SpanID=%s",
			byName["C"].Parent.SpanID(), byName["B"].SpanContext.SpanID())
	}
}

func TestOTelTracerExportsParentChildLinkage(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tr := &OTelTracer{
		provider: provider,
		tracer:   provider.Tracer("loom-test"),
		privacy:  PrivacyConfig{RedactCredentials: true, RedactPII: true},
	}
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	parentCtx, parent := tr.StartSpan(context.Background(), "agent.chat")
	_, child := tr.StartSpan(parentCtx, "llm.completion")
	tr.EndSpan(child)
	tr.EndSpan(parent)

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("exported spans = %d, want 2", len(spans))
	}
	var parentSpan, childSpan tracetest.SpanStub
	for _, span := range spans {
		switch span.Name {
		case "agent.chat":
			parentSpan = span
		case "llm.completion":
			childSpan = span
		}
	}
	if childSpan.SpanContext.TraceID() != parentSpan.SpanContext.TraceID() {
		t.Fatalf("child trace ID %s does not match parent %s", childSpan.SpanContext.TraceID(), parentSpan.SpanContext.TraceID())
	}
	if childSpan.Parent.SpanID() != parentSpan.SpanContext.SpanID() {
		t.Fatalf("child parent ID %s does not match parent span ID %s", childSpan.Parent.SpanID(), parentSpan.SpanContext.SpanID())
	}
}

// ---------------------------------------------------------------------------
// OTelConfig env resolution
// ---------------------------------------------------------------------------

func TestResolveOTelConfig(t *testing.T) {
	t.Run("explicit values not overwritten by env", func(t *testing.T) {
		cfg := resolveOTelConfig(OTelConfig{
			Endpoint:    "http://explicit:4318",
			ServiceName: "my-service",
		})
		if cfg.Endpoint != "http://explicit:4318" {
			t.Errorf("explicit endpoint should not be overridden, got %q", cfg.Endpoint)
		}
		if cfg.ServiceName != "my-service" {
			t.Errorf("explicit service name should not be overridden, got %q", cfg.ServiceName)
		}
	})

	t.Run("defaults applied for zero values", func(t *testing.T) {
		cfg := resolveOTelConfig(OTelConfig{Endpoint: "http://localhost:4318"})
		if cfg.Timeout == 0 {
			t.Error("timeout default should be non-zero")
		}
		if cfg.MaxBatchSize == 0 {
			t.Error("max batch size default should be non-zero")
		}
		if cfg.FlushInterval == 0 {
			t.Error("flush interval default should be non-zero")
		}
	})

	t.Run("service name defaults to loom when OTEL_SERVICE_NAME unset", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "")
		cfg := resolveOTelConfig(OTelConfig{Endpoint: "http://localhost:4318"})
		if cfg.ServiceName != "loom" {
			t.Errorf("expected default service name %q, got %q", "loom", cfg.ServiceName)
		}
	})

	t.Run("OTEL_SERVICE_NAME overrides loom default", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "my-agent")
		cfg := resolveOTelConfig(OTelConfig{})
		if cfg.ServiceName != "my-agent" {
			t.Errorf("expected OTEL_SERVICE_NAME %q, got %q", "my-agent", cfg.ServiceName)
		}
	})
}

func TestResolveOTLPEndpointEnv(t *testing.T) {
	t.Run("returns empty string when no env vars set", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
		t.Setenv("LOOM_OTLP_ENDPOINT", "")
		got := ResolveOTLPEndpointEnv()
		if got != "" {
			t.Errorf("expected empty string when no env vars set, got %q", got)
		}
	})

	t.Run("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT used verbatim", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://col:4318/v1/traces")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://should-be-ignored:4317")
		got := ResolveOTLPEndpointEnv()
		if got != "http://col:4318/v1/traces" {
			t.Errorf("expected traces-specific endpoint verbatim, got %q", got)
		}
	})

	t.Run("OTEL_EXPORTER_OTLP_ENDPOINT base URL appends /v1/traces", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://col:4317")
		got := ResolveOTLPEndpointEnv()
		if got != "http://col:4317/v1/traces" {
			t.Errorf("expected base URL + /v1/traces, got %q", got)
		}
	})

	t.Run("OTEL_EXPORTER_OTLP_ENDPOINT trailing slash is normalised", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://col:4317/")
		got := ResolveOTLPEndpointEnv()
		if got != "http://col:4317/v1/traces" {
			t.Errorf("trailing slash not stripped, got %q", got)
		}
	})

	t.Run("LOOM_OTLP_ENDPOINT fallback used verbatim", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
		t.Setenv("LOOM_OTLP_ENDPOINT", "http://loom-col:4318/v1/traces")
		got := ResolveOTLPEndpointEnv()
		if got != "http://loom-col:4318/v1/traces" {
			t.Errorf("expected LOOM_OTLP_ENDPOINT fallback, got %q", got)
		}
	})
}

func TestResolveOTelConfigHeaders(t *testing.T) {
	t.Run("OTEL_EXPORTER_OTLP_TRACES_HEADERS preferred over base", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "X-Traces=traces-val")
		t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "X-Base=base-val")
		cfg := resolveOTelConfig(OTelConfig{Endpoint: "http://localhost:4318"})
		if cfg.Headers["X-Traces"] != "traces-val" {
			t.Errorf("expected traces-specific header, got %v", cfg.Headers)
		}
	})

	t.Run("OTEL_EXPORTER_OTLP_HEADERS used when traces header unset", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "")
		t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer tok")
		cfg := resolveOTelConfig(OTelConfig{Endpoint: "http://localhost:4318"})
		if cfg.Headers["Authorization"] != "Bearer tok" {
			t.Errorf("expected base OTLP header, got %v", cfg.Headers)
		}
	})
}

func TestResolveOTelConfigInsecure(t *testing.T) {
	t.Run("Insecure stays false when LOOM_OTLP_INSECURE is unset", func(t *testing.T) {
		t.Setenv("LOOM_OTLP_INSECURE", "")
		cfg := resolveOTelConfig(OTelConfig{Endpoint: "https://collector:4318"})
		if cfg.Insecure {
			t.Error("expected Insecure=false when LOOM_OTLP_INSECURE is unset")
		}
	})

	t.Run("LOOM_OTLP_INSECURE sets Insecure when not already true", func(t *testing.T) {
		t.Setenv("LOOM_OTLP_INSECURE", "true")
		cfg := resolveOTelConfig(OTelConfig{Endpoint: "http://localhost:4318"})
		if !cfg.Insecure {
			t.Error("expected Insecure=true from LOOM_OTLP_INSECURE")
		}
	})

	t.Run("explicit Insecure=true not overwritten by LOOM_OTLP_INSECURE=false", func(t *testing.T) {
		t.Setenv("LOOM_OTLP_INSECURE", "false")
		cfg := resolveOTelConfig(OTelConfig{Endpoint: "http://localhost:4318", Insecure: true})
		if !cfg.Insecure {
			t.Error("explicit Insecure=true should not be overwritten")
		}
	})
}

// ---------------------------------------------------------------------------
// OTLP/HTTP receiver: path, project header and payload reach the backend
// ---------------------------------------------------------------------------

type otlpRequest struct {
	path    string
	project string
	body    []byte
	err     error
}

// newOTLPReceiver starts an httptest server that records every request it gets.
func newOTLPReceiver(t *testing.T) (*httptest.Server, <-chan otlpRequest) {
	t.Helper()
	reqs := make(chan otlpRequest, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := otlpRequest{path: r.URL.Path, project: r.Header.Get("x-project-name")}
		var reader io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				rec.err = err
				reqs <- rec
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer func() { _ = gz.Close() }()
			reader = gz
		}
		rec.body, rec.err = io.ReadAll(reader)
		reqs <- rec
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, reqs
}

func TestOTelTracerExportsToOTLPReceiver(t *testing.T) {
	// Env headers would replace the configured ones; keep the test hermetic.
	for _, k := range []string{"OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_HEADERS", "LOOM_OTLP_HEADERS"} {
		t.Setenv(k, "")
	}

	t.Run("mode otel sends the span to /v1/traces with x-project-name", func(t *testing.T) {
		srv, reqs := newOTLPReceiver(t)

		tr, err := NewAutoSelectTracer(&AutoSelectConfig{
			Mode:         TracerModeOTel,
			OTLPEndpoint: srv.URL + "/v1/traces",
			OTLPHeaders:  map[string]string{"x-project-name": "loom-receiver-test"},
			OTLPInsecure: true,
			ServiceName:  "loom-test",
		})
		if err != nil {
			t.Fatalf("NewAutoSelectTracer: %v", err)
		}
		otelTracer, ok := tr.(*OTelTracer)
		if !ok {
			t.Fatalf("mode otel returned %T, want *OTelTracer", tr)
		}
		t.Cleanup(func() { _ = otelTracer.Shutdown(context.Background()) })

		_, span := tr.StartSpan(context.Background(), "agent.chat")
		tr.EndSpan(span)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := tr.Flush(ctx); err != nil {
			t.Fatalf("Flush: %v", err)
		}

		var rec otlpRequest
		select {
		case rec = <-reqs:
		case <-time.After(5 * time.Second):
			t.Fatal("receiver got no request after Flush")
		}
		if rec.err != nil {
			t.Fatalf("reading request body: %v", rec.err)
		}
		if rec.path != "/v1/traces" {
			t.Errorf("request path = %q, want /v1/traces", rec.path)
		}
		if rec.project != "loom-receiver-test" {
			t.Errorf("x-project-name header = %q, want loom-receiver-test", rec.project)
		}

		var payload coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(rec.body, &payload); err != nil {
			t.Fatalf("decoding OTLP payload: %v", err)
		}
		var names []string
		for _, rs := range payload.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				for _, s := range ss.GetSpans() {
					names = append(names, s.GetName())
				}
			}
		}
		if len(names) == 0 {
			t.Fatal("decoded OTLP payload has no spans")
		}
		found := false
		for _, n := range names {
			if n == "agent.chat" {
				found = true
			}
		}
		if !found {
			t.Errorf("exported spans %v do not include agent.chat", names)
		}
	})

	t.Run("mode none sends no request", func(t *testing.T) {
		srv, reqs := newOTLPReceiver(t)

		tr, err := NewAutoSelectTracer(&AutoSelectConfig{
			Mode:         TracerModeNone,
			OTLPEndpoint: srv.URL + "/v1/traces",
			OTLPHeaders:  map[string]string{"x-project-name": "loom-receiver-test"},
			OTLPInsecure: true,
			ServiceName:  "loom-test",
		})
		if err != nil {
			t.Fatalf("NewAutoSelectTracer: %v", err)
		}

		_, span := tr.StartSpan(context.Background(), "agent.chat")
		tr.EndSpan(span)
		if err := tr.Flush(context.Background()); err != nil {
			t.Fatalf("Flush: %v", err)
		}

		select {
		case rec := <-reqs:
			t.Fatalf("mode none sent a request to %q", rec.path)
		case <-time.After(500 * time.Millisecond):
		}
	})
}

type failingAttributeMarshaler struct{}

func (failingAttributeMarshaler) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private-marshaler-error")
}

type panickingAttributeMarshaler struct{}

func (panickingAttributeMarshaler) MarshalJSON() ([]byte, error) {
	panic("private-marshaler-panic")
}

type typedAttributeBytes []byte

type attributeWithBytes struct {
	Data []byte `json:"data"`
}

type attributeWithIgnoredBytes struct {
	Name   string `json:"name"`
	Secret []byte `json:"-"`
	hidden []byte
}

type attributeWithPrivateMarshaledBytes struct {
	secret []byte
}

func (v attributeWithPrivateMarshaledBytes) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.secret)
}

func TestOTelTracerExportsRedactedTypedValues(t *testing.T) {
	for _, k := range []string{"OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_HEADERS", "LOOM_OTLP_HEADERS"} {
		t.Setenv(k, "")
	}
	var mu sync.Mutex
	var reported []error
	previous := otel.GetErrorHandler()
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, err)
	}))
	t.Cleanup(func() { otel.SetErrorHandler(previous) })

	srv, reqs := newOTLPReceiver(t)
	tr, err := NewOTelTracer(OTelConfig{
		Endpoint:    srv.URL + "/v1/traces",
		ServiceName: "redaction-test",
		Insecure:    true,
		Privacy:     PrivacyConfig{RedactCredentials: true, RedactPII: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Shutdown(context.Background()) })

	input := map[string]interface{}{
		"typed": []string{"password=hunter2", "email=user@example.com"},
		"nested": []map[string]string{{
			"accessToken": "opaque-nested-secret",
			"contact":     "other@example.com",
			"safe":        "preserved",
		}},
		"nested_keys": map[string]string{
			"password=secret-in-key": "safe",
			"owner@example.com":      "safe",
		},
	}
	_, span := tr.StartSpan(context.Background(), "redaction.test")
	span.SetAttribute("payload", input)
	span.SetAttribute("list", []string{"password=hunter2", "user@example.com"})
	span.SetAttribute("accessToken", "opaque-key-secret")
	span.SetAttribute("bad", make(chan int))
	span.SetAttribute("nested_bad", map[string]interface{}{"value": make(chan int)})
	span.SetAttribute("marshal_error", failingAttributeMarshaler{})
	span.SetAttribute("marshal_panic", panickingAttributeMarshaler{})
	byteSecret := []byte("password=secret-in-bytes")
	span.SetAttribute("bytes", byteSecret)
	span.SetAttribute("nested_bytes", map[string]interface{}{
		"items": []interface{}{map[string]interface{}{"value": typedAttributeBytes(byteSecret)}},
	})
	span.SetAttribute("struct_bytes", attributeWithBytes{Data: byteSecret})
	span.SetAttribute("embedded_bytes", struct{ attributeWithBytes }{attributeWithBytes{Data: byteSecret}})
	span.SetAttribute("array_bytes", [4]byte{'s', 'a', 'f', 'e'})
	overlapping := []interface{}{"safe", byteSecret}
	span.SetAttribute("overlapping", []interface{}{overlapping[:1], overlapping[:2]})
	span.SetAttribute("colliding_keys", map[string]string{"password=first-secret": "a", "password=second-secret": "b"})
	span.SetAttribute("raw_json", json.RawMessage(`{"password":"raw-json-secret","safe":"preserved"}`))
	span.SetAttribute("ignored_bytes", attributeWithIgnoredBytes{Name: "preserved", Secret: byteSecret, hidden: byteSecret})
	span.SetAttribute("custom_bytes", attributeWithPrivateMarshaledBytes{secret: byteSecret})
	span.SetAttribute("timestamp", time.Unix(1, 0).In(time.FixedZone("test", 0)))
	cyclic := make(map[string]interface{})
	cyclic["self"] = cyclic
	span.SetAttribute("cyclic", cyclic)
	dynamicType := reflect.StructOf([]reflect.StructField{{
		Name: "Value",
		Type: reflect.TypeOf(make(chan int)),
		Tag:  `json:"password=secret-in-type-tag"`,
	}})
	span.SetAttribute("dynamic_bad", reflect.New(dynamicType).Elem().Interface())
	span.AddEvent("result", map[string]interface{}{
		"payload":      input,
		"refreshToken": "opaque-key-secret",
		"bad":          make(chan int),
		"bytes":        byteSecret,
		"nested_bytes": map[string]interface{}{"value": &attributeWithBytes{Data: byteSecret}},
	})
	span.Status = Status{Code: StatusError, Message: "password=hunter2 contact user@example.com"}
	tr.EndSpan(span)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tr.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var rec otlpRequest
	select {
	case rec = <-reqs:
	case <-ctx.Done():
		t.Fatal("receiver did not get a redacted span")
	}
	if rec.err != nil {
		t.Fatal(rec.err)
	}
	for _, secret := range []string{"hunter2", "user@example.com", "other@example.com", "opaque-nested-secret",
		"opaque-key-secret", "raw-json-secret", "secret-in-key", "owner@example.com", "first-secret", "second-secret",
		string(byteSecret), base64.StdEncoding.EncodeToString(byteSecret)} {
		if strings.Contains(string(rec.body), secret) {
			t.Errorf("secret %q appeared in the OTLP payload", secret)
		}
	}
	var payload coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(rec.body, &payload); err != nil {
		t.Fatal(err)
	}
	resources := payload.GetResourceSpans()
	if len(resources) != 1 || len(resources[0].GetScopeSpans()) != 1 || len(resources[0].GetScopeSpans()[0].GetSpans()) != 1 {
		t.Fatal("expected exactly one exported span")
	}
	exported := resources[0].GetScopeSpans()[0].GetSpans()[0]
	spanValues := make(map[string]string)
	for _, attr := range exported.GetAttributes() {
		spanValues[attr.GetKey()] = attr.GetValue().GetStringValue()
	}
	events := exported.GetEvents()
	if len(events) != 1 {
		t.Fatal("expected one exported event")
	}
	eventValues := make(map[string]string)
	for _, attr := range events[0].GetAttributes() {
		eventValues[attr.GetKey()] = attr.GetValue().GetStringValue()
	}
	for _, data := range []string{spanValues["loom.payload"], eventValues["payload"]} {
		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(data), &decoded); err != nil {
			t.Fatalf("nested value is not exported as JSON: %q: %v", data, err)
		}
		if !strings.Contains(data, RedactedValue) || !strings.Contains(data, "[EMAIL_REDACTED]") || !strings.Contains(data, "preserved") {
			t.Errorf("typed values were not correctly redacted: %q", data)
		}
	}
	for _, key := range []string{"loom.accessToken", "loom.bad", "loom.nested_bad", "loom.marshal_error", "loom.marshal_panic",
		"loom.bytes", "loom.nested_bytes", "loom.struct_bytes", "loom.embedded_bytes", "loom.array_bytes", "loom.overlapping", "loom.colliding_keys", "loom.cyclic", "loom.custom_bytes", "loom.dynamic_bad"} {
		if _, ok := spanValues[key]; ok {
			t.Errorf("unsafe span attribute %q survived", key)
		}
	}
	if raw := spanValues["loom.raw_json"]; !strings.Contains(raw, RedactedValue) || !strings.Contains(raw, "preserved") {
		t.Errorf("inspectable raw JSON was not preserved and redacted: %q", raw)
	}
	if ignored := spanValues["loom.ignored_bytes"]; !strings.Contains(ignored, "preserved") {
		t.Errorf("struct with non-serialized bytes was not preserved: %q", ignored)
	}
	if spanValues["loom.timestamp"] != "1970-01-01T00:00:01Z" {
		t.Errorf("inspectable marshaler was not preserved: %q", spanValues["loom.timestamp"])
	}
	for _, key := range []string{"refreshToken", "bad", "bytes", "nested_bytes"} {
		if _, ok := eventValues[key]; ok {
			t.Errorf("unsafe event attribute %q survived", key)
		}
	}
	if strings.Contains(exported.GetStatus().GetMessage(), "hunter2") || strings.Contains(exported.GetStatus().GetMessage(), "user@example.com") {
		t.Errorf("status message was not redacted: %q", exported.GetStatus().GetMessage())
	}
	if input["typed"].([]string)[0] != "password=hunter2" ||
		span.Events[0].Attributes["refreshToken"] != "opaque-key-secret" {
		t.Error("redaction modified the caller's nested values or event")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reported) < 17 {
		t.Errorf("reported %d conversion failures, want at least 17", len(reported))
	}
	for _, err := range reported {
		for _, secret := range []string{"private-marshaler", "secret-in-bytes", "first-secret", "second-secret", "secret-in-type-tag"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error handler exposed a value or marshaler message: %v", err)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestOTelTracerImplementsTracer(t *testing.T) {
	var _ Tracer = (*OTelTracer)(nil) // compile-time check duplicated for clarity
}
