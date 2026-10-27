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

// Package eventlog defines the EventLogStore interface and its supporting
// types for the durable, tool-call-granularity execution log (issue #229).
// It is a leaf package with no internal loom imports beyond the generated
// proto, mirroring pkg/task's TaskStore and pkg/checkpoint's CheckpointStore
// precedents, so storage backends can implement it without pulling in
// heavier packages (in particular pkg/agent, which depends on a large part
// of the codebase and, via test fixtures in pkg/agent's own in-package test
// files that import pkg/storage/sqlite, would otherwise close an import
// cycle at test-build time).
package eventlog

import (
	"context"
	"time"
)

// EventType enumerates the four write-ahead event kinds execution_events
// records. See docs/superpowers/specs/2026-09-06-durable-execution-replay-design.md
// for the full design.
type EventType string

const (
	EventLLMCallIntent  EventType = "llm_call_intent"
	EventLLMCallResult  EventType = "llm_call_result"
	EventToolCallIntent EventType = "tool_call_intent"
	EventToolCallResult EventType = "tool_call_result"
)

// DefaultResumeClaimTTL bounds how long a session-resume claim is honored
// before another resume attempt may reclaim it. Long enough to cover a slow
// tool call plus retry backoff; short enough that a dead resumer's stale
// claim does not block recovery for long. Not configurable in phase 1.
const DefaultResumeClaimTTL = 5 * time.Minute

// ExecutionEvent is one row of the execution_events log.
type ExecutionEvent struct {
	SessionID     string
	StepIndex     int
	BatchPosition int
	EventType     EventType
	// ToolUseID is auxiliary/debugging only — never a correlation key. See
	// the design doc's "Correlation key" section: ToolCall.ID is not
	// reliably unique or present across every LLM provider.
	ToolUseID string
	Payload   []byte // JSON
	CreatedAt time.Time
}

// EventLogStore is the optional-capability interface for the durable,
// tool-call-granularity execution log (issue #229). A StorageBackend
// implements this once its execution_events migration has run; callers
// type-assert backend.StorageBackend.(backend.EventLogProvider) to reach it,
// exactly like TaskStoreProvider/GraphMemoryProvider.
type EventLogStore interface {
	// RecordIntent durably writes an *_intent event before the corresponding
	// LLM/tool call executes. The caller (agent.go) must treat a failure here
	// as a precondition failure: abort the call before it runs, since nothing
	// has happened yet. Idempotent: a retried call with the same
	// (sessionID, stepIndex, batchPosition, eventType) is a no-op, not an
	// error, so a caller may safely retry a few times on transient failure.
	RecordIntent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType EventType, toolUseID string, payload []byte) error

	// RecordResult durably writes the matching *_result event after the call
	// completes. Best-effort by design — the action has already happened by
	// the time this is called, so the caller logs-and-continues on failure
	// exactly like every other post-action persist in this package
	// (PersistToolExecution, appendMessage). A failed result write degrades
	// into the same (intent, no result) gap state a crash mid-call produces;
	// see ResumeSession.
	RecordResult(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType EventType, toolUseID string, payload []byte) error

	// LoadEvents returns every recorded event for a session, ordered by
	// step_index ascending, then batch_position ascending, then event_type
	// (an "*_intent" sorts before its "*_result" lexically, which is also
	// insertion order).
	LoadEvents(ctx context.Context, sessionID string) ([]ExecutionEvent, error)

	// NextStepIndex returns MAX(step_index)+1 for the session (0 if no events
	// are recorded yet) — the step counter's durable continuation point,
	// read once at loop entry (chat/ResumeChat/ResumeSession) and held in
	// memory for that one invocation.
	NextStepIndex(ctx context.Context, sessionID string) (int, error)

	// ClaimSessionResume attempts to take exclusive ownership of resuming
	// sessionID for leaseTTL, via a conditional UPDATE whose rows-affected
	// count is itself the CAS signal (see this plan's "Decisions made during
	// planning" note on why this differs from claimParkedRequest's read-back
	// pattern). Returns true if this call won the claim.
	ClaimSessionResume(ctx context.Context, sessionID, claimToken string, leaseTTL time.Duration) (bool, error)

	// ReleaseSessionResume clears the claim if claimToken still owns it. A
	// mismatched or already-cleared token is a no-op, not an error — the same
	// "judge by the row, not the write" caution claimParkedRequest documents.
	ReleaseSessionResume(ctx context.Context, sessionID, claimToken string) error

	// PruneResolvedStep deletes every execution_events row for one
	// (sessionID, stepIndex) pair. Called once every call in that step's
	// batch is confirmed complete (event log agrees with messages/
	// tool_executions) and the loop has advanced past it — see "Retention"
	// in the design doc.
	PruneResolvedStep(ctx context.Context, sessionID string, stepIndex int) error
}
