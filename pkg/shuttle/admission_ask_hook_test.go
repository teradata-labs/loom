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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/session"
)

// D-14 acceptance tests for the `ask` rule kind (admission_ask_hook.go). Every
// case turns a `tools.hooks` fixture into a live chain via the real
// BuildChainFromConfig — no test constructs an askRule by hand — and drives the
// real shuttle Executor's Execute interface (SE-1 harness). The assertion is on
// observable admission effect: whether the configured AskResolver was consulted,
// the terminal Result, and whether the tool body ran.

// askChain builds the chain a single `ask` binding scoped to sql_execute with a
// param matcher over "sql" compiles to, wired to the given deps. Selection is
// the shared scope ∧ matcher contract: sql_execute calls whose sql contains
// GRANT are governed.
func askChain(t *testing.T, deps ChainDeps) *Chain {
	t.Helper()
	const fixture = `
hooks:
  - kind: ask
    scope: sql_execute
    matcher:
      param_path: sql
      op: contains
      value: GRANT
`
	chain, err := BuildChainFromConfig(hooksFromYAML(t, fixture), deps)
	require.NoError(t, err, "a well-formed ask binding builds into a live hook")
	require.NotNil(t, chain)
	return chain
}

// -- AC1: selection is the shared scope ∧ matcher contract -------------------

// A call whose tool is in scope AND whose params satisfy the matcher is governed
// by the ask binding. Governance is observed through the resolver: a resolver
// that denies turns the Ask into a permission_denied Result with the body
// unrun, which only happens if the hook reached the Ask verdict.
func TestAskRule_AC1_InScopeAndMatchingParams_IsGoverned(t *testing.T) {
	chain := askChain(t, ChainDeps{Ask: resolveTo{decision: Decision{Kind: Deny, Reason: "reviewer declined"}}})
	tool := countingTool("sql_execute")

	res, err := execFor(chain, tool).
		Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": "GRANT ROLE admin TO alice"})

	require.NoError(t, err, "a policy deny is a Result, not a transport error")
	require.NotNil(t, res.Error)
	require.Equal(t, "permission_denied", res.Error.Code,
		"the in-scope, matching call reached the ask binding's resolver")
	require.Equal(t, 0, tool.ExecuteCount, "the held tool body does not run")
}

// A call to a tool outside the binding's scope is not governed, even when its
// params would satisfy the matcher. The resolver — which would deny — is never
// consulted, so the call is admitted and the body runs.
func TestAskRule_AC1_OutOfScopeTool_IsNotGoverned(t *testing.T) {
	chain := askChain(t, ChainDeps{Ask: resolveTo{decision: Decision{Kind: Deny, Reason: "reviewer declined"}}})
	tool := countingTool("shell")

	res, err := execFor(chain, tool).
		Execute(context.Background(), "shell", map[string]interface{}{"sql": "GRANT ROLE admin TO alice"})

	require.NoError(t, err)
	require.True(t, res.Success, "a tool outside the ask binding's scope is admitted")
	require.Nil(t, res.Error)
	require.Equal(t, 1, tool.ExecuteCount, "the ungoverned tool body runs exactly once")
}

// A call to the scoped tool whose params miss the matcher is not governed. The
// denying resolver is never consulted, so the call is admitted and the body runs.
func TestAskRule_AC1_InScopeButParamsMissMatcher_IsNotGoverned(t *testing.T) {
	chain := askChain(t, ChainDeps{Ask: resolveTo{decision: Decision{Kind: Deny, Reason: "reviewer declined"}}})
	tool := countingTool("sql_execute")

	res, err := execFor(chain, tool).
		Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": "SELECT 1"})

	require.NoError(t, err)
	require.True(t, res.Success, "a call whose params miss the ask matcher is admitted")
	require.Equal(t, 1, tool.ExecuteCount, "the ungoverned tool body runs exactly once")
}

// -- AC2: a governed call defers to the configured AskResolver; an ungoverned
// call contributes no decision -----------------------------------------------

// A resolver that approves admits the governed call and the tool body runs.
func TestAskRule_AC2_GovernedCall_ResolverAllows_AdmitsAndRunsBody(t *testing.T) {
	chain := askChain(t, ChainDeps{Ask: resolveTo{decision: Decision{Kind: Allow}}})
	tool := countingTool("sql_execute")

	res, err := execFor(chain, tool).
		Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": "GRANT ROLE admin TO alice"})

	require.NoError(t, err)
	require.True(t, res.Success, "an approved held call is admitted")
	require.Nil(t, res.Error)
	require.Equal(t, 1, tool.ExecuteCount, "the approved tool body runs exactly once")
}

