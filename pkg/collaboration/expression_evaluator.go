// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"fmt"
	"strconv"
	"strings"
)

// evaluateExpression evaluates a simple boolean expression against an EvaluationContext.
// Supports:
//   - Comparison operators: <, >, <=, >=, ==, !=
//   - Logical operators: &&, ||
//   - Variable lookup from context (e.g., average_confidence, consensus_reached, tie_detected)
//   - Numeric and boolean literals
//
// Examples:
//   - "average_confidence < 0.5"
//   - "total_votes > 10 && !consensus_reached"
//   - "tie_detected == true"
func evaluateExpression(expr string, ctx *EvaluationContext) (bool, error) {
	if ctx == nil {
		return false, fmt.Errorf("evaluation context is nil")
	}

	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false, fmt.Errorf("empty expression")
	}

	// Handle logical OR (||) - lowest precedence
	if strings.Contains(expr, "||") {
		parts := strings.Split(expr, "||")
		for _, part := range parts {
			result, err := evaluateExpression(strings.TrimSpace(part), ctx)
			if err != nil {
				return false, err
			}
			if result {
				return true, nil // Short-circuit on first true
			}
		}
		return false, nil
	}

	// Handle logical AND (&&) - higher precedence
	if strings.Contains(expr, "&&") {
		parts := strings.Split(expr, "&&")
		for _, part := range parts {
			result, err := evaluateExpression(strings.TrimSpace(part), ctx)
			if err != nil {
				return false, err
			}
			if !result {
				return false, nil // Short-circuit on first false
			}
		}
		return true, nil
	}

	// Handle negation (!)
	if strings.HasPrefix(expr, "!") {
		inner := strings.TrimSpace(expr[1:])
		result, err := evaluateExpression(inner, ctx)
		if err != nil {
			return false, err
		}
		return !result, nil
	}

	// Handle comparison operators
	for _, op := range []string{"<=", ">=", "==", "!=", "<", ">"} {
		if strings.Contains(expr, op) {
			parts := strings.SplitN(expr, op, 2)
			if len(parts) != 2 {
				continue
			}

			left := strings.TrimSpace(parts[0])
			right := strings.TrimSpace(parts[1])

			leftVal, err := resolveValue(left, ctx)
			if err != nil {
				return false, fmt.Errorf("failed to resolve left side '%s': %w", left, err)
			}

			rightVal, err := resolveValue(right, ctx)
			if err != nil {
				return false, fmt.Errorf("failed to resolve right side '%s': %w", right, err)
			}

			return compare(leftVal, rightVal, op)
		}
	}

	// If no operators found, evaluate as a boolean variable
	val, err := resolveValue(expr, ctx)
	if err != nil {
		return false, err
	}

	// Try to convert to bool
	if boolVal, ok := val.(bool); ok {
		return boolVal, nil
	}

	return false, fmt.Errorf("expression '%s' did not evaluate to a boolean", expr)
}

// resolveValue resolves a value from the expression, which can be:
// - A literal (true, false, number)
// - A variable from the EvaluationContext
func resolveValue(token string, ctx *EvaluationContext) (interface{}, error) {
	token = strings.TrimSpace(token)

	// Try boolean literals
	if token == "true" {
		return true, nil
	}
	if token == "false" {
		return false, nil
	}

	// Try numeric literals
	if num, err := strconv.ParseFloat(token, 64); err == nil {
		return num, nil
	}

	// Try to resolve from context
	switch token {
	case "consensus_reached":
		return ctx.ConsensusReached, nil
	case "average_confidence":
		if ctx.AverageConfidence == nil {
			return nil, fmt.Errorf("average_confidence is not set in context")
		}
		return float64(*ctx.AverageConfidence), nil
	case "tie_detected":
		return ctx.TieDetected, nil
	case "escalation_requested":
		return ctx.EscalationRequested, nil
	case "total_votes":
		if ctx.TotalVotes == nil {
			return nil, fmt.Errorf("total_votes is not set in context")
		}
		return float64(*ctx.TotalVotes), nil
	case "winning_vote_count":
		if ctx.WinningVoteCount == nil {
			return nil, fmt.Errorf("winning_vote_count is not set in context")
		}
		return float64(*ctx.WinningVoteCount), nil
	default:
		// Try custom fields
		if ctx.CustomFields != nil {
			if val, exists := ctx.CustomFields[token]; exists {
				return val, nil
			}
		}
		return nil, fmt.Errorf("unknown variable: %s", token)
	}
}

// compare compares two values using the specified operator
func compare(left, right interface{}, op string) (bool, error) {
	// Handle boolean comparisons
	if leftBool, leftOk := left.(bool); leftOk {
		rightBool, rightOk := right.(bool)
		if !rightOk {
			return false, fmt.Errorf("type mismatch: cannot compare bool with %T", right)
		}
		switch op {
		case "==":
			return leftBool == rightBool, nil
		case "!=":
			return leftBool != rightBool, nil
		default:
			return false, fmt.Errorf("operator %s not supported for boolean comparison", op)
		}
	}

	// Handle numeric comparisons
	leftNum, leftOk := toFloat64(left)
	rightNum, rightOk := toFloat64(right)

	if !leftOk || !rightOk {
		return false, fmt.Errorf("cannot compare non-numeric values: %T %s %T", left, op, right)
	}

	switch op {
	case "<":
		return leftNum < rightNum, nil
	case ">":
		return leftNum > rightNum, nil
	case "<=":
		return leftNum <= rightNum, nil
	case ">=":
		return leftNum >= rightNum, nil
	case "==":
		return leftNum == rightNum, nil
	case "!=":
		return leftNum != rightNum, nil
	default:
		return false, fmt.Errorf("unknown operator: %s", op)
	}
}

// toFloat64 attempts to convert a value to float64
func toFloat64(val interface{}) (float64, bool) {
	switch v := val.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}
