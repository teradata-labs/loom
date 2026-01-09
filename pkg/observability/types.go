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
// Package observability provides distributed tracing and metrics for Loom agents.
//
// Every operation in Loom is instrumented: LLM calls, tool executions, pattern selections,
// and full conversation flows. Traces are exported to Hawk (or other backends) for analysis,
// debugging, and cost attribution.
//
// Example usage:
//
//	tracer := observability.NewHawkTracer(config)
//	ctx, span := tracer.StartSpan(ctx, "llm.completion")
//	defer tracer.EndSpan(span)
//	// ... do work ...
//	span.SetAttribute("llm.tokens", 1234)
package observability

import (
	"time"
)

// StatusCode represents the final status of a span.
type StatusCode int

const (
	// StatusUnset indicates status was not explicitly set.
	StatusUnset StatusCode = iota
	// StatusOK indicates successful completion.
	StatusOK
	// StatusError indicates an error occurred.
	StatusError
)

func (s StatusCode) String() string {
	switch s {
	case StatusUnset:
		return "unset"
	case StatusOK:
		return "ok"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// Status represents the final status of a span with optional message.
type Status struct {
	Code    StatusCode
	Message string
}

// Event represents a point-in-time occurrence within a span.
// Use for logging important events during span execution.
type Event struct {
	Timestamp  time.Time
	Name       string
	Attributes map[string]interface{}
}

// Span represents a unit of work with timing and metadata.
// Spans form a tree structure via ParentID references.
type Span struct {
	// Identifiers
	TraceID  string // Unique ID for entire trace
	SpanID   string // Unique ID for this span
	ParentID string // Parent span ID (empty for root)

	// Metadata
	Name       string                 // Operation name (e.g., "llm.completion")
	Attributes map[string]interface{} // Key-value metadata

	// Timing
	StartTime time.Time
	EndTime   time.Time
	Duration  time.Duration // Calculated on EndSpan

	// Events and status
	Events []Event
	Status Status
}

// SetAttribute sets a key-value attribute on the span.
func (s *Span) SetAttribute(key string, value interface{}) {
	if s.Attributes == nil {
		s.Attributes = make(map[string]interface{})
	}
	s.Attributes[key] = value
}

// AddEvent adds a timestamped event to the span.
func (s *Span) AddEvent(name string, attrs map[string]interface{}) {
	s.Events = append(s.Events, Event{
		Timestamp:  time.Now(),
		Name:       name,
		Attributes: attrs,
	})
}

// RecordError records an error on the span.
// Sets status to StatusError and adds error attributes.
func (s *Span) RecordError(err error) {
	if err == nil {
		return
	}
	s.Status = Status{
		Code:    StatusError,
		Message: err.Error(),
	}
	s.SetAttribute(AttrErrorMessage, err.Error())
	s.SetAttribute(AttrErrorType, "error")
}

// SpanOption is a functional option for configuring spans.
type SpanOption func(*Span)

// WithAttribute returns a SpanOption that sets an attribute.
func WithAttribute(key string, value interface{}) SpanOption {
	return func(s *Span) {
		s.SetAttribute(key, value)
	}
}

// WithSpanKind returns a SpanOption that sets the span.kind attribute.
// Common values: "conversation", "llm", "tool", "backend", "guardrail"
func WithSpanKind(kind string) SpanOption {
	return func(s *Span) {
		s.SetAttribute("span.kind", kind)
	}
}

// WithParentSpanID returns a SpanOption that explicitly sets the parent span ID.
func WithParentSpanID(parentID string) SpanOption {
	return func(s *Span) {
		s.ParentID = parentID
	}
}
