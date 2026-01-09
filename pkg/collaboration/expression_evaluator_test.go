// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluateExpression(t *testing.T) {
	// Create test context
	avgConf := float32(0.45)
	totalVotes := int32(15)
	winningCount := int32(8)

	ctx := &EvaluationContext{
		ConsensusReached:    false,
		AverageConfidence:   &avgConf,
		TieDetected:         false,
		TotalVotes:          &totalVotes,
		WinningVoteCount:    &winningCount,
		EscalationRequested: true,
		CustomFields: map[string]interface{}{
			"custom_score": float64(0.75),
			"flag_enabled": true,
		},
	}

	tests := []struct {
		name    string
		expr    string
		want    bool
		wantErr bool
	}{
		// Simple comparisons
		{
			name: "average_confidence less than threshold",
			expr: "average_confidence < 0.5",
			want: true,
		},
		{
			name: "average_confidence greater than threshold",
			expr: "average_confidence > 0.5",
			want: false,
		},
		{
			name: "total_votes greater than value",
			expr: "total_votes > 10",
			want: true,
		},
		{
			name: "winning_vote_count equals value",
			expr: "winning_vote_count == 8",
			want: true,
		},
		{
			name: "consensus_reached is false",
			expr: "consensus_reached == false",
			want: true,
		},
		{
			name: "tie_detected is false",
			expr: "tie_detected == false",
			want: true,
		},
		{
			name: "escalation_requested is true",
			expr: "escalation_requested == true",
			want: true,
		},

		// Logical operators
		{
			name: "AND operator - both true",
			expr: "average_confidence < 0.5 && total_votes > 10",
			want: true,
		},
		{
			name: "AND operator - first false",
			expr: "average_confidence > 0.5 && total_votes > 10",
			want: false,
		},
		{
			name: "OR operator - first true",
			expr: "average_confidence < 0.5 || total_votes < 10",
			want: true,
		},
		{
			name: "OR operator - both false",
			expr: "average_confidence > 0.5 || total_votes < 10",
			want: false,
		},

		// Negation
		{
			name: "negation of false",
			expr: "!consensus_reached",
			want: true,
		},
		{
			name: "negation of true",
			expr: "!escalation_requested",
			want: false,
		},

		// Complex expressions
		{
			name: "complex: low confidence and high votes",
			expr: "average_confidence < 0.5 && total_votes > 10 && !consensus_reached",
			want: true,
		},
		{
			name: "complex: escalation or tie",
			expr: "escalation_requested == true || tie_detected == true",
			want: true,
		},

		// Custom fields
		{
			name: "custom field comparison",
			expr: "custom_score > 0.7",
			want: true,
		},
		{
			name: "custom boolean field",
			expr: "flag_enabled == true",
			want: true,
		},

		// Boolean variable alone
		{
			name: "boolean variable (true)",
			expr: "escalation_requested",
			want: true,
		},
		{
			name: "boolean variable (false)",
			expr: "consensus_reached",
			want: false,
		},

		// Comparison operators
		{
			name: "less than or equal",
			expr: "average_confidence <= 0.45",
			want: true,
		},
		{
			name: "greater than or equal",
			expr: "total_votes >= 15",
			want: true,
		},
		{
			name: "not equal",
			expr: "winning_vote_count != 10",
			want: true,
		},

		// Error cases
		{
			name:    "unknown variable",
			expr:    "unknown_var > 5",
			wantErr: true,
		},
		{
			name:    "empty expression",
			expr:    "",
			wantErr: true,
		},
		{
			name:    "invalid operator",
			expr:    "average_confidence <> 0.5",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evaluateExpression(tt.expr, ctx)
			if tt.wantErr {
				assert.Error(t, err, "expected error for expression: %s", tt.expr)
			} else {
				require.NoError(t, err, "unexpected error for expression: %s", tt.expr)
				assert.Equal(t, tt.want, got, "expression: %s", tt.expr)
			}
		})
	}
}

func TestEvaluateExpression_NilContext(t *testing.T) {
	result, err := evaluateExpression("average_confidence < 0.5", nil)
	assert.Error(t, err)
	assert.False(t, result)
	assert.Contains(t, err.Error(), "context is nil")
}

func TestEvaluateExpression_MissingFields(t *testing.T) {
	ctx := &EvaluationContext{
		// No fields set
	}

	// Try to access a pointer field that's not set
	result, err := evaluateExpression("average_confidence < 0.5", ctx)
	assert.Error(t, err)
	assert.False(t, result)
	assert.Contains(t, err.Error(), "not set")
}

func TestCompare_TypeMismatch(t *testing.T) {
	// Boolean vs number comparison should fail
	result, err := compare(true, float64(5), "==")
	assert.Error(t, err)
	assert.False(t, result)
	assert.Contains(t, err.Error(), "type mismatch")
}

func TestCompare_BooleanOperators(t *testing.T) {
	tests := []struct {
		name    string
		left    bool
		right   bool
		op      string
		want    bool
		wantErr bool
	}{
		{"bool equal true", true, true, "==", true, false},
		{"bool equal false", true, false, "==", false, false},
		{"bool not equal", true, false, "!=", true, false},
		{"bool invalid op", true, false, "<", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := compare(tt.left, tt.right, tt.op)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  float64
		ok    bool
	}{
		{"float64", float64(3.14), 3.14, true},
		{"float32", float32(2.5), 2.5, true},
		{"int", int(42), 42.0, true},
		{"int32", int32(100), 100.0, true},
		{"int64", int64(200), 200.0, true},
		{"string", "not a number", 0, false},
		{"bool", true, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toFloat64(tt.input)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestResolveValue(t *testing.T) {
	avgConf := float32(0.7)
	ctx := &EvaluationContext{
		ConsensusReached:  true,
		AverageConfidence: &avgConf,
		CustomFields: map[string]interface{}{
			"my_var": float64(42.5),
		},
	}

	tests := []struct {
		name    string
		token   string
		want    interface{}
		wantErr bool
	}{
		{"boolean literal true", "true", true, false},
		{"boolean literal false", "false", false, false},
		{"numeric literal", "3.14", 3.14, false},
		{"integer literal", "42", float64(42), false},
		{"context variable", "consensus_reached", true, false},
		{"context pointer", "average_confidence", float64(0.7), false}, // Note: float32 conversion may have precision loss
		{"custom field", "my_var", float64(42.5), false},
		{"unknown variable", "unknown", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveValue(tt.token, ctx)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				// Use InDelta for float comparisons due to float32->float64 conversion precision loss
				if expectedFloat, ok := tt.want.(float64); ok {
					if gotFloat, ok := got.(float64); ok {
						assert.InDelta(t, expectedFloat, gotFloat, 0.0001, "Float values should be approximately equal")
						return
					}
				}
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
