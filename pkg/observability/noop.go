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
	"time"

	"github.com/google/uuid"
)

// NoOpTracer is a tracer that does nothing.
// Use for testing or when observability is disabled.
type NoOpTracer struct{}

// NewNoOpTracer creates a no-op tracer.
func NewNoOpTracer() *NoOpTracer {
	return &NoOpTracer{}
}

// StartSpan creates a minimal span but doesn't export it.
func (t *NoOpTracer) StartSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, *Span) {
	span := &Span{
		TraceID:    uuid.New().String(),
		SpanID:     uuid.New().String(),
		Name:       name,
		StartTime:  time.Now(),
		Attributes: make(map[string]interface{}),
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

// EndSpan does nothing.
func (t *NoOpTracer) EndSpan(span *Span) {
	span.EndTime = time.Now()
	span.Duration = span.EndTime.Sub(span.StartTime)
}

// RecordMetric does nothing.
func (t *NoOpTracer) RecordMetric(name string, value float64, labels map[string]string) {}

// RecordEvent does nothing.
func (t *NoOpTracer) RecordEvent(ctx context.Context, name string, attributes map[string]interface{}) {
}

// Flush does nothing.
func (t *NoOpTracer) Flush(ctx context.Context) error {
	return nil
}

// Ensure NoOpTracer implements Tracer interface.
var _ Tracer = (*NoOpTracer)(nil)
