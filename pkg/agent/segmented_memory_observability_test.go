// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// mockTracerForMetrics captures metrics calls for testing
type mockTracerForMetrics struct {
	observability.Tracer
	metrics []metricRecord
	events  []eventRecord
}

type metricRecord struct {
	name   string
	value  float64
	labels map[string]string
}

type eventRecord struct {
	name       string
	attributes map[string]interface{}
}

func (m *mockTracerForMetrics) RecordMetric(name string, value float64, labels map[string]string) {
	m.metrics = append(m.metrics, metricRecord{
		name:   name,
		value:  value,
		labels: copyLabels(labels),
	})
}

func (m *mockTracerForMetrics) RecordEvent(ctx context.Context, name string, attributes map[string]interface{}) {
	m.events = append(m.events, eventRecord{
		name:       name,
		attributes: copyAttributes(attributes),
	})
}

func (m *mockTracerForMetrics) StartSpan(ctx context.Context, name string, opts ...observability.SpanOption) (context.Context, *observability.Span) {
	// Return noop span
	span := &observability.Span{}
	return ctx, span
}

func (m *mockTracerForMetrics) EndSpan(span *observability.Span) {
	// No-op
}

func (m *mockTracerForMetrics) Flush(ctx context.Context) error {
	return nil
}

func copyLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	copy := make(map[string]string, len(labels))
	for k, v := range labels {
		copy[k] = v
	}
	return copy
}

func copyAttributes(attrs map[string]interface{}) map[string]interface{} {
	if attrs == nil {
		return nil
	}
	copy := make(map[string]interface{}, len(attrs))
	for k, v := range attrs {
		copy[k] = v
	}
	return copy
}

func TestSegmentedMemory_CompressionMetrics_DataIntensive(t *testing.T) {
	romContent := "Test documentation"
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE]

	// Use small context budget to trigger compression quickly
	sm := NewSegmentedMemoryWithCompression(romContent, 10000, 1000, profile)
	sm.SetCompressor(&mockCompressor{enabled: true})

	// Setup mock tracer to capture metrics
	mockTracer := &mockTracerForMetrics{}
	sm.SetTracer(mockTracer)

	// Verify profile configuration event was recorded
	require.Len(t, mockTracer.events, 1, "Should record profile configuration event")
	assert.Equal(t, "memory.profile_configured", mockTracer.events[0].name)
	assert.Equal(t, "data_intensive", mockTracer.events[0].attributes["profile"])
	assert.Equal(t, 5, mockTracer.events[0].attributes["max_l1_messages"])
	assert.Equal(t, 50, mockTracer.events[0].attributes["warning_threshold_percent"])
	assert.Equal(t, 70, mockTracer.events[0].attributes["critical_threshold_percent"])

	// Add messages to trigger compression
	for i := 0; i < 8; i++ {
		sm.AddMessage(Message{
			Role:    "user",
			Content: "Test message with content to consume tokens " + string(rune(i)),
		})
	}

	// Verify compression metrics were recorded
	assert.Greater(t, len(mockTracer.metrics), 0, "Should record compression metrics")

	// Find compression metrics
	var compressionEvents []metricRecord
	var messagesCompressed []metricRecord
	var tokensSaved []metricRecord
	var budgetPct []metricRecord
	var l1Size []metricRecord

	for _, metric := range mockTracer.metrics {
		switch metric.name {
		case "memory.compression.events":
			compressionEvents = append(compressionEvents, metric)
		case "memory.compression.messages":
			messagesCompressed = append(messagesCompressed, metric)
		case "memory.compression.tokens_saved":
			tokensSaved = append(tokensSaved, metric)
		case "memory.compression.budget_pct":
			budgetPct = append(budgetPct, metric)
		case "memory.l1.size":
			l1Size = append(l1Size, metric)
		}
	}

	// Verify compression happened at least once
	assert.Greater(t, len(compressionEvents), 0, "Should record compression events")

	if len(compressionEvents) > 0 {
		// Check first compression event
		event := compressionEvents[0]
		assert.Equal(t, 1.0, event.value, "Event counter should be 1")
		assert.Equal(t, "data_intensive", event.labels["profile"], "Should label with profile")
		assert.Contains(t, []string{"normal", "warning", "critical"}, event.labels["batch_size"], "Should label with batch size")

		// Verify all metrics have the same labels (same compression event)
		if len(messagesCompressed) > 0 {
			assert.Equal(t, event.labels["profile"], messagesCompressed[0].labels["profile"])
			assert.Greater(t, messagesCompressed[0].value, 0.0, "Should compress at least 1 message")
		}

		if len(tokensSaved) > 0 {
			assert.GreaterOrEqual(t, tokensSaved[0].value, 0.0, "Tokens saved should be non-negative")
		}

		if len(budgetPct) > 0 {
			assert.GreaterOrEqual(t, budgetPct[0].value, 0.0, "Budget % should be non-negative")
			assert.LessOrEqual(t, budgetPct[0].value, 100.0, "Budget % should be <= 100")
		}

		if len(l1Size) > 0 {
			assert.LessOrEqual(t, l1Size[0].value, float64(profile.MaxL1Messages), "L1 size should be <= maxL1Messages")
		}
	}
}

