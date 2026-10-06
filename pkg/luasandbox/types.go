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

package luasandbox

import (
	"context"
	"errors"
)

// Program is one script to run.
type Program struct {
	// Name is the chunk name shown in error messages ("inline" or a saved
	// script's name). Empty means "inline".
	Name string
	// Source is the Lua 5.4 source text.
	Source string
	// Args becomes the Lua global table `args`. Values must be
	// JSON-compatible; anything else is converted through encoding/json.
	Args map[string]any
	// Info is merged into the Lua global table `script`, whose `name` field
	// is always Name.
	Info map[string]any
}

// ToolInfo describes one tool a script may call.
type ToolInfo struct {
	Name        string
	Description string
}

// CallError describes a failed nested tool call.
type CallError struct {
	Code       string
	Message    string
	Suggestion string
	Retryable  bool
}

// CallResult is a host's answer to one nested tool call.
//
// OK=false with Error set is an ordinary tool failure: the script sees it and
// decides what to do. A Go error returned by the Host instead means the host
// itself is broken, and it ends the run with OutcomeHostError.
type CallResult struct {
	OK bool
	// Data is the tool's output: a string, or a JSON-compatible value. A
	// string holding a JSON object or array is decoded for the script.
	Data  any
	Error *CallError
	// Decision is the admission outcome the host applied, recorded in the
	// run's call ledger ("allow", "deny", "approval_required", ...).
	Decision string
}

// Progress event kinds.
const (
	ProgressToolStarted   = "tool_started"
	ProgressToolCompleted = "tool_completed"
	ProgressSleep         = "sleep"
)

// ProgressEvent reports script activity to the host, for example to keep a
// streaming connection alive or to show what the script is doing.
type ProgressEvent struct {
	Kind   string
	Tool   string
	OK     bool
	Code   string
	Millis int64
	// Seq is the 1-based index of the nested call within the run.
	Seq int
	// Max is the run's tool-call budget.
	Max int
}

// ErrToolNotVisible is returned by Host.ToolSchema for a tool the script may
// not call.
var ErrToolNotVisible = errors.New("tool not visible to this script")

// Host connects a running script to the outside world.
//
// Every method is called on the goroutine running Run and must honour ctx.
// Progress must never block.
type Host interface {
	// ListTools returns the tools the script may call.
	ListTools(ctx context.Context) ([]ToolInfo, error)
	// ToolSchema returns one tool's JSON Schema, or ErrToolNotVisible.
	ToolSchema(ctx context.Context, name string) (map[string]any, error)
	// CallTool executes one tool call.
	CallTool(ctx context.Context, name string, args map[string]any) (*CallResult, error)
	// TurnResult returns a tool result produced earlier in the current turn.
	TurnResult(ctx context.Context, messageID int64) (*CallResult, error)
	// Progress reports activity. It must return immediately.
	Progress(ev ProgressEvent)
}

// Outcome classifies how a run ended.
type Outcome string

// Outcomes.
const (
	// OutcomeOK means the script ran to completion.
	OutcomeOK Outcome = "ok"
	// OutcomeScriptError means the script failed to compile or raised an
	// error it did not catch.
	OutcomeScriptError Outcome = "script_error"
	// OutcomeBudgetExceeded means a limit fired; RunResult.Limit names it.
	OutcomeBudgetExceeded Outcome = "budget_exceeded"
	// OutcomeCancelled means the caller's context was cancelled or expired.
	OutcomeCancelled Outcome = "cancelled"
	// OutcomeHostError means the host failed or this package hit an internal
	// error. RunResult.Detail carries diagnostics for logs.
	OutcomeHostError Outcome = "host_error"
)

// Budget names reported in RunResult.Limit.
const (
	LimitCPU       = "cpu"
	LimitMemory    = "memory"
	LimitWall      = "wall"
	LimitToolCalls = "tool_calls"
)

// Error codes shared by the engine and its hosts for nested call failures.
const (
	CodeToolNotVisible   = "TOOL_NOT_VISIBLE"
	CodeHardDenied       = "HARD_DENIED"
	CodeApprovalRequired = "approval_required"
	CodePermissionDenied = "permission_denied"
	CodeResourcePending  = "RESOURCE_PENDING"
	CodeExecutionFailed  = "execution_failed"
	CodeToolCallTimeout  = "TOOL_CALL_TIMEOUT"
)

// CallRecord is one entry of a run's call ledger.
type CallRecord struct {
	Tool     string
	OK       bool
	Code     string
	Millis   int64
	Decision string
}

// Usage reports resources a run consumed.
type Usage struct {
	CPUTicks    uint64
	MemoryBytes uint64
	WallMillis  uint64
}

// Truncation reports which parts of a run's output were cut to fit limits.
type Truncation struct {
	Output      bool
	Value       bool
	CallResults bool
}

// RunResult is the outcome of one run.
type RunResult struct {
	Outcome Outcome
	// Value is the script's first return value converted to JSON-compatible
	// Go values (nil, bool, int64, float64, string, []any, map[string]any).
	Value any
	// Output is captured print/log output.
	Output string
	// Error is a message suitable for the model: "<chunk>:<line>: message"
	// for script errors, the limit for budget errors.
	Error string
	// Limit names the budget that fired when Outcome is
	// OutcomeBudgetExceeded.
	Limit string
	// Detail carries diagnostics for server logs only (panic values, stack
	// traces). Never show it to a model or end user.
	Detail    string
	Used      Usage
	Calls     []CallRecord
	Truncated Truncation
}
