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

import (
	"fmt"
	"regexp"
	"strings"
)

// CustomHookRegistry resolves a custom binding's Name to the constructor that
// builds its hook. The constructor receives the full binding, so a custom hook
// is placed in the chain with the binding's scope and matcher like any library
// policy. A name with no registered constructor is a build error.
type CustomHookRegistry interface {
	lookup(name string) (func(HookBinding) (Hook, error), bool)
}

// ChainDeps carries the collaborators BuildChainFromConfig needs to turn
// bindings into live hooks: the name-level permission checker (folded in as the
// first hook), the approved-set accessor a gated-allowlist reads, the resolver
// an Ask verdict defers to, and the registry a custom binding resolves against.
// The pending-emit collaborator is NOT injected here — it is a constructor
// argument of NewHITLAskResolver, the one way in.
type ChainDeps struct {
	Perm        *PermissionChecker
	ApprovedSet ApprovedSetAccessor
	Ask         AskResolver
	Custom      CustomHookRegistry
}

// BuildChainFromConfig assembles the admission chain from a tools.hooks config.
// The name-level permission hook is placed first when a checker is supplied,
// then one hook per binding in config order. Each gated-allowlist binding also
// contributes a recorder post-tool hook — the sole writer of its approved set —
// so the render tool's completions populate the set the gated read side checks.
// A malformed binding — unknown kind, empty scope, bad matcher/read pattern, a
// gated-allowlist missing state_key/source_tool, or a custom name with no
// registered hook — is an error, so serve aborts rather than run silently
// ungoverned (fail-closed).
func BuildChainFromConfig(cfg HooksConfig, deps ChainDeps) (*Chain, error) {
	hooks := make([]Hook, 0, len(cfg.Bindings)+1)
	if deps.Perm != nil {
		hooks = append(hooks, newPermHook(deps.Perm))
	}
	var postHooks []PostToolHook
	for i, b := range cfg.Bindings {
		h, err := buildHook(b, deps)
		if err != nil {
			return nil, fmt.Errorf("tools.hooks[%d] (kind %q): %w", i, b.Kind, err)
		}
		hooks = append(hooks, h)
		if b.Kind == "gated-allowlist" {
			postHooks = append(postHooks, &recorder{
				sourceTool: b.SourceTool,
				stateKey:   b.StateKey,
				stmtParam:  b.StmtParam,
				resultPath: b.ResultPath,
				state:      deps.ApprovedSet,
			})
		}
	}
	return NewChain(hooks, postHooks, deps.Ask), nil
}

// ValidateHooksConfig reports the first structurally invalid binding in a
// tools.hooks config: an unknown kind, an empty scope, an uncompilable
// matcher/read_pattern, or a kind missing a required field. It shares
// validateBinding with BuildChainFromConfig, so a config that validates clean
// here builds without a structural error, and it wraps each fault with the
// offending binding's index and kind in BuildChainFromConfig's style. It is
// deps-free — callable at save time without a live ChainDeps — so a custom
// binding's name-registry membership is left to build (an unregistered name
// fails closed there).
func ValidateHooksConfig(cfg HooksConfig) error {
	return ValidateHooksConfigWithRegistry(cfg, nil)
}

// ValidateHooksConfigWithRegistry is ValidateHooksConfig plus custom-name
// resolution: when a registry is supplied, a custom binding whose Name has no
// registered constructor is a validation error — so a host's save door can
// reject an unknown custom hook at save time (C-010) instead of the operator
// discovering a deny-all agent at the next session build. Pass
// ProcessCustomHookRegistry() to validate against the process registry.
func ValidateHooksConfigWithRegistry(cfg HooksConfig, reg CustomHookRegistry) error {
	for i, b := range cfg.Bindings {
		if err := validateBinding(b); err != nil {
			return fmt.Errorf("tools.hooks[%d] (kind %q): %w", i, b.Kind, err)
		}
		if b.Kind == "custom" && reg != nil {
			if _, ok := reg.lookup(b.Name); !ok {
				return fmt.Errorf("tools.hooks[%d] (kind %q): custom hook %q is not registered", i, b.Kind, b.Name)
			}
		}
	}
	return nil
}

