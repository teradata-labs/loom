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
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/eventlog"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// ErrEventLogDisabled is returned by write-ahead-intent recording (and by
// ResumeSession) when no EventLogStore was configured via WithEventLog.
var ErrEventLogDisabled = errors.New("execution event log is not configured on this agent")

// SetEventLogForResume wires an EventLogStore onto an already-constructed
// Agent — used by CLI/server paths (looms agent resume) that build agents
// via Registry.LoadAgents (which does not yet accept WithEventLog per-agent
// config) and need to attach the event log store afterward, before calling
// ResumeSession. Prefer WithEventLog at construction time everywhere else.
func (a *Agent) SetEventLogForResume(store eventlog.EventLogStore) {
	a.eventLog = store
}

// currentStepIndex resolves the step_index a loop invocation should use for
// its FIRST llm_call_intent event, by reading the durable continuation point
// (see EventLogStore.NextStepIndex doc comment). With no event log configured
// this always returns 0 — the value is never persisted anywhere in that case,
// so its exact number is irrelevant.
func (a *Agent) currentStepIndex(ctx context.Context, sessionID string) int {
	if a.eventLog == nil {
		return 0
	}
	next, err := a.eventLog.NextStepIndex(ctx, sessionID)
	if err != nil {
		zap.L().Warn("failed to read next step index; starting from 0 for this invocation",
			zap.String("session_id", sessionID), zap.Error(err))
		return 0
	}
	return next
}

// lastRecordedStepIndex returns the step_index of the most recently recorded
// LLM call for sessionID — the step a parked batch's tool calls belong to
// when completing it later (see completeParkedBatch in park.go). No further
// LLM call can occur between a batch parking and its completion (parking
// ends the turn immediately via TurnParkedError), so the highest recorded
// step_index at completion time IS the step that produced the parked batch
// — the same "unresolved tail is unambiguous" reasoning the design doc's
// Resume Flow section uses for crash-resume. Returns 0 if no event log is
// configured or no events have been recorded yet (both cases make the exact
// value inert, since recording is a no-op throughout when a.eventLog is nil,
// and there is nothing to correlate against if nothing was ever recorded).
func (a *Agent) lastRecordedStepIndex(ctx context.Context, sessionID string) int {
	next := a.currentStepIndex(ctx, sessionID)
	if next <= 0 {
		return 0
	}
	return next - 1
}

// recordLLMIntent is a blocking precondition: per the design's write-ahead
// durability semantics, an LLM call must not be made until its intent is
// durably recorded. A no-op (nil error) when no event log is configured.
// Retries a bounded number of times on failure before giving up, since a
// transient DB hiccup here should not permanently block the turn if it
// clears within a second or two.
func (a *Agent) recordLLMIntent(ctx context.Context, sessionID string, stepIndex int, messageCount int, model string) error {
	if a.eventLog == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]interface{}{"model": model, "message_count": messageCount})
	return a.recordIntentWithRetry(ctx, sessionID, stepIndex, 0, eventlog.EventLLMCallIntent, "", payload)
}

// recordLLMResult is best-effort: the call has already happened. Logs and
// continues on failure — the same convention as appendMessage/
// PersistToolExecution — leaving an (intent, no result) gap for
// ResumeSession to detect if the process crashes before this lands.
func (a *Agent) recordLLMResult(ctx context.Context, sessionID string, stepIndex int, resp *LLMResponse) {
	if a.eventLog == nil || resp == nil {
		return
	}
	// The WHOLE LLMResponse is recorded — content, tool calls, usage and each
	// call's opaque ThoughtSignature — deliberately, for a future replay
	// consumer. Nothing reads an llm_call_result event back today: resume
	// correlates and backfills at TOOL-call granularity only, so a crash
	// before the assistant message persisted always costs a fresh (repaid)
	// LLM call. This is forward-compatible recording, not a working replay
	// path — don't assume it is wired up.
	payload, err := json.Marshal(resp)
	if err != nil {
		zap.L().Warn("failed to marshal LLMResponse for event log", zap.String("session_id", sessionID), zap.Error(err))
		return
	}
	if err := a.eventLog.RecordResult(ctx, sessionID, stepIndex, 0, eventlog.EventLLMCallResult, "", payload); err != nil {
		zap.L().Warn("failed to record llm_call_result event", zap.String("session_id", sessionID), zap.Error(err))
	}
}

