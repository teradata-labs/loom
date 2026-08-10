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

	"github.com/stretchr/testify/require"
)

// D-5 acceptance tests for the denylist policy decision body (admission_denylist.go).
// Each case builds the denylist Hook — scope ∧ matcher selection, deny decision —
// exactly as D-2's BuildChainFromConfig does for a `kind: denylist` binding, attaches
// it to the chain, and drives the real shuttle Executor's Execute interface (SE-1
// harness). The assertion is on observable admission effect: a governed call's denied
// Result with a zero body count, or a non-matching call's admitted run.

// denylistFor builds the denylist Hook a `kind: denylist` binding compiles to: the
// scope selector plus the compiled param matcher. Compilation failure fails the test —
// a test denylist must be well-formed.
func denylistFor(t *testing.T, scope string, spec MatcherSpec) denylist {
	t.Helper()
	matcher, err := spec.Compile()
	require.NoError(t, err)
	return denylist{scope: NewToolScope(scope), matcher: matcher}
}

// AC1: a call whose params match the configured pattern is denied and the tool body
// does not run (FR-009, SC-003). The deny returns via the chain as the executor's
// permission_denied Result and the tool's ExecuteCount stays at zero.
func TestDenylist_AC1_MatchingParamsDeniedAndBodySkipped(t *testing.T) {
	hook := denylistFor(t, "shell", MatcherSpec{ParamPath: "cmd", Op: "contains", Value: "rm -rf"})
	tool := countingTool("shell")

	res, err := execFor(NewChain([]Hook{hook}, nil, nil), tool).
		Execute(context.Background(), "shell", map[string]interface{}{"cmd": "rm -rf /"})

	require.NoError(t, err, "a policy deny is a Result, not a transport error")
	require.NotNil(t, res.Error)
	require.Equal(t, "permission_denied", res.Error.Code, "a call matching the denylist pattern is denied")
	require.False(t, res.Success)
	require.Equal(t, 0, tool.ExecuteCount, "the denied tool body does not run")
}

// AC1 (nested/carried op): the denylist matcher is D-2's single source of truth for
// param matching, so a matcher over a carried op nested under "params" denies the
// governed call identically to a top-level param. Same deny, same zero body count.
func TestDenylist_AC1_MatchingCarriedParamDenied(t *testing.T) {
	hook := denylistFor(t, "dispatch", MatcherSpec{
		ParamPath: "params.sql",
		Op:        "regex",
		Value:     "(?i)GRANT .* TO CURRENT_USER",
	})
	tool := countingTool("dispatch")

	res, err := execFor(NewChain([]Hook{hook}, nil, nil), tool).
		Execute(context.Background(), "dispatch", map[string]interface{}{
			"tool":   "sql_execute",
			"params": map[string]interface{}{"sql": "GRANT ROLE admin TO CURRENT_USER"},
		})

	require.NoError(t, err)
	require.Equal(t, "permission_denied", res.Error.Code, "a carried op matching the denylist pattern is denied")
	require.Equal(t, 0, tool.ExecuteCount, "the denied dispatch body does not run")
}

// AC2: a call whose params do not match is admitted and the tool runs (FR-009,
// SC-003). The denylist does not govern it (Matches is false ⇒ NoDecision ⇒
// pass-through), so the body runs once and no admission decision is stamped.
func TestDenylist_AC2_NonMatchingParamsAdmittedAndBodyRuns(t *testing.T) {
	hook := denylistFor(t, "shell", MatcherSpec{ParamPath: "cmd", Op: "contains", Value: "rm -rf"})
	tool := countingTool("shell")

	res, err := execFor(NewChain([]Hook{hook}, nil, nil), tool).
		Execute(context.Background(), "shell", map[string]interface{}{"cmd": "ls -la"})

	require.NoError(t, err)
	require.True(t, res.Success, "a call whose params miss the denylist pattern is admitted")
	require.Nil(t, res.Error)
	require.Equal(t, 1, tool.ExecuteCount, "the admitted tool body runs exactly once")
	require.NotContains(t, res.Metadata, "admission.decision", "an ungoverned call carries no admission decision")
}

// AC2 (unresolved param path): a call missing the param the matcher addresses does
// not match, so it is admitted and the tool runs — the matcher's not-found path is a
// non-match, never an accidental deny.
func TestDenylist_AC2_MissingParamPathAdmitted(t *testing.T) {
	hook := denylistFor(t, "shell", MatcherSpec{ParamPath: "cmd", Op: "contains", Value: "rm -rf"})
	tool := countingTool("shell")

	res, err := execFor(NewChain([]Hook{hook}, nil, nil), tool).
		Execute(context.Background(), "shell", map[string]interface{}{"other": "rm -rf /"})

	require.NoError(t, err)
	require.True(t, res.Success, "a call missing the matched param is admitted")
	require.Equal(t, 1, tool.ExecuteCount, "the admitted tool body runs exactly once")
}

// a denylist that sets the (unread) pattern field is refused at
// validation instead of silently denying everything in scope.
func TestDenylist_PatternField_RefusedLoudly(t *testing.T) {
	err := ValidateHooksConfig(HooksConfig{Bindings: []HookBinding{{
		Kind: "denylist", Scope: "sql_execute", Pattern: "(?i)GRANT|DROP",
	}}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "matcher")
}
