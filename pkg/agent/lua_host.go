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
	"strings"
	"unicode/utf8"

	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Names of the Lua tools and the prefix of tools made from saved scripts. All
// three are reserved: the bridge denies them to every script, so a script can
// never start another script.
const (
	RunLuaToolName           = "run_lua"
	ManageLuaScriptsToolName = "manage_lua_scripts"
	LuaScriptToolPrefix      = "lua_"
)

// Errors returned by NewLuaHost. Each one means the bridge cannot prove what a
// script may do, so the run must not start (fail closed).
var (
	ErrLuaNoSession    = errors.New("lua host: the tool call has no session id")
	ErrLuaNoProjection = errors.New("lua host: the tool call carries no record of the tools the model was shown")
	ErrLuaNoGuards     = errors.New("lua host: the agent's executor has neither an admission chain nor a permission checker")
)

// luaLateAskReason is the deny reason of the grant nested calls execute under.
// A hook that answers Ask during execution, after Preflight allowed the call,
// is resolved by that grant at once instead of waiting on a human; the bridge
// recognizes this reason and reports approval_required.
const luaLateAskReason = "lua: this call needs approval; call the tool directly so the user can approve it"

// luaToolSummaryMax bounds the one-line descriptions ListTools returns.
const luaToolSummaryMax = 160

// LuaHostOptions carries everything one run's bridge needs. The engine's
// Policy has no trust field (Policy.Visible and Policy.Check take it
// separately), so the resolved trust travels here.
type LuaHostOptions struct {
	// SessionID is the session whose tools the script may use: the ctx value
	// "session_id" set by executeToolWithSelfCorrection.
	SessionID string
	// Policy holds the tool lists from the host's configuration. The bridge
	// adds the reserved Lua names itself; its limits are not used here.
	Policy luasandbox.Policy
	// Trust says who wrote the script.
	Trust luasandbox.Trust
	// Requires, when non-nil, is a saved script's manifest.requires: the
	// script may call only these tools, even if more are visible.
	Requires []string
	// Progress receives the script's activity. It may be nil and must not
	// block.
	Progress func(luasandbox.ProgressEvent)
}

// NewLuaHost returns the bridge that lets one script call this agent's tools.
//
// The visible set is fixed once, here: the tools the model was shown for the
// provider call that requested this run (recorded on ctx by the conversation
// loop, after circuit-breaker filtering), minus the policy's deny lists and
// the reserved Lua names, intersected with Requires when set. Every nested call
// is matched against it by exact name and then runs through the executor's
// Preflight and ExecuteWithTool, the same admission path as a direct call.
//
// It fails closed when the session id is missing, when ctx carries no
// projection, or when the executor has no guard at all.
func (a *Agent) NewLuaHost(ctx context.Context, opt LuaHostOptions) (luasandbox.Host, error) {
	if opt.SessionID == "" {
		return nil, ErrLuaNoSession
	}
	if a.executor == nil || !a.executor.HasAdmissionGuards() {
		return nil, ErrLuaNoGuards
	}
	projection, ok := advertisedProjectionFromContext(ctx)
	if !ok {
		return nil, ErrLuaNoProjection
	}
	pol := withLuaReservedNames(opt.Policy)
	if err := pol.Validate(); err != nil {
		return nil, fmt.Errorf("lua host: %w", err)
	}

	names := pol.Visible(projection, opt.Trust)
	if opt.Requires != nil {
		names = intersectNames(names, opt.Requires)
	}
	h := &luaHost{
		agent:     a,
		sessionID: opt.SessionID,
		policy:    pol,
		trust:     opt.Trust,
		progress:  opt.Progress,
		visible:   make(map[string]shuttle.Tool, len(names)),
	}
	for _, name := range names {
		tool, ok := a.tools.Get(name)
		if !ok || tool == nil {
			continue // unregistered since the provider call; not callable
		}
		h.visible[name] = tool
		h.order = append(h.order, name)
	}
	return h, nil
}

// withLuaReservedNames returns a copy of pol with the Lua tool names and the
// script-tool prefix reserved, whatever the host configured.
func withLuaReservedNames(pol luasandbox.Policy) luasandbox.Policy {
	out := pol
	out.Reserved = append(append([]string(nil), pol.Reserved...), RunLuaToolName, ManageLuaScriptsToolName)
	out.ReservedPrefixes = append(append([]string(nil), pol.ReservedPrefixes...), LuaScriptToolPrefix)
	return out
}

