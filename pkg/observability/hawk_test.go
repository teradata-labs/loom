// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk

package observability

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestHawkTracer(t *testing.T) {
	t.Run("NewHawkTracer validates config", func(t *testing.T) {
		_, err := NewHawkTracer(HawkConfig{})
		if err == nil {
			t.Error("Expected error for empty config")
		}

		tracer, err := NewHawkTracer(HawkConfig{
			Endpoint: "http://localhost:9090",
		})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		defer tracer.Close()

		// Verify defaults
		if tracer.config.BatchSize != 100 {
			t.Errorf("Expected default BatchSize 100, got %d", tracer.config.BatchSize)
		}
		if tracer.config.FlushInterval != 10*time.Second {
			t.Errorf("Expected default FlushInterval 10s, got %v", tracer.config.FlushInterval)
		}
	})

	t.Run("StartSpan and EndSpan", func(t *testing.T) {
		tracer, _ := NewHawkTracer(HawkConfig{
			Endpoint:  "http://localhost:9090",
			BatchSize: 10,
		})
		defer tracer.Close()

		ctx := context.Background()
		_, span := tracer.StartSpan(ctx, "test_span",
			WithAttribute("key", "value"),
		)

		if span.Name != "test_span" {
			t.Errorf("Expected name 'test_span', got %q", span.Name)
		}
		if span.Attributes["key"] != "value" {
			t.Errorf("Expected attribute key=value")
		}

		time.Sleep(10 * time.Millisecond)
		tracer.EndSpan(span)

		if span.Duration < 10*time.Millisecond {
			t.Errorf("Duration %v less than expected", span.Duration)
		}
	})

	t.Run("Exports to Hawk endpoint", func(t *testing.T) {
		// Mock Hawk server with mutex-protected payload
		var mu sync.Mutex
		var receivedPayload hawkExportPayload

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" {
				t.Errorf("Expected POST, got %s", r.Method)
			}
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("Expected Content-Type application/json")
			}
			if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
				t.Errorf("Expected Authorization header, got %q", auth)
			}

			body, _ := io.ReadAll(r.Body)
			var payload hawkExportPayload
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Errorf("Failed to unmarshal payload: %v", err)
			}

			// Thread-safe write
			mu.Lock()
			receivedPayload = payload
			mu.Unlock()

			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		tracer, _ := NewHawkTracer(HawkConfig{
			Endpoint:  server.URL,
			APIKey:    "test-key",
			BatchSize: 2,
		})
		defer tracer.Close()

		// Create and end two spans (triggers flush)
		ctx := context.Background()
		_, span1 := tracer.StartSpan(ctx, "span1")
		span1.SetAttribute("test", "value1")
		tracer.EndSpan(span1)

		_, span2 := tracer.StartSpan(ctx, "span2")
		span2.SetAttribute("test", "value2")
		tracer.EndSpan(span2)

		// Wait for flush
		time.Sleep(100 * time.Millisecond)

		// Thread-safe read
		mu.Lock()
		spans := receivedPayload.Spans
		mu.Unlock()

		if len(spans) != 2 {
			t.Errorf("Expected 2 spans, got %d", len(spans))
		}
		if len(spans) >= 2 {
			if spans[0].Name != "span1" {
				t.Errorf("Expected span1, got %s", spans[0].Name)
			}
			if spans[1].Name != "span2" {
				t.Errorf("Expected span2, got %s", spans[1].Name)
			}
		}
	})

	t.Run("Redacts credentials", func(t *testing.T) {
		tracer, _ := NewHawkTracer(HawkConfig{
			Endpoint: "http://localhost:9090",
			Privacy: PrivacyConfig{
				RedactCredentials: true,
			},
		})
		defer tracer.Close()

		span := &Span{
			Attributes: map[string]interface{}{
				"password":     "secret123",
				"api_key":      "key-abc",
				"normal_field": "public-data",
				"session.id":   "sess-123",
			},
		}

		redacted := tracer.redact(span)

		if _, exists := redacted.Attributes["password"]; exists {
			t.Error("password should be redacted")
		}
		if _, exists := redacted.Attributes["api_key"]; exists {
			t.Error("api_key should be redacted")
		}
		if redacted.Attributes["normal_field"] != "public-data" {
			t.Error("normal_field should not be redacted")
		}
	})

	t.Run("Respects allowed attributes", func(t *testing.T) {
		tracer, _ := NewHawkTracer(HawkConfig{
			Endpoint: "http://localhost:9090",
			Privacy: PrivacyConfig{
				RedactCredentials: true,
				AllowedAttributes: []string{"api_key"}, // Whitelist api_key
			},
		})
		defer tracer.Close()

		span := &Span{
			Attributes: map[string]interface{}{
				"password": "secret123",
				"api_key":  "key-abc", // Should be preserved
			},
		}

		redacted := tracer.redact(span)

		if _, exists := redacted.Attributes["password"]; exists {
			t.Error("password should be redacted")
		}
		if redacted.Attributes["api_key"] != "key-abc" {
			t.Error("api_key should be preserved (in allowlist)")
		}
	})

	t.Run("Flush exports buffered spans", func(t *testing.T) {
		var mu sync.Mutex
		var receivedCount int

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload hawkExportPayload
			_ = json.NewDecoder(r.Body).Decode(&payload)

			// Thread-safe write
			mu.Lock()
			receivedCount = len(payload.Spans)
			mu.Unlock()

			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		tracer, _ := NewHawkTracer(HawkConfig{
			Endpoint:  server.URL,
			BatchSize: 100, // Large buffer, won't auto-flush
		})
		defer tracer.Close()

		// Create 3 spans but don't trigger auto-flush
		ctx := context.Background()
		for i := 0; i < 3; i++ {
			_, span := tracer.StartSpan(ctx, "span")
			tracer.EndSpan(span)
		}

		// Manual flush
		if err := tracer.Flush(context.Background()); err != nil {
			t.Errorf("Flush error: %v", err)
		}

		time.Sleep(50 * time.Millisecond)

		// Thread-safe read
		mu.Lock()
		count := receivedCount
		mu.Unlock()

		if count != 3 {
			t.Errorf("Expected 3 spans flushed, got %d", count)
		}
	})
}

