// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// outputTruncatedCode is the tool-result error code for a call whose
// arguments the model never finished writing because the response hit the
// output token limit.
const outputTruncatedCode = "OUTPUT_TRUNCATED"

// truncatedToolCallIndexes returns the batch indexes of the calls in a
// max_tokens response that are incomplete per the schema they were advertised
// with. Indexes, not IDs: providers can emit empty or duplicated call IDs, and
// an index always names exactly one call of the batch.
//
// Only a max_tokens stop qualifies. A call missing a required argument in an
// ordinary response is the model's mistake, and the tool's own validation
// error is the right answer for it.
func truncatedToolCallIndexes(llmResp *LLMResponse, tools []shuttle.Tool) map[int]bool {
	if llmResp == nil || llmResp.StopReason != "max_tokens" {
		return nil
	}
	var truncated map[int]bool
	for i, tc := range llmResp.ToolCalls {
		if classifyToolCall(tc, tools) != toolCallStateIncomplete {
			continue
		}
		if truncated == nil {
			truncated = make(map[int]bool)
		}
		truncated[i] = true
	}
	return truncated
}

// missingRequiredArgs lists the top-level properties the call's advertised
// schema requires but the call does not carry, in sorted order. Used only to
// tell the model what was cut off; completeness itself is decided by
// classifyToolCall.
func missingRequiredArgs(tc ToolCall, tools []shuttle.Tool) []string {
	tool := advertisedTool(tc.Name, tools)
	if tool == nil {
		return nil
	}
	schema := tool.InputSchema()
	if schema == nil {
		return nil
	}
	input := shuttle.NormalizeParametersToSchema(tool, tc.Input)
	var missing []string
	for _, name := range schema.Required {
		if v, ok := input[name]; !ok || v == nil {
			missing = append(missing, name)
		}
	}
	missing = append(missing, missingConditionalArgs(tool, input)...)
	sort.Strings(missing)
	return missing
}

// answerTruncatedToolCalls writes a tool row for every call in truncated
// instead of running it: executing a call on arguments the model never
// finished writing either fails with a validation error that does not say why
// (so the model repeats the same oversized call until the output-token circuit
// breaker ends the turn) or, worse, runs on partial input.
//
// The row tells the model its output hit the limit and to send the content in
// smaller pieces. It is written before the HITL park pre-scan and dispatch so
// that neither path, including a resumed parked batch, runs these calls: a
// call with a tool row is no longer rowless.
//
// Each answered call counts as one tool execution, like a per-turn-cap skip,
// so the whole-turn MaxToolExecutions budget still bounds a run of truncated
// turns alongside the circuit breaker.
func (a *Agent) answerTruncatedToolCalls(ctx Context, session *Session, llmResp *LLMResponse, truncated map[int]bool, tools []shuttle.Tool, span *observability.Span, toolExecutionCount *int, allToolExecutions *[]ToolExecution) {
	if len(truncated) == 0 {
		return
	}
	for i, tc := range llmResp.ToolCalls {
		if !truncated[i] {
			continue
		}
		missing := missingRequiredArgs(tc, tools)
		message := a.outputTruncatedMessage(ctx, tc.Name, llmResp.Usage.OutputTokens, missing)
		result := &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:      outputTruncatedCode,
				Message:   message,
				Retryable: true,
			},
		}

		span.AddEvent("tool_call.truncated_not_executed", map[string]interface{}{
			"tool_name":     tc.Name,
			"index":         i + 1,
			"total":         len(llmResp.ToolCalls),
			"output_tokens": llmResp.Usage.OutputTokens,
			"missing_args":  strings.Join(missing, ","),
		})

		a.appendMessage(ctx, session, Message{
			Role:       "tool",
			Content:    message,
			ToolUseID:  tc.ID,
			ToolResult: result,
			AgentID:    a.GetID(),
			Timestamp:  time.Now(),
		}, false)
		*allToolExecutions = append(*allToolExecutions, ToolExecution{
			ToolName: tc.Name,
			Input:    tc.Input,
			Result:   result,
		})
		*toolExecutionCount++
	}
}

// outputTruncatedMessage loads errors.tool_execution.output_truncated from the
// prompt registry, falling back to the same text when no registry is loaded.
func (a *Agent) outputTruncatedMessage(ctx Context, toolName string, outputTokens int, missing []string) string {
	// Named in the sentence either way: the registry interpolates {{.var}}
	// only and trims values, so an optional clause cannot be templated.
	missingArgs := "not determined"
	if len(missing) > 0 {
		missingArgs = strings.Join(missing, ", ")
	}
	fallback := fmt.Sprintf("%s: your response reached the output token limit (%d tokens) before the arguments of this %s call were complete (missing: %s), so the call was not run. Send the content in smaller pieces: split it across several calls (for example one call per item or section) so each call fits in one response.",
		outputTruncatedCode, outputTokens, toolName, missingArgs)

	return a.getErrorMessage(ctx, "tool_execution", "output_truncated", map[string]interface{}{
		"tool_name":     toolName,
		"output_tokens": outputTokens,
		"missing_args":  missingArgs,
		"error_message": fallback,
	})
}
