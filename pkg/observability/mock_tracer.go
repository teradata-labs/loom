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
	"sync"
	"time"
)

// MockTracer is a test implementation of Tracer that captures all spans for inspection.
// Thread-safe: All methods can be called concurrently.
type MockTracer struct {
	mu    sync.RWMutex
	spans []*Span
}

// NewMockTracer creates a new mock tracer for testing.
func NewMockTracer() *MockTracer {
	return &MockTracer{
		spans: make([]*Span, 0),
	}
}

// StartSpan creates a new span and stores it for inspection.
func (m *MockTracer) StartSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, *Span) {
	span := &Span{
		TraceID:    "trace-" + generateID(),
		SpanID:     "span-" + generateID(),
		Name:       name,
		StartTime:  time.Now(),
		Attributes: make(map[string]interface{}),
		Events:     make([]Event, 0),
	}

	// Apply options
	for _, opt := range opts {
		opt(span)
	}

	// Link to parent if exists
	if parent := SpanFromContext(ctx); parent != nil {
		span.TraceID = parent.TraceID
		span.ParentID = parent.SpanID
	}

	return ContextWithSpan(ctx, span), span
}

// EndSpan completes a span and stores it.
func (m *MockTracer) EndSpan(span *Span) {
	if span == nil {
		return
	}

	span.EndTime = time.Now()
	span.Duration = span.EndTime.Sub(span.StartTime)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.spans = append(m.spans, span)
}

// RecordMetric records a metric (captured but not stored in mock).
func (m *MockTracer) RecordMetric(name string, value float64, labels map[string]string) {
	// Mock implementation - metrics not stored
}

// RecordEvent records a standalone event (captured but not stored in mock).
func (m *MockTracer) RecordEvent(ctx context.Context, name string, attributes map[string]interface{}) {
	// Mock implementation - events not stored
}

// Flush is a no-op for mock tracer.
func (m *MockTracer) Flush(ctx context.Context) error {
	return nil
}

// GetSpans returns all captured spans (for testing).
func (m *MockTracer) GetSpans() []*Span {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Return a copy to prevent concurrent modification
	spans := make([]*Span, len(m.spans))
	copy(spans, m.spans)
	return spans
}

// Reset clears all captured spans (for testing).
func (m *MockTracer) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spans = make([]*Span, 0)
}

// GetSpanByName finds the first span with the given name (for testing).
func (m *MockTracer) GetSpanByName(name string) *Span {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, span := range m.spans {
		if span.Name == name {
			return span
		}
	}
	return nil
}

// GetSpansByName finds all spans with the given name (for testing).
func (m *MockTracer) GetSpansByName(name string) []*Span {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Span, 0)
	for _, span := range m.spans {
		if span.Name == name {
			result = append(result, span)
		}
	}
	return result
}

// Helper to generate simple IDs for testing
func generateID() string {
	return time.Now().Format("20060102150405.000000")
}

// Ensure MockTracer implements Tracer interface
var _ Tracer = (*MockTracer)(nil)
