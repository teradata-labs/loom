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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"
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
	defer tr.Shutdown(context.Background())

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

	t.Run("LOOM_OTLP_INSECURE env var enables insecure mode", func(t *testing.T) {
		t.Setenv("LOOM_OTLP_INSECURE", "true")
		cfg := resolveOTelConfig(OTelConfig{Endpoint: "http://localhost:4318"})
		if !cfg.Insecure {
			t.Error("Insecure should be true when LOOM_OTLP_INSECURE=true")
		}
	})

	t.Run("LOOM_OTLP_INSECURE env unset leaves Insecure false", func(t *testing.T) {
		t.Setenv("LOOM_OTLP_INSECURE", "")
		cfg := resolveOTelConfig(OTelConfig{Endpoint: "http://localhost:4318"})
		if cfg.Insecure {
			t.Error("Insecure should remain false when LOOM_OTLP_INSECURE is not set")
		}
	})

	t.Run("explicit Insecure=true not cleared by missing env", func(t *testing.T) {
		t.Setenv("LOOM_OTLP_INSECURE", "")
		cfg := resolveOTelConfig(OTelConfig{Endpoint: "http://localhost:4318", Insecure: true})
		if !cfg.Insecure {
			t.Error("explicit Insecure=true should not be cleared by resolveOTelConfig")
		}
	})
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestOTelTracerImplementsTracer(t *testing.T) {
	var _ Tracer = (*OTelTracer)(nil) // compile-time check duplicated for clarity
}