func TestTraceBuffer(t *testing.T) {
	t.Run("Buffer operations", func(t *testing.T) {
		buffer := newTraceBuffer(3)

		span1 := &Span{SpanID: "1"}
		span2 := &Span{SpanID: "2"}

		buffer.add(span1)
		if buffer.shouldFlush() {
			t.Error("Should not flush with 1 span (capacity 3)")
		}

		buffer.add(span2)
		if buffer.shouldFlush() {
			t.Error("Should not flush with 2 spans (capacity 3)")
		}

		span3 := &Span{SpanID: "3"}
		buffer.add(span3)
		if !buffer.shouldFlush() {
			t.Error("Should flush with 3 spans (capacity 3)")
		}

		drained := buffer.drain()
		if len(drained) != 3 {
			t.Errorf("Expected 3 drained spans, got %d", len(drained))
		}

		if buffer.shouldFlush() {
			t.Error("Should not flush after drain")
		}
	})

	t.Run("Thread safety", func(t *testing.T) {
		buffer := newTraceBuffer(100)

		// Concurrent adds
		done := make(chan bool)
		for i := 0; i < 10; i++ {
			go func() {
				for j := 0; j < 10; j++ {
					buffer.add(&Span{})
				}
				done <- true
			}()
		}

		for i := 0; i < 10; i++ {
			<-done
		}

		drained := buffer.drain()
		if len(drained) != 100 {
			t.Errorf("Expected 100 spans, got %d", len(drained))
		}
	})
}