func intersectNames(names, allowed []string) []string {
	keep := make(map[string]bool, len(allowed))
	for _, n := range allowed {
		keep[n] = true
	}
	out := names[:0:0]
	for _, n := range names {
		if keep[n] {
			out = append(out, n)
		}
	}
	return out
}

// luaHost implements luasandbox.Host for one run. All of its fields are set by
// NewLuaHost and only read afterwards, so it needs no lock, and it holds none
// while a nested tool runs.
type luaHost struct {
	agent     *Agent
	sessionID string
	policy    luasandbox.Policy
	trust     luasandbox.Trust
	progress  func(luasandbox.ProgressEvent)
	visible   map[string]shuttle.Tool
	order     []string // visible names, sorted
}

var _ luasandbox.Host = (*luaHost)(nil)

// ListTools returns the visible tools with one-line descriptions.
func (h *luaHost) ListTools(context.Context) ([]luasandbox.ToolInfo, error) {
	out := make([]luasandbox.ToolInfo, 0, len(h.order))
	for _, name := range h.order {
		out = append(out, luasandbox.ToolInfo{Name: name, Description: oneLineSummary(h.visible[name].Description())})
	}
	return out, nil
}

// ToolSchema returns a visible tool's input schema as a JSON-compatible map.
func (h *luaHost) ToolSchema(_ context.Context, name string) (map[string]any, error) {
	tool, ok := h.visible[name]
	if !ok {
		return nil, luasandbox.ErrToolNotVisible
	}
	schema := tool.InputSchema()
	if schema == nil {
		return map[string]any{"type": "object"}, nil
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("lua host: schema of %s: %w", name, err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("lua host: schema of %s: %w", name, err)
	}
	return out, nil
}

// CallTool runs one nested call. Tool failures are results the script sees;
// a Go error is returned only when the host itself is broken.
func (h *luaHost) CallTool(ctx context.Context, name string, args map[string]any) (*luasandbox.CallResult, error) {
	tool, ok := h.visible[name]
	if !ok {
		return h.notVisible(name), nil
	}

	ctx, span := h.agent.tracer.StartSpan(ctx, "lua.tool_call")
	defer h.agent.tracer.EndSpan(span)
	span.SetAttribute("tool_name", name)
	span.SetAttribute("lua.trust", h.trust.String())

	// An approval granted for the run_lua call itself never covers the calls
	// the script makes.
	callCtx := shuttle.ContextWithoutAskGrant(ctx)

	// Preflight first, so a call that needs a human is reported and never
	// waits for one.
	switch dec := h.agent.executor.Preflight(callCtx, name, args); dec.Kind {
	case shuttle.Ask:
		span.SetAttribute("lua.decision", "approval_required")
		return &luasandbox.CallResult{
			OK: false,
			Error: &luasandbox.CallError{
				Code:       luasandbox.CodeApprovalRequired,
				Message:    name + " needs the user's approval",
				Suggestion: "call this tool directly so the user can approve it",
			},
			Decision: "approval_required",
		}, nil
	case shuttle.Deny:
		span.SetAttribute("lua.decision", "deny")
		return &luasandbox.CallResult{
			OK:       false,
			Error:    &luasandbox.CallError{Code: luasandbox.CodePermissionDenied, Message: dec.Reason},
			Decision: "deny",
		}, nil
	}

	// Execute under a deny grant: if a hook answers Ask now (state changed
	// since Preflight), the chain resolves it from the grant at once instead
	// of calling the blocking resolver.
	execCtx := shuttle.ContextWithAskGrant(callCtx, &shuttle.AskGrant{Approved: false, Reason: luaLateAskReason})
	res, err := h.agent.executor.ExecuteWithTool(execCtx, tool, args)
	if err != nil {
		span.SetAttribute("lua.decision", "allow")
		return &luasandbox.CallResult{
			OK:       false,
			Error:    &luasandbox.CallError{Code: luasandbox.CodeExecutionFailed, Message: err.Error()},
			Decision: "allow",
		}, nil
	}
	if res == nil {
		return &luasandbox.CallResult{
			OK:       false,
			Error:    &luasandbox.CallError{Code: luasandbox.CodeExecutionFailed, Message: name + " returned no result"},
			Decision: "allow",
		}, nil
	}
	return h.mapResult(name, res, span), nil
}

// mapResult converts an executed call's Result for the script.
func (h *luaHost) mapResult(name string, res *shuttle.Result, span *observability.Span) *luasandbox.CallResult {
	if res.Success && res.AwaitResource != nil {
		// Parking a turn on a long-running job is an agent-loop feature;
		// a script polls instead.
		span.SetAttribute("lua.decision", "allow")
		return &luasandbox.CallResult{
			OK: false,
			Error: &luasandbox.CallError{
				Code:       luasandbox.CodeResourcePending,
				Message:    res.AwaitResource.URI,
				Suggestion: "the tool started a long-running job; poll it later",
				Retryable:  true,
			},
			Decision: "allow",
		}
	}
	if res.Success {
		span.SetAttribute("lua.decision", "allow")
		return &luasandbox.CallResult{OK: true, Data: res.Data, Decision: "allow"}
	}

	e := res.Error
	if e == nil {
		e = &shuttle.Error{Code: luasandbox.CodeExecutionFailed, Message: name + " failed"}
	}
	out := &luasandbox.CallResult{
		OK: false,
		Error: &luasandbox.CallError{
			Code:       e.Code,
			Message:    e.Message,
			Suggestion: e.Suggestion,
			Retryable:  e.Retryable,
		},
		Decision: "allow",
	}
	switch {
	case e.Code == luasandbox.CodePermissionDenied && strings.Contains(e.Message, luaLateAskReason):
		out.Error = &luasandbox.CallError{
			Code:       luasandbox.CodeApprovalRequired,
			Message:    name + " needs the user's approval",
			Suggestion: "call this tool directly so the user can approve it",
		}
		out.Decision = "approval_required"
	case e.Code == luasandbox.CodePermissionDenied:
		out.Decision = "deny"
	}
	if out.Error.Code == "" {
		out.Error.Code = luasandbox.CodeExecutionFailed
	}
	span.SetAttribute("lua.decision", out.Decision)
	return out
}

// notVisible answers a call to a tool outside the visible set.
func (h *luaHost) notVisible(name string) *luasandbox.CallResult {
	code, _ := h.policy.Check(name, h.trust)
	decision := "not_visible"
	switch {
	case code == luasandbox.CodeHardDenied:
		decision = "hard_denied"
	default:
		code = luasandbox.CodeToolNotVisible
	}
	return &luasandbox.CallResult{
		OK: false,
		Error: &luasandbox.CallError{
			Code:       code,
			Message:    name + " is not available to this script",
			Suggestion: "tools.list() returns the tools this script may call",
		},
		Decision: decision,
	}
}

// TurnResult returns a tool result produced earlier in the current turn.
func (h *luaHost) TurnResult(_ context.Context, messageID int64) (*luasandbox.CallResult, error) {
	payload, err := h.agent.InTurnPayload(h.sessionID, messageID)
	if err != nil {
		if errors.Is(err, ErrNotThisTurn) {
			return &luasandbox.CallResult{
				OK: false,
				Error: &luasandbox.CallError{
					Code:       "NOT_FOUND",
					Message:    fmt.Sprintf("message %d is not a tool result from the current turn", messageID),
					Suggestion: "turn.result reads only this turn's tool results; call the tool again instead",
				},
			}, nil
		}
		return nil, err
	}
	return &luasandbox.CallResult{OK: true, Data: payload}, nil
}

// Progress forwards the script's activity to the caller's callback.
func (h *luaHost) Progress(ev luasandbox.ProgressEvent) {
	if h.progress != nil {
		h.progress(ev)
	}
}

// oneLineSummary returns the first sentence of s, at most luaToolSummaryMax
// bytes and cut on a word boundary.
func oneLineSummary(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	if len(s) <= luaToolSummaryMax {
		return s
	}
	end := luaToolSummaryMax
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	cut := s[:end]
	if i := strings.LastIndexByte(cut, ' '); i > luaToolSummaryMax/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:") + "…"
}