// validateBinding runs the binding's structural checks that need no live deps:
// scope presence, matcher and read_pattern compilability, and the per-kind
// required fields. It is the single source of a binding's field rules —
// buildHook runs it before constructing the live hook and ValidateHooksConfig
// runs it at save time — so build and save validation cannot drift. A custom
// binding's name-registry membership is not checked here: a save-time validator
// holds no runtime registry, so an unregistered name is caught at build instead
// (fail-closed).
func validateBinding(b HookBinding) error {
	if strings.TrimSpace(b.Scope) == "" {
		return fmt.Errorf("scope is required")
	}
	if _, err := b.Matcher.Compile(); err != nil {
		return err
	}
	switch b.Kind {
	case "audit", "ask":
		return nil
	case "denylist":
		if b.Pattern != "" {
			return fmt.Errorf("denylist does not take pattern — select calls with matcher (param_path/op/value); an unmatched field would silently widen the rule")
		}
		return nil
	case "gated-allowlist":
		if strings.TrimSpace(b.StateKey) == "" {
			return fmt.Errorf("gated-allowlist requires state_key")
		}
		if strings.TrimSpace(b.SourceTool) == "" {
			return fmt.Errorf("gated-allowlist requires source_tool")
		}
		if strings.TrimSpace(b.StmtParam) == "" {
			return fmt.Errorf("gated-allowlist requires stmt_param — without it every call canonicalizes to the empty identity and the gate degrades to a wildcard after any render")
		}
		if b.ReadPattern != "" {
			if _, err := compileReadPattern(b.ReadPattern); err != nil {
				return fmt.Errorf("invalid read_pattern %q: %w", b.ReadPattern, err)
			}
		}
		return nil
	case "custom":
		if strings.TrimSpace(b.Name) == "" {
			return fmt.Errorf("custom hook requires name")
		}
		return nil
	default:
		return fmt.Errorf("unknown kind (want gated-allowlist|denylist|audit|ask|custom)")
	}
}

// buildHook compiles one binding into a Hook. Its field rules are enforced by
// validateBinding first; selection (scope ∧ matcher) is then compiled for every
// kind, and the decision body is the kind's policy.
func buildHook(b HookBinding, deps ChainDeps) (Hook, error) {
	if err := validateBinding(b); err != nil {
		return nil, err
	}
	scope := NewToolScope(b.Scope)
	matcher, err := b.Matcher.Compile()
	if err != nil {
		return nil, err
	}

	switch b.Kind {
	case "denylist":
		return denylist{scope: scope, matcher: matcher}, nil
	case "ask":
		return askRule{scope: scope, matcher: matcher}, nil
	case "gated-allowlist":
		return gatedAllowlistHook(b, scope, matcher, deps)
	case "audit":
		return libraryAuditHook{scope: scope, matcher: matcher}, nil
	case "custom":
		return customHook(b, deps)
	default:
		return nil, fmt.Errorf("unknown kind (want gated-allowlist|denylist|audit|ask|custom)")
	}
}

// libraryHook is a config-built policy whose Matches is the scope ∧ matcher
// selection contract and whose Evaluate is the kind's decision body,
// supplied as a closure at build time.
type libraryHook struct {
	scope   ToolScope
	matcher Matcher
	eval    func(req AdmissionRequest) Decision
}

// Matches reports whether the binding governs this call: its tool is in scope
// and its params satisfy the matcher.
func (h libraryHook) Matches(req AdmissionRequest) bool {
	return h.scope.MatchesTool(req.ToolName) && h.matcher.MatchesParams(req.Params)
}

// Evaluate returns the policy's verdict for a governed call.
func (h libraryHook) Evaluate(req AdmissionRequest) Decision {
	return h.eval(req)
}

// gatedAllowlistHook admits a governed call only when its canonical identity is
// in the approved set at the binding's state_key, or — failing that — when the
// WHOLE payload classifies as read-only under read_pattern: the payload is split
// into statements and every statement must match the anchored pattern, so a
// write smuggled behind a read (`SELECT 1; DROP TABLE …`) never rides the read
// allowance. The set check runs FIRST, the read classification second. An empty
// canonical identity is never a membership key — it hard-denies. A missing
// entry, an accessor error, or no accessor wired is a hard deny (fail-closed) —
// never an ask. Field rules (state_key, source_tool, stmt_param, read_pattern
// compilability) are enforced by validateBinding before this constructs.
func gatedAllowlistHook(b HookBinding, scope ToolScope, matcher Matcher, deps ChainDeps) (Hook, error) {
	var readRe *regexp.Regexp
	if b.ReadPattern != "" {
		re, err := compileReadPattern(b.ReadPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid read_pattern %q: %w", b.ReadPattern, err)
		}
		readRe = re
	}
	stateKey := b.StateKey
	stmtParam := b.StmtParam
	accessor := deps.ApprovedSet

	eval := func(req AdmissionRequest) Decision {
		state := req.State
		if state == nil {
			state = accessor
		}
		if state == nil {
			return Decision{Kind: Deny, Reason: "gated-allowlist has no approved-set store"}
		}
		id := Canonicalize(req.ToolName, req.Params, stmtParam)
		if id != "" {
			ok, err := state.Contains(req.Ctx, stateKey, id)
			if err != nil {
				return Decision{Kind: Deny, Reason: fmt.Sprintf("approved-set lookup failed: %v", err)}
			}
			if ok {
				return Decision{Kind: Allow}
			}
		}
		if readRe != nil && readOnlyPayload(stmtValue(req.Params, stmtParam), readRe) {
			return Decision{Kind: Allow}
		}
		if id == "" {
			return Decision{Kind: Deny, Reason: "call carries no statement to judge (empty or non-string stmt_param)"}
		}
		return Decision{Kind: Deny, Reason: "call not in approved set"}
	}

	return libraryHook{scope: scope, matcher: matcher, eval: eval}, nil
}

