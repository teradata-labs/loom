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

import "fmt"

// AdmissionResult is the outcome of running the chain for one tool call.
// AuditDecision is set when a matched hook is an AuditHook and its
// AuditDecisionFor returns a non-empty string; it is carried to the persist
// path.
type AdmissionResult struct {
	Decision      Decision
	AuditDecision string
}

// PersistedDecision is the value the executor stamps into the tool-execution
// record: a matched audit hook's decision when one fired, and otherwise "deny"
// for a denied call — a denial is intrinsically classifiable and the analytics
// view keys policy_denied_count on this column, so it must ride every deny,
// audited or not. A non-denied, non-audited call stamps nothing.
func (r AdmissionResult) PersistedDecision() string {
	if r.AuditDecision != "" {
		return r.AuditDecision
	}
	if r.Decision.Kind == Deny {
		return "deny"
	}
	return ""
}

// Chain evaluates admission hooks for every tool call and combines their
// verdicts into a single decision. It also fans out post-tool observation to
// its PostToolHooks after a tool body runs.
type Chain struct {
	hooks       []Hook
	postHooks   []PostToolHook
	askResolver AskResolver
}

// NewChain builds a chain from ordered admission hooks, post-tool hooks, and an
// optional Ask resolver. Hook order is the caller's responsibility (the name-level
// permission hook is placed first by the builder); combination is order-independent.
func NewChain(hooks []Hook, postHooks []PostToolHook, askResolver AskResolver) *Chain {
	return &Chain{
		hooks:       hooks,
		postHooks:   postHooks,
		askResolver: askResolver,
	}
}

// Admit evaluates every hook whose Matches reports true, combines their verdicts
// as Deny > Ask > Allow (most restrictive wins), and resolves an Ask outcome via
// the AskResolver — with no resolver, Ask fails closed to Deny. A hook that
// panics is treated as a matched Deny (fail-closed). With no matching hook the
// decision is NoDecision, which the executor treats as today's pass-through.
// For each matched AuditHook, AuditDecisionFor(final) sets AdmissionResult.AuditDecision.
func (c *Chain) Admit(req AdmissionRequest) AdmissionResult {
	if c == nil {
		return AdmissionResult{Decision: Decision{Kind: NoDecision}}
	}

	final, matched := c.combine(req)

	if final.Kind == Ask {
		if granted, ok := applyAskGrant(req, final); ok {
			final = granted
		} else if c.askResolver != nil {
			final = resolveAsk(c.askResolver, req, final)
		} else {
			final = Decision{Kind: Deny, Reason: "tool call requires approval but no approval resolver is configured"}
		}
	}

	auditDecision := ""
	for _, h := range matched {
		if ah, ok := h.(AuditHook); ok {
			if s := ah.AuditDecisionFor(final); s != "" {
				auditDecision = s
			}
		}
	}

	return AdmissionResult{Decision: final, AuditDecision: auditDecision}
}

// Preflight combines hook verdicts exactly as Admit does and resolves an Ask
// ONLY from a context AskGrant (deterministic, non-blocking); with no grant
// the Ask is returned as Ask. The blocking resolver is never invoked and no
// audit decision is computed — audit stamps belong to the real execution
// pass. Used by the park pre-scan to learn whether a call would hold, without
// side effects. Nil chain → NoDecision.
func (c *Chain) Preflight(req AdmissionRequest) Decision {
	if c == nil {
		return Decision{Kind: NoDecision}
	}
	final, _ := c.combine(req)
	if final.Kind == Ask {
		if granted, ok := applyAskGrant(req, final); ok {
			final = granted
		}
	}
	return final
}

// combine evaluates every matching hook and folds their verdicts under
// Deny > Ask > Allow > NoDecision — the shared first half of Admit and
// Preflight, so the two can never drift.
func (c *Chain) combine(req AdmissionRequest) (Decision, []Hook) {
	final := Decision{Kind: NoDecision}
	matched := make([]Hook, 0, len(c.hooks))

	for _, h := range c.hooks {
		if h == nil {
			continue
		}
		ok, d := evalHook(h, req)
		if !ok {
			continue
		}
		matched = append(matched, h)
		final = moreRestrictive(final, d)
	}
	return final, matched
}

// applyAskGrant resolves an Ask from a context AskGrant when one is installed.
// The grant lifts only the Ask — combine has already let any Deny dominate.
func applyAskGrant(req AdmissionRequest, d Decision) (Decision, bool) {
	if d.Kind != Ask || req.Ctx == nil {
		return d, false
	}
	if g := AskGrantFromContext(req.Ctx); g != nil {
		return g.Decide(), true
	}
	return d, false
}

// Observe fans a completed tool call out to every post-tool hook. A nil chain
// or nil hook is a no-op; a panicking hook is contained so observation cannot
// break execution that already completed.
func (c *Chain) Observe(req AdmissionRequest, result *Result) {
	if c == nil {
		return
	}
	for _, h := range c.postHooks {
		if h == nil {
			continue
		}
		observeOne(h, req, result)
	}
}

// evalHook matches then evaluates a hook, recovering a panic in either call into
// a matched Deny so a misbehaving hook fails closed rather than opening a bypass.
func evalHook(h Hook, req AdmissionRequest) (matched bool, d Decision) {
	defer func() {
		if r := recover(); r != nil {
			matched = true
			d = Decision{Kind: Deny, Reason: fmt.Sprintf("admission hook panicked: %v", r)}
		}
	}()
	if !h.Matches(req) {
		return false, Decision{Kind: NoDecision}
	}
	return true, h.Evaluate(req)
}

// observeOne runs a single post-tool hook, containing any panic.
func observeOne(h PostToolHook, req AdmissionRequest, result *Result) {
	defer func() { _ = recover() }()
	h.Observe(req, result)
}

// resolveAsk runs the Ask resolver with the same fail-closed panic containment
// evalHook gives hooks: the resolver blocks across store polls and runs outside
// evalHook's recover, and a panic there must become a Deny for this call — never
// an unwound serving process.
func resolveAsk(r AskResolver, req AdmissionRequest, d Decision) (out Decision) {
	defer func() {
		if p := recover(); p != nil {
			out = Decision{Kind: Deny, Reason: fmt.Sprintf("approval resolver panicked: %v", p)}
		}
	}()
	return r.Resolve(req, d)
}

// moreRestrictive returns whichever decision is more restrictive under
// Deny > Ask > Allow; ties keep the incumbent so an earlier hook's reason wins.
func moreRestrictive(a, b Decision) Decision {
	if restrictiveness(b.Kind) > restrictiveness(a.Kind) {
		return b
	}
	return a
}

// restrictiveness ranks decision kinds so combination is independent of the
// DecisionKind iota values. Allow ranks ABOVE NoDecision: a matched hook's
// explicit Allow must displace the NoDecision seed, or an audited allowed call
// would report no decision at all and its audit record would never be written.
func restrictiveness(k DecisionKind) int {
	switch k {
	case Deny:
		return 3
	case Ask:
		return 2
	case Allow:
		return 1
	default:
		return 0
	}
}
