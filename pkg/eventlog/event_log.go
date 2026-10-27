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
	"errors"
	"time"
)

// EventType enumerates the four write-ahead event kinds execution_events
// records: an intent before each LLM or tool call, and a result after it.
type EventType string

const (
	EventLLMCallIntent  EventType = "llm_call_intent"
	EventLLMCallResult  EventType = "llm_call_result"
	EventToolCallIntent EventType = "tool_call_intent"
	EventToolCallResult EventType = "tool_call_result"
)

// DefaultSessionLeaseTTL is how long a session lease lasts without renewal.
// A holder renews it every DefaultSessionLeaseTTL/3 while it runs, so the TTL
// only bounds how long a crashed holder blocks the next one.
const DefaultSessionLeaseTTL = 2 * time.Minute

// ErrEventConflict is returned by RecordIntent and RecordResult when an event
// already exists at the same (session, step, position, type) with a different
// payload. Two writers recorded different calls at the same step, so the log
// can no longer say which call that position holds. A retry of the same write
// (same payload) is not a conflict.
var ErrEventConflict = errors.New("execution event conflict: a different event is already recorded at this step and position")

// ExecutionEvent is one row of the execution_events log.
type ExecutionEvent struct {
	SessionID     string
	StepIndex     int
	BatchPosition int
	EventType     EventType
	// ToolUseID is for debugging only, never a correlation key: ToolCall.ID
	// is not reliably unique or present across LLM providers (Ollama can
	// omit it; Gemini reuses the function name for parallel calls).
	ToolUseID string
	Payload   []byte // JSON
	CreatedAt time.Time
}

// EventLogStore is the optional-capability interface for the durable,
// tool-call-granularity execution log. A StorageBackend implements it once
// its execution_events migration has run; callers type-assert
// backend.StorageBackend.(backend.EventLogProvider) to reach it, like
// TaskStoreProvider and GraphMemoryProvider.
//
// Every method acts as the caller identity in ctx (types.UserIDFromContext):
// a caller reads and writes only its own sessions' events and leases.
type EventLogStore interface {
	// RecordIntent durably writes an *_intent event before the corresponding
	// LLM or tool call executes. The caller must treat a failure as a
	// precondition failure and not run the call. Retrying the same write is a
	// no-op; a different payload at the same key returns ErrEventConflict.
	RecordIntent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType EventType, toolUseID string, payload []byte) error

	// RecordResult durably writes the matching *_result event after the call
	// completes. Best-effort for the caller: the action already happened, so
	// a failure is logged and leaves an (intent, no result) gap, the same
	// state a crash mid-call produces. Same retry and conflict rules as
	// RecordIntent.
	RecordResult(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType EventType, toolUseID string, payload []byte) error

	// LoadEvents returns every recorded event for a session, ordered by
	// step_index, then batch_position, then event_type ("*_intent" sorts
	// before its "*_result").
	LoadEvents(ctx context.Context, sessionID string) ([]ExecutionEvent, error)

	// NextStepIndex returns MAX(step_index)+1 for the session, or 0 if no
	// events are recorded.
	NextStepIndex(ctx context.Context, sessionID string) (int, error)

	// AcquireSessionLease makes holder the only process driving sessionID's
	// conversation loop for ttl. It succeeds when no lease exists, the
	// existing lease has expired, or holder already holds it. Expiry uses the
	// database clock, so replicas with skewed clocks agree.
	AcquireSessionLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error)

	// RenewSessionLease extends holder's lease to now+ttl. It returns false
	// when holder no longer holds the lease (another holder took it after it
	// expired); the caller must then stop dispatching calls.
	RenewSessionLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error)

	// ReleaseSessionLease removes holder's lease. A lease held by someone
	// else, or no lease, is a no-op.
	ReleaseSessionLease(ctx context.Context, sessionID, holder string) error

	// PruneResolvedStep deletes every event for one (sessionID, stepIndex),
	// once every call in that step is confirmed complete.
	PruneResolvedStep(ctx context.Context, sessionID string, stepIndex int) error

	// PruneStepsBefore deletes every event for sessionID with a step_index
	// below stepIndex. Called once a turn completes, so steps left behind by
	// a resolved execution gap do not accumulate.
	PruneStepsBefore(ctx context.Context, sessionID string, stepIndex int) error
}