// compileReadPattern anchors the operator's read_pattern to the start of each
// statement: the pattern classifies what KIND of statement this is, so it must
// match at the head, not anywhere inside (a bare `SELECT` must not bless
// `DELETE … WHERE id IN (SELECT …)`).
func compileReadPattern(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile("^(?:" + pattern + ")")
}

// readOnlyPayload reports whether the WHOLE payload is read-only under the
// anchored pattern: it splits on ';' into statements and requires every
// non-empty statement to match. An empty payload is not read-only. Splitting is
// syntactic — a ';' inside a string literal splits too, which can only deny a
// legitimate read, never admit a write (fail-closed).
func readOnlyPayload(payload string, readRe *regexp.Regexp) bool {
	stmts := splitStatements(payload)
	if len(stmts) == 0 {
		return false
	}
	for _, s := range stmts {
		if !readRe.MatchString(s) {
			return false
		}
	}
	return true
}

// splitStatements cuts a payload on ';' into trimmed, non-empty statements.
func splitStatements(payload string) []string {
	parts := strings.Split(payload, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// customHook resolves a custom binding's Name to its constructor and builds the
// hook from the binding, so the binding's scope and matcher apply. Name presence
// is enforced by validateBinding; an unconfigured registry or an unregistered
// name is a build error (fail-closed), deferred here because a save-time
// validator holds no runtime registry.
func customHook(b HookBinding, deps ChainDeps) (Hook, error) {
	if deps.Custom == nil {
		return nil, fmt.Errorf("custom hook %q named but no custom hook registry is configured", b.Name)
	}
	ctor, ok := deps.Custom.lookup(b.Name)
	if !ok {
		return nil, fmt.Errorf("custom hook %q is not registered", b.Name)
	}
	return ctor(b)
}

// libraryAuditHook records but never gates: Evaluate is Allow, and the AuditHook
// marker reports the chain's final decision to the persist path.
type libraryAuditHook struct {
	scope   ToolScope
	matcher Matcher
}

// Matches reports whether the audit binding governs this call.
func (h libraryAuditHook) Matches(req AdmissionRequest) bool {
	return h.scope.MatchesTool(req.ToolName) && h.matcher.MatchesParams(req.Params)
}

// Evaluate always allows; audit is observability, not enforcement.
func (h libraryAuditHook) Evaluate(req AdmissionRequest) Decision {
	return Decision{Kind: Allow}
}

// AuditDecisionFor renders the chain's final verdict for the audit record.
func (h libraryAuditHook) AuditDecisionFor(final Decision) string {
	return decisionLabel(final.Kind)
}

// Canonicalize derives the call-equality key an approved-set membership check
// compares on. Normalization is conservative and fail-closed — it prefers a
// false-deny over a false-allow and never widens the equivalence class: it takes
// the SQL statement param named by stmtParam, trims it, collapses internal
// whitespace runs to a single space, and strips a trailing ';'. No keyword or
// case folding, no semantic rewrite. The write side that records approvals and
// this read side normalize through this one function, so a rendered statement
// matches its re-emitted whitespace-different form while a textually-outside
// statement does not. toolName selects the normalization; only the SQL statement
// normalization exists today.
func Canonicalize(toolName string, params map[string]interface{}, stmtParam string) CallIdentity {
	stmt := stmtValue(params, stmtParam)
	stmt = strings.Join(strings.Fields(stmt), " ") // trim + collapse whitespace runs
	stmt = strings.TrimSuffix(stmt, ";")
	stmt = strings.TrimRight(stmt, " ") // a ';' preceded by whitespace leaves one trailing space
	return CallIdentity(stmt)
}

// stmtValue reads the statement param a gated-allowlist keys on; a missing or
// non-string value yields the empty statement.
func stmtValue(params map[string]interface{}, stmtParam string) string {
	if stmtParam == "" {
		return ""
	}
	if v, ok := params[stmtParam]; ok {
		return valueToString(v)
	}
	return ""
}

// decisionLabel maps a decision kind to its audit label; a non-gating outcome
// has no label.
func decisionLabel(k DecisionKind) string {
	switch k {
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	case Ask:
		return "ask"
	default:
		return ""
	}
}
