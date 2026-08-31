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

// Preflight/Admit parity and the AskGrant contract. Preflight must agree with
// Admit on every verdict except Ask resolution (Preflight reports Ask, Admit
// resolves it), and a grant lifts ONLY an Ask — a Deny from any hook
// dominates in both entry points.

import (
	"context"
	"testing"
)

type verdictHook struct{ d Decision }

func (h verdictHook) Matches(AdmissionRequest) bool      { return true }
func (h verdictHook) Evaluate(AdmissionRequest) Decision { return h.d }

func req(ctx context.Context) AdmissionRequest {
	return AdmissionRequest{Ctx: ctx, ToolName: "t", Params: map[string]interface{}{}}
}

func TestPreflightAdmitParity(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name          string
		hooks         []Hook
		wantPreflight DecisionKind
		wantAdmit     DecisionKind // with no resolver
	}{
		{"no hooks", nil, NoDecision, NoDecision},
		{"allow", []Hook{verdictHook{Decision{Kind: Allow}}}, Allow, Allow},
		{"deny", []Hook{verdictHook{Decision{Kind: Deny, Reason: "no"}}}, Deny, Deny},
		{"ask", []Hook{verdictHook{Decision{Kind: Ask}}}, Ask, Deny}, // Admit fails closed without resolver
		{"ask+deny", []Hook{verdictHook{Decision{Kind: Ask}}, verdictHook{Decision{Kind: Deny, Reason: "hard"}}}, Deny, Deny},
		{"ask+allow", []Hook{verdictHook{Decision{Kind: Ask}}, verdictHook{Decision{Kind: Allow}}}, Ask, Deny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chain := NewChain(tc.hooks, nil, nil)
			if got := chain.Preflight(req(ctx)); got.Kind != tc.wantPreflight {
				t.Fatalf("Preflight = %v, want %v", got.Kind, tc.wantPreflight)
			}
			if got := chain.Admit(req(ctx)); got.Decision.Kind != tc.wantAdmit {
				t.Fatalf("Admit = %v, want %v", got.Decision.Kind, tc.wantAdmit)
			}
		})
	}
}

func TestAskGrantLiftsOnlyAsk(t *testing.T) {
	approve := ContextWithAskGrant(context.Background(), &AskGrant{Approved: true})
	reject := ContextWithAskGrant(context.Background(), &AskGrant{Approved: false, Reason: "nope"})

	askChain := NewChain([]Hook{verdictHook{Decision{Kind: Ask}}}, nil, nil)
	if got := askChain.Preflight(req(approve)); got.Kind != Allow {
		t.Fatalf("Preflight ask+approve grant = %v, want Allow", got.Kind)
	}
	if got := askChain.Admit(req(approve)); got.Decision.Kind != Allow {
		t.Fatalf("Admit ask+approve grant = %v, want Allow", got.Decision.Kind)
	}
	if got := askChain.Admit(req(reject)); got.Decision.Kind != Deny || got.Decision.Reason != "nope" {
		t.Fatalf("Admit ask+reject grant = %v (%q), want Deny with verbatim reason", got.Decision.Kind, got.Decision.Reason)
	}

	// A Deny is never lifted by a grant — combination lets Deny dominate.
	denyChain := NewChain([]Hook{verdictHook{Decision{Kind: Ask}}, verdictHook{Decision{Kind: Deny, Reason: "hard"}}}, nil, nil)
	if got := denyChain.Preflight(req(approve)); got.Kind != Deny {
		t.Fatalf("Preflight deny+grant = %v, want Deny", got.Kind)
	}
	if got := denyChain.Admit(req(approve)); got.Decision.Kind != Deny || got.Decision.Reason != "hard" {
		t.Fatalf("Admit deny+grant = %v (%q), want the hook's Deny", got.Decision.Kind, got.Decision.Reason)
	}

	// A grant leaves Allow / NoDecision untouched.
	allowChain := NewChain([]Hook{verdictHook{Decision{Kind: Allow}}}, nil, nil)
	if got := allowChain.Preflight(req(approve)); got.Kind != Allow {
		t.Fatalf("Preflight allow+grant = %v, want Allow", got.Kind)
	}
	empty := NewChain(nil, nil, nil)
	if got := empty.Preflight(req(reject)); got.Kind != NoDecision {
		t.Fatalf("Preflight empty+grant = %v, want NoDecision", got.Kind)
	}
}

func TestExecutorPreflight(t *testing.T) {
	ctx := context.Background()

	// Short-circuit: no chain, no permission checker → NoDecision, no
	// registry work (an unregistered name must not error or register).
	bare := NewExecutor(NewRegistry())
	if got := bare.Preflight(ctx, "unknown_tool", nil); got.Kind != NoDecision {
		t.Fatalf("bare Preflight = %v, want NoDecision", got.Kind)
	}

	// Governed executor: registered tool preflights through the chain.
	reg := NewRegistry()
	reg.Register(&MockTool{MockName: "governed"})
	exec := NewExecutor(reg)
	exec.SetAdmissionChain(NewChain([]Hook{verdictHook{Decision{Kind: Ask}}}, nil, nil))
	if got := exec.Preflight(ctx, "governed", map[string]interface{}{"x": 1}); got.Kind != Ask {
		t.Fatalf("governed Preflight = %v, want Ask", got.Kind)
	}
	// With a grant on ctx the same call preflights Allow (no park under
	// override).
	granted := ContextWithAskGrant(ctx, &AskGrant{Approved: true})
	if got := exec.Preflight(granted, "governed", map[string]interface{}{"x": 1}); got.Kind != Allow {
		t.Fatalf("governed Preflight with grant = %v, want Allow", got.Kind)
	}
	// Unknown tool on a governed executor: dynamic registration cannot
	// succeed here → NoDecision, execution will surface the real error.
	if got := exec.Preflight(ctx, "never_registered", nil); got.Kind != NoDecision {
		t.Fatalf("unknown-tool Preflight = %v, want NoDecision", got.Kind)
	}
}