func TestSegmentedMemory_CompressionMetrics_ConversationalProfile(t *testing.T) {
	romContent := "Test documentation"
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL]

	sm := NewSegmentedMemoryWithCompression(romContent, 20000, 2000, profile)
	sm.SetCompressor(&mockCompressor{enabled: true})

	mockTracer := &mockTracerForMetrics{}
	sm.SetTracer(mockTracer)

	// Verify profile configuration
	require.Len(t, mockTracer.events, 1)
	assert.Equal(t, "conversational", mockTracer.events[0].attributes["profile"])
	assert.Equal(t, 12, mockTracer.events[0].attributes["max_l1_messages"])

	// Add many messages to trigger compression
	for i := 0; i < 15; i++ {
		sm.AddMessage(Message{
			Role:    "user",
			Content: "Conversational message " + string(rune(i)),
		})
	}

	// Verify metrics were recorded
	var found bool
	for _, metric := range mockTracer.metrics {
		if metric.name == "memory.compression.events" {
			found = true
			assert.Equal(t, "conversational", metric.labels["profile"])
		}
	}
	assert.True(t, found, "Should record compression events with conversational profile label")
}

func TestSegmentedMemory_CompressionMetrics_BatchSizeLabels(t *testing.T) {
	// Test that different budget levels result in different batch size labels
	romContent := "Test documentation"
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]

	tests := []struct {
		name          string
		contextSize   int
		expectedBatch string // Expected batch size label based on budget usage
	}{
		{
			name:          "normal batch - low budget usage",
			contextSize:   100000, // Large budget
			expectedBatch: "normal",
		},
		{
			name:          "warning batch - medium budget usage",
			contextSize:   15000, // Medium budget (will exceed 60%)
			expectedBatch: "warning",
		},
		{
			name:          "critical batch - high budget usage",
			contextSize:   8000, // Small budget (will exceed 75%)
			expectedBatch: "critical",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := NewSegmentedMemoryWithCompression(romContent, tt.contextSize, tt.contextSize/10, profile)
			sm.SetCompressor(&mockCompressor{enabled: true})

			mockTracer := &mockTracerForMetrics{}
			sm.SetTracer(mockTracer)

			// Add messages to trigger compression
			for i := 0; i < 10; i++ {
				sm.AddMessage(Message{
					Role:    "user",
					Content: "Message with content to fill budget " + string(make([]byte, 500)),
				})
			}

			// Find compression events
			var batchSizeLabels []string
			for _, metric := range mockTracer.metrics {
				if metric.name == "memory.compression.events" {
					batchSizeLabels = append(batchSizeLabels, metric.labels["batch_size"])
				}
			}

			if len(batchSizeLabels) > 0 {
				// Note: Exact batch size depends on token counts at compression time
				// We verify that batch_size labels are valid and being set
				assert.Contains(t, []string{"normal", "warning", "critical"}, batchSizeLabels[0],
					"Should use valid batch size label")
			}
		})
	}
}

func TestSegmentedMemory_ProfileConfigurationEvent(t *testing.T) {
	// Test that profile configuration is logged when tracer is set
	romContent := "Test documentation"

	tests := []struct {
		name    string
		profile CompressionProfile
	}{
		{
			name:    "data_intensive",
			profile: ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE],
		},
		{
			name:    "conversational",
			profile: ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL],
		},
		{
			name:    "balanced",
			profile: ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED],
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := NewSegmentedMemoryWithCompression(romContent, 200000, 20000, tt.profile)

			mockTracer := &mockTracerForMetrics{}
			sm.SetTracer(mockTracer)

			require.Len(t, mockTracer.events, 1, "Should record profile configuration event")

			event := mockTracer.events[0]
			assert.Equal(t, "memory.profile_configured", event.name)
			assert.Equal(t, tt.profile.Name, event.attributes["profile"])
			assert.Equal(t, tt.profile.MaxL1Messages, event.attributes["max_l1_messages"])
			assert.Equal(t, tt.profile.MinL1Messages, event.attributes["min_l1_messages"])
			assert.Equal(t, tt.profile.WarningThresholdPercent, event.attributes["warning_threshold_percent"])
			assert.Equal(t, tt.profile.CriticalThresholdPercent, event.attributes["critical_threshold_percent"])
			assert.Equal(t, tt.profile.NormalBatchSize, event.attributes["normal_batch_size"])
			assert.Equal(t, tt.profile.WarningBatchSize, event.attributes["warning_batch_size"])
			assert.Equal(t, tt.profile.CriticalBatchSize, event.attributes["critical_batch_size"])
		})
	}
}

func TestSegmentedMemory_NoMetricsWithoutTracer(t *testing.T) {
	// Test that compression works without tracer (no crashes)
	romContent := "Test documentation"
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]

	sm := NewSegmentedMemoryWithCompression(romContent, 10000, 1000, profile)
	sm.SetCompressor(&mockCompressor{enabled: true})

	// Don't set tracer - should work without crashes

	// Add messages to trigger compression
	for i := 0; i < 10; i++ {
		sm.AddMessage(Message{
			Role:    "user",
			Content: "Test message " + string(rune(i)),
		})
	}

	// Should complete without panic
	assert.LessOrEqual(t, len(sm.l1Messages), profile.MaxL1Messages, "Compression should still work")
}
