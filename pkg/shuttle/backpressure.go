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
package shuttle

import "encoding/json"

// DetailsBackpressure is the well-known Error.Details key carrying a
// BackpressureHint. The value is a map[string]interface{} with "code",
// "retry_after_s", "wait_param", and "max_wait_s" fields — the same wire
// names the MCP-level contract uses, so a backend can copy a hint through
// unchanged.
const DetailsBackpressure = "loom.backpressure"

// BackpressureHint is the machine-readable park-and-wake contract a backend
// may attach to a tool error: the failure is capacity flow control, not a
// fault — the identical call, re-issued after a wait, is expected to succeed
// once load drains or a slot frees. A runtime that honors the hint parks the
// calling conversation and re-invokes instead of surfacing the error to the
// model. Task-level failures (SQL errors, timeouts, deadlocks) never carry
// the hint and must reach the model.
//
// This type is the contract only — no wait loop lives at the shuttle layer.
// The MCP adapter's freeze loop (PR #355) migrates onto this contract when
// that PR merges; until then it parses its own MCP-level hint.
type BackpressureHint struct {
	// Code names the capacity condition (e.g. "session_handle_budget_full").
	// Backend-defined; loom treats it as an opaque label.
	Code string
	// RetryAfterS is the backend's worst-case estimate, in seconds, of when
	// the retry will succeed (capacity may free sooner). 0 = no estimate.
	RetryAfterS int64
	// WaitParam names a tool argument that parks the retry backend-side for
	// up to MaxWaitS seconds, waking on freed capacity instead of polling.
	// Empty = the backend offers no server-side wait.
	WaitParam string
	// MaxWaitS caps one backend-side wait in seconds. 0 = backend default.
	MaxWaitS int64
}

// SetBackpressure attaches the hint to the error under DetailsBackpressure,
// initializing Details as needed, and marks the error Retryable — capacity
// flow control is by definition retryable. The hint is stored in its
// JSON-shaped form so it parses identically after a marshal/unmarshal
// round trip. A nil error is a no-op.
func (e *Error) SetBackpressure(h BackpressureHint) {
	if e == nil {
		return
	}
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[DetailsBackpressure] = map[string]interface{}{
		"code":          h.Code,
		"retry_after_s": h.RetryAfterS,
		"wait_param":    h.WaitParam,
		"max_wait_s":    h.MaxWaitS,
	}
	e.Retryable = true
}

// Backpressure extracts the hint, or nil when the error declares none. The
// key's presence is the declaration; parsing is tolerant by contract — tool
// results are data, so a malformed value (wrong type) yields nil, never an
// error, and wrong-typed fields inside a well-formed map read as zero.
func (e *Error) Backpressure() *BackpressureHint {
	if e == nil || e.Details == nil {
		return nil
	}
	switch v := e.Details[DetailsBackpressure].(type) {
	case BackpressureHint:
		h := v
		return &h
	case map[string]interface{}:
		code, _ := v["code"].(string)
		waitParam, _ := v["wait_param"].(string)
		return &BackpressureHint{
			Code:        code,
			RetryAfterS: detailsInt64(v["retry_after_s"]),
			WaitParam:   waitParam,
			MaxWaitS:    detailsInt64(v["max_wait_s"]),
		}
	default:
		return nil
	}
}

// detailsInt64 reads an integer that may arrive as any of the numeric types
// a Go emitter or a JSON round trip produces. Anything else reads as 0.
func detailsInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	return 0
}