// recordToolIntent: see recordLLMIntent — same blocking-precondition contract,
// batchPosition is the tool call's index within its batch (the same value
// dispatchOneCall already receives as its "i" parameter).
func (a *Agent) recordToolIntent(ctx context.Context, sessionID string, stepIndex, batchPosition int, toolCall ToolCall) error {
	if a.eventLog == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]interface{}{"tool_name": toolCall.Name, "input": toolCall.Input})
	return a.recordIntentWithRetry(ctx, sessionID, stepIndex, batchPosition, eventlog.EventToolCallIntent, toolCall.ID, payload)
}

// recordToolResult: see recordLLMResult — best-effort.
func (a *Agent) recordToolResult(ctx context.Context, sessionID string, stepIndex, batchPosition int, toolUseID string, result *shuttle.Result, execErr error) {
	if a.eventLog == nil {
		return
	}
	errMsg := ""
	if execErr != nil {
		errMsg = execErr.Error()
	}
	payload, err := json.Marshal(map[string]interface{}{"result": result, "error": errMsg})
	if err != nil {
		zap.L().Warn("failed to marshal tool result for event log", zap.String("session_id", sessionID), zap.Error(err))
		return
	}
	if err := a.eventLog.RecordResult(ctx, sessionID, stepIndex, batchPosition, eventlog.EventToolCallResult, toolUseID, payload); err != nil {
		zap.L().Warn("failed to record tool_call_result event", zap.String("session_id", sessionID), zap.Error(err))
	}
}

const recordIntentMaxAttempts = 3

func (a *Agent) recordIntentWithRetry(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	var lastErr error
	for attempt := 0; attempt < recordIntentMaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 100 * time.Millisecond):
			}
		}
		lastErr = a.eventLog.RecordIntent(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload)
		if lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("recording %s event after %d attempts: %w", eventType, recordIntentMaxAttempts, lastErr)
}

// pruneStepIfResolved deletes a step's execution_events once every call in
// its batch is confirmed complete (a tool_call_intent/result pair, or no
// tool calls at all — a text-only step) — that step carries no further
// replay value. Best-effort: a failed prune leaves harmless extra rows that
// a later prune attempt (next step, or the next resume) will clean up.
//
// "If resolved" is VERIFIED here, not assumed from the caller. Every caller
// invokes this because it believes the step is fully resolved, but each of
// recordToolResult, PersistToolExecution and appendMessage is fail-open by
// design: any of them can silently fail and leave the step's real state
// short of what the caller believes. The event log is the authoritative
// record ResumeSession's backfill branch reads when messages/tool_executions
// disagree with it — so deleting it on a belief rather than on evidence
// removes exactly the safety net that would have let a resume recover from
// such a failure, at the instant it is needed.
func (a *Agent) pruneStepIfResolved(ctx context.Context, sessionID string, stepIndex int) {
	if a.eventLog == nil {
		return
	}
	events, err := a.eventLog.LoadEvents(ctx, sessionID)
	if err != nil {
		zap.L().Warn("failed to load events before pruning; skipping prune",
			zap.String("session_id", sessionID), zap.Int("step_index", stepIndex), zap.Error(err))
		return
	}
	// Pairing is per event-type FAMILY, not per batch_position alone: an LLM
	// call records at batch_position 0 (recordLLMIntent/recordLLMResult pass a
	// literal 0) and so does a batch's FIRST tool call, so one shared
	// position→bool map would let the step's llm_call_result vouch for tool
	// call 0's missing tool_call_result — disabling this check at exactly the
	// most common position. findEventPair (resume_session.go) keys on the type
	// family for the same reason.
	llmResultSeen := make(map[int]bool)
	toolResultSeen := make(map[int]bool)
	for _, e := range events {
		if e.StepIndex != stepIndex {
			continue
		}
		switch e.EventType {
		case eventlog.EventLLMCallResult:
			llmResultSeen[e.BatchPosition] = true
		case eventlog.EventToolCallResult:
			toolResultSeen[e.BatchPosition] = true
		}
	}
	for _, e := range events {
		if e.StepIndex != stepIndex {
			continue
		}
		resolved := true
		switch e.EventType {
		case eventlog.EventLLMCallIntent:
			resolved = llmResultSeen[e.BatchPosition]
		case eventlog.EventToolCallIntent:
			resolved = toolResultSeen[e.BatchPosition]
		}
		if !resolved {
			// An intent at this step has no matching result yet — not safe to
			// prune. This should be rare (the caller only invokes this when it
			// believes the step is fully resolved) but a silently-failed
			// result/persist write must not also silently disable the one
			// safety net that would let resume recover from it.
			return
		}
	}
	if err := a.eventLog.PruneResolvedStep(ctx, sessionID, stepIndex); err != nil {
		zap.L().Warn("failed to prune resolved execution-event step", zap.String("session_id", sessionID), zap.Int("step_index", stepIndex), zap.Error(err))
	}
}
