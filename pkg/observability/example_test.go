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

package observability_test

import (
	"context"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
)

// Example shows basic tracer usage.
func Example() {
	// Create a Hawk tracer
	tracer, err := observability.NewHawkTracer(observability.HawkConfig{
		Endpoint:      "http://localhost:9090/v1/traces",
		APIKey:        "your-api-key",
		BatchSize:     100,
		FlushInterval: 10 * time.Second,
	})
	if err != nil {
		panic(err)
	}
	defer tracer.Flush(context.Background())

	// Start a conversation span
	ctx := context.Background()
	ctx, span := tracer.StartSpan(ctx, observability.SpanAgentConversation,
		observability.WithSpanKind("conversation"),
		observability.WithAttribute(observability.AttrSessionID, "sess-123"),
	)
	defer tracer.EndSpan(span)

	// Simulate LLM call (child span)
	simulateLLMCall(ctx, tracer)

	// Add event to span
	span.AddEvent("user_message_received", map[string]interface{}{
		"message": "Show me top 10 customers",
	})

	// Mark span as successful
	span.Status = observability.Status{Code: observability.StatusOK}

	// Output:
	// Conversation traced with ID: sess-123
}

func simulateLLMCall(ctx context.Context, tracer observability.Tracer) {
	_, span := tracer.StartSpan(ctx, observability.SpanLLMCompletion,
		observability.WithSpanKind("llm"),
		observability.WithAttribute(observability.AttrLLMProvider, "anthropic"),
		observability.WithAttribute(observability.AttrLLMModel, "claude-3-5-sonnet"),
	)
	defer tracer.EndSpan(span)

	// Simulate work
	time.Sleep(10 * time.Millisecond)

	// Record token usage
	span.SetAttribute("llm.response.input_tokens", 1200)
	span.SetAttribute("llm.response.output_tokens", 350)
	span.SetAttribute("llm.response.cost_usd", 0.023)

	span.Status = observability.Status{Code: observability.StatusOK}

	fmt.Println("Conversation traced with ID: sess-123")
}

// ExampleNoOpTracer shows using the no-op tracer for testing.
func ExampleNoOpTracer() {
	// Create a no-op tracer (doesn't export anything)
	tracer := observability.NewNoOpTracer()

	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test_operation")
	defer tracer.EndSpan(span)

	// Span is created but not exported
	fmt.Println("Operation completed")
	// Output: Operation completed
}

// ExampleSpan_AddEvent shows adding events to a span.
func ExampleSpan_AddEvent() {
	tracer := observability.NewNoOpTracer()

	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "data_processing")
	defer tracer.EndSpan(span)

	// Add events during processing
	span.AddEvent("validation_started", nil)

	span.AddEvent("validation_completed", map[string]interface{}{
		"rows_validated": 1000,
		"errors_found":   3,
	})

	span.AddEvent("correction_applied", map[string]interface{}{
		"correction_type": "schema_mismatch",
	})

	fmt.Printf("Recorded %d events\n", len(span.Events))
	// Output: Recorded 3 events
}
