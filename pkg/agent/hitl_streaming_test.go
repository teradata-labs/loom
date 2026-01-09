// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// NOTE: This file focuses on unit testing the HITL helper functions.

// TestExtractHITLInfo tests the extractHITLInfo helper function.
func TestExtractHITLInfo(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		expected *HITLRequestInfo
	}{
		{
			name: "complete input",
			input: map[string]interface{}{
				"question":        "Should I proceed?",
				"request_type":    "approval",
				"priority":        "high",
				"timeout_seconds": 300.0,
				"context": map[string]interface{}{
					"table": "users",
				},
			},
			expected: &HITLRequestInfo{
				Question:    "Should I proceed?",
				RequestType: "approval",
				Priority:    "high",
				Timeout:     5 * time.Minute,
				Context: map[string]interface{}{
					"table": "users",
				},
			},
		},
		{
			name: "minimal input with defaults",
			input: map[string]interface{}{
				"question": "What should I do?",
			},
			expected: &HITLRequestInfo{
				Question:    "What should I do?",
				RequestType: "input",
				Priority:    "normal",
				Timeout:     5 * time.Minute,
				Context:     map[string]interface{}{},
			},
		},
		{
			name: "partial input",
			input: map[string]interface{}{
				"question":     "Review this code",
				"request_type": "review",
				"priority":     "low",
			},
			expected: &HITLRequestInfo{
				Question:    "Review this code",
				RequestType: "review",
				Priority:    "low",
				Timeout:     5 * time.Minute,
				Context:     map[string]interface{}{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractHITLInfo(tt.input)

			assert.Equal(t, tt.expected.Question, result.Question)
			assert.Equal(t, tt.expected.RequestType, result.RequestType)
			assert.Equal(t, tt.expected.Priority, result.Priority)
			assert.Equal(t, tt.expected.Timeout, result.Timeout)
			assert.Equal(t, tt.expected.Context, result.Context)
		})
	}
}