// A resolver that rejects denies the governed call, and the resolver's reason —
// not a reason minted by the ask rule — is what the caller sees, byte-identical.
func TestAskRule_AC2_GovernedCall_ResolverDenies_CarriesResolverReason(t *testing.T) {
	const note = "rejected by reviewer@example.com: grant is out of policy"
	chain := askChain(t, ChainDeps{Ask: resolveTo{decision: Decision{Kind: Deny, Reason: note}}})
	tool := countingTool("sql_execute")

	res, err := execFor(chain, tool).
		Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": "GRANT ROLE admin TO alice"})

	require.NoError(t, err)
	require.NotNil(t, res.Error)
	require.Equal(t, "permission_denied", res.Error.Code)
	require.Equal(t, note, res.Error.Message, "the resolver's reason reaches the caller verbatim")
	require.Equal(t, 0, tool.ExecuteCount, "the rejected tool body does not run")
}

// An ungoverned call yields no decision from the ask binding: the chain reaches
// NoDecision, so the executor passes through exactly as with no chain attached.
// The resolver wired here would deny if it were ever consulted, and the
// pass-through path stamps no admission metadata.
func TestAskRule_AC2_UngovernedCall_YieldsNoDecision(t *testing.T) {
	chain := askChain(t, ChainDeps{Ask: resolveTo{decision: Decision{Kind: Deny, Reason: "resolver must not be consulted"}}})
	tool := countingTool("sql_execute")

	res, err := execFor(chain, tool).
		Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": "SELECT count(*) FROM t"})

	require.NoError(t, err)
	require.True(t, res.Success)
	require.Equal(t, 1, tool.ExecuteCount)
	require.NotContains(t, res.Metadata, "admission.decision",
		"a call the ask binding does not govern carries no admission decision")
}

// Against the real HITL resolver, a governed call raises a pending approval
// request carrying the ask rule's Reason as the approver-facing context, and
// does not return until a separate actor resolves it — approving admits the
// call and runs the body. This is the ask kind driving CT-2's concrete
// resolver, not a stub.
func TestAskRule_AC2_GovernedCall_HITLResolver_RaisesRequestThenAdmitsOnApproval(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	chain := askChain(t, ChainDeps{
		// nil notifier: this case asserts the hold and its resolution, not the
		// pending-emit, which leaves the hold behavior unchanged.
		Ask: NewHITLAskResolver(store, 2*time.Second, 10*time.Millisecond, nil),
	})
	tool := countingTool("sql_execute")
	exec := execFor(chain, tool)
	exec.SetIdentityResolver(func(context.Context) string { return "user-1" })
	ctx := session.WithSessionID(context.Background(), "sess-1")

	done := make(chan *Result, 1)
	go func() {
		res, _ := exec.Execute(ctx, "sql_execute", map[string]interface{}{"sql": "GRANT ROLE admin TO alice"})
		done <- res
	}()

	// While the call is held, exactly one pending approval request is present,
	// carrying the ask rule's reason for the approver to read.
	hr := waitForPending(t, store)
	require.Equal(t, "approval", hr.RequestType)
	require.Equal(t, "pending", hr.Status)
	require.Equal(t, "sql_execute", hr.Context["tool"])
	require.Equal(t, "approval required by ask rule", hr.Context["reason"],
		"the ask rule's Reason reaches the raised request's context")

	// A separate actor approves; the held call is then admitted.
	require.NoError(t, store.RespondToRequest(context.Background(), hr.ID, "approved", "", "reviewer@example.com", nil))
	res := <-done
	require.NotNil(t, res)
	require.True(t, res.Success, "the approved held call is admitted")
	require.Equal(t, 1, tool.ExecuteCount, "the approved tool body runs exactly once")
}

// -- AC6: no resolver wired ⇒ the governed call fails closed to Deny ----------

// Built with no AskResolver in its deps, the ask binding's governed call is
// denied by the chain's existing fail-closed default and the body never runs.
func TestAskRule_AC6_NoResolverWired_GovernedCallFailsClosed(t *testing.T) {
	chain := askChain(t, ChainDeps{}) // no Ask resolver
	tool := countingTool("sql_execute")

	res, err := execFor(chain, tool).
		Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": "GRANT ROLE admin TO alice"})

	require.NoError(t, err)
	require.NotNil(t, res.Error)
	require.Equal(t, "permission_denied", res.Error.Code)
	require.Equal(t, "tool call requires approval but no approval resolver is configured", res.Error.Message)
	require.Equal(t, 0, tool.ExecuteCount, "fail-closed: the held body never runs without a resolver")
}
