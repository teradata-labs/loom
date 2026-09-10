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

import "context"

// DecisionKind is the verdict a hook (or the combined chain) reaches for a tool
// call. Ordering by restrictiveness is Deny > Ask > Allow; NoDecision is the
// sentinel returned when no hook matched a request and is never produced by a
// hook's Evaluate.
type DecisionKind int

const (
	// Allow lets the tool body run.
	Allow DecisionKind = iota
	// Deny blocks the tool body; the executor returns a permission_denied Result.
	Deny
	// Ask defers to an AskResolver; with no resolver wired it fails closed to Deny.
	Ask
	// NoDecision means no hook matched. The executor treats it as a pass-through:
	// the tool runs exactly as if no chain were attached.
	NoDecision
)

// Decision is a hook's verdict plus a human-readable reason carried on Deny/Ask.
type Decision struct {
	Kind   DecisionKind
	Reason string
}

// AdmissionRequest is the immutable view of a tool call handed to every hook.
// State is the approved-set accessor and may be nil until one is wired.
type AdmissionRequest struct {
	Ctx       context.Context
	ToolName  string
	Params    map[string]interface{} // the tool's own params (MCP nested/carried op lives here)
	UserID    string                 // caller identity resolved from Ctx
	SessionID string                 // session identity resolved from Ctx
	State     ApprovedSetAccessor    // may be nil until an approved-set store is wired
}

// Hook is a single admission policy: it decides whether it applies to a request
// (Matches) and, when it does, returns its verdict (Evaluate).
//
// Both methods MUST be side-effect free, and both may be called MORE THAN ONCE
// for a single tool call: Preflight asks the same question Admit does without
// executing anything, so a call that is preflighted and then executed
// evaluates every matching hook twice (three times for a contact_human that a
// park re-classifies at resume). A hook that counts, meters, or records inside
// Evaluate will therefore over-count. Observation has its own seam — see
// PostToolHook, which runs exactly once, after the body.
//
// Reading mutable state is fine (the gated allowlist reads the approved set);
// WRITING it from Evaluate is not.
type Hook interface {
	Matches(req AdmissionRequest) bool
	Evaluate(req AdmissionRequest) Decision
}

// PostToolHook observes a tool call after the body has run. It never gates
// execution; it exists for recording and audit side effects.
type PostToolHook interface {
	Observe(req AdmissionRequest, result *Result)
}

// AskResolver turns an Ask verdict into a terminal Allow/Deny. A nil resolver
// means Ask fails closed to Deny.
type AskResolver interface {
	Resolve(req AdmissionRequest, d Decision) Decision
}

// CallIdentity is the shared call-equality key for approved-set membership.
// Canonicalize is the normalization that derives it.
type CallIdentity string

// ApprovedSetAccessor records and queries prior approvals keyed by a caller
// state key. ForgetSession is the reclamation half of the contract: a host
// that retires a session drops its approvals through it (the agent's
// DeleteSession does), so the set's growth is bounded by LIVE sessions, not by
// every session the process ever served.
type ApprovedSetAccessor interface {
	Record(ctx context.Context, stateKey string, ids []CallIdentity) error
	Contains(ctx context.Context, stateKey string, id CallIdentity) (bool, error)
	ForgetSession(sessionID string)
}

// AuditHook is a Hook that additionally reports how a final decision should be
// recorded. The chain checks matched hooks for this interface and carries the
// non-empty result on AdmissionResult.AuditDecision. A "" result is not recorded.
type AuditHook interface {
	Hook
	AuditDecisionFor(final Decision) string
}

// permHook adapts the name-level PermissionChecker into the admission chain as
// its first hook, preserving the tools.permissions allow/deny/disabled/yolo
// behavior. It matches every call and maps a CheckPermission error to Deny.
type permHook struct {
	pc *PermissionChecker
}

// newPermHook wraps a PermissionChecker as an admission Hook.
func newPermHook(pc *PermissionChecker) permHook {
	return permHook{pc: pc}
}

// Matches reports true for every request: the permission check applies to all
// tool calls, exactly as the name-level checker did.
func (h permHook) Matches(req AdmissionRequest) bool {
	return true
}

// Evaluate runs the name-level permission check. A nil checker or a nil error
// is Allow (the yolo short-circuit lives inside CheckPermission); any error is
// Deny carrying the checker's message.
func (h permHook) Evaluate(req AdmissionRequest) Decision {
	if h.pc == nil {
		return Decision{Kind: Allow}
	}
	if err := h.pc.CheckPermission(req.Ctx, req.ToolName, req.Params); err != nil {
		return Decision{Kind: Deny, Reason: err.Error()}
	}
	return Decision{Kind: Allow}
}
