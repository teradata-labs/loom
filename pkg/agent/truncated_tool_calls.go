// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
)

const (
	// outputTruncatedCode is the tool-result error code for a call whose
	// arguments the model never finished writing because the response hit the
	// output token limit.
	outputTruncatedCode = "OUTPUT_TRUNCATED"

	parkTruncatedPositionsContextKey = "truncated_positions"
	parkOutputTokensContextKey       = "output_tokens"
)

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

// answerTruncatedToolCall writes an OUTPUT_TRUNCATED tool row for the call at
// batchPos instead of running it: executing a call on arguments the model never
// finished writing either fails with a validation error that does not say why
// (so the model repeats the same oversized call until the output-token circuit
// breaker ends the turn) or, worse, runs on partial input.
//
// The row tells the model its output hit the limit and to send the content in
// smaller pieces. It is written at the call's own batch position inside
// dispatchBatch, completeParkedBatch, and ResumeSession so tool rows always
// land in call order (required by order-paired providers such as Gemini and
// Ollama).
//
// Each answered call counts as one tool execution at its batch position, like
// a per-turn-cap skip, so the whole-turn MaxToolExecutions budget still bounds
// a run of truncated turns without a later cut-off call preempting an earlier
// complete call in the same batch.
func (a *Agent) answerTruncatedToolCall(ctx Context, session *Session, calls []ToolCall, batchPos, eventPos int, st *batchState) {
	if sessionLeaseLost(ctx) != nil {
		return
	}
	tc := calls[batchPos]
	var tools []shuttle.Tool
	if st.tools != nil && len(*st.tools) > 0 {
		tools = *st.tools
	} else {
		tools = a.advertisedTools(session)
	}
	missing := missingRequiredArgs(tc, tools)
	message := a.outputTruncatedMessage(ctx, tc.Name, st.outputTokens, missing)
	result := &shuttle.Result{
		Success: false,
		Error: &shuttle.Error{
			Code:      outputTruncatedCode,
			Message:   message,
			Retryable: true,
			Details: map[string]interface{}{
				"tool_name":         tc.Name,
				"tool_call_ordinal": strconv.Itoa(toolCallOrdinal(calls, batchPos)),
			},
		},
	}

	st.span.AddEvent("tool_call.truncated_not_executed", map[string]interface{}{
		"tool_name":     tc.Name,
		"index":         batchPos + 1,
		"total":         len(calls),
		"output_tokens": st.outputTokens,
		"missing_args":  strings.Join(missing, ","),
	})

	// A synthesized result has no execution intent: no tool was attempted.
	// Retain the result if its message is lost, so crash resume can backfill
	// it even when the tool is no longer advertised.
	a.recordToolResult(ctx, session.ID, st.stepIndex, eventPos, tc.ID, result, nil)
	_, persisted := a.appendMessage(ctx, session, Message{
		Role:       "tool",
		Content:    message,
		ToolUseID:  tc.ID,
		ToolResult: result,
		AgentID:    a.GetID(),
		Timestamp:  time.Now(),
	}, false)
	if !persisted {
		st.rowNotPersisted = true
		st.parkableTail = false
	}
	execution := ToolExecution{
		ToolName: tc.Name,
		Input:    tc.Input,
		Result:   result,
	}
	*st.allToolExecutions = append(*st.allToolExecutions, execution)
	if err := a.memory.PersistToolExecution(ctx, session.ID, execution); err != nil {
		zap.L().Warn("failed to persist truncated tool call result",
			zap.String("session_id", session.ID), zap.String("tool", tc.Name), zap.Error(err))
	}
	*st.toolExecutionCount++
}

func recordParkedTruncatedCalls(reqCtx map[string]interface{}, answered map[int]bool, outputTokens int) {
	if len(answered) == 0 || reqCtx == nil {
		return
	}
	positions := make([]int, 0, len(answered))
	for pos, isTruncated := range answered {
		if isTruncated {
			positions = append(positions, pos)
		}
	}
	if len(positions) == 0 {
		return
	}
	sort.Ints(positions)
	reqCtx[parkTruncatedPositionsContextKey] = positions
	reqCtx[parkOutputTokensContextKey] = outputTokens
}

func parkedTruncatedCalls(hr *shuttle.HumanRequest) (map[int]bool, int) {
	if hr == nil || hr.Context == nil {
		return nil, 0
	}
	var truncated map[int]bool
	switch v := hr.Context[parkTruncatedPositionsContextKey].(type) {
	case []int:
		truncated = make(map[int]bool, len(v))
		for _, pos := range v {
			truncated[pos] = true
		}
	case []interface{}:
		truncated = make(map[int]bool, len(v))
		for _, raw := range v {
			switch pos := raw.(type) {
			case int:
				truncated[pos] = true
			case float64:
				if pos == float64(int(pos)) {
					truncated[int(pos)] = true
				}
			}
		}
	}
	var outputTokens int
	switch v := hr.Context[parkOutputTokensContextKey].(type) {
	case int:
		outputTokens = v
	case float64:
		outputTokens = int(v)
	}
	return truncated, outputTokens
}

// The ordinal within one tool name and ID survives query_tool_result
// filtering at persistence, unlike the response's raw batch index.
func toolCallOrdinal(calls []ToolCall, index int) int {
	ordinal := 0
	for _, call := range calls[:index] {
		if call.ID == calls[index].ID && call.Name == calls[index].Name {
			ordinal++
		}
	}
	return ordinal
}

func truncatedToolRowPosition(row Message, calls []ToolCall) (int, bool) {
	if row.ToolResult == nil || row.ToolResult.Error == nil || row.ToolResult.Error.Code != outputTruncatedCode {
		return 0, false
	}
	details := row.ToolResult.Error.Details
	name, _ := details["tool_name"].(string)
	ordinalText, _ := details["tool_call_ordinal"].(string)
	ordinal, err := strconv.Atoi(ordinalText)
	if err != nil || ordinal < 0 {
		return 0, false
	}
	for i, call := range calls {
		if call.ID != row.ToolUseID || call.Name != name {
			continue
		}
		if ordinal == 0 {
			return i, true
		}
		ordinal--
	}
	return 0, false
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
