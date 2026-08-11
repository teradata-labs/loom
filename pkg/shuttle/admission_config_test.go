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
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// D-2 acceptance tests for the config-driven library framework at the
// build-from-config seam. Each case turns a `tools.hooks` fixture into a live
// chain via BuildChainFromConfig — the produced §5.2 contract — and drives the
// real shuttle Executor so the assertion is on observable admission effect
// (denied Result + zero body runs, or an admitted run), not on internal wiring.
// The fixtures are decoded through Viper's mapstructure exactly as serve loads
// them, so "purely from config" is proven by construction: no test builds a
// HookBinding by hand.

// -- config decode + fakes --------------------------------------------------

// hooksFromYAML decodes a `tools.hooks`-shaped YAML body into HooksConfig
// through the same Viper mapstructure path serve uses, so a fixture proves the
// config schema unmarshals, not just that the Go struct can be populated.
func hooksFromYAML(t *testing.T, body string) HooksConfig {
	t.Helper()
	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(body)))
	var cfg HooksConfig
	require.NoError(t, v.Unmarshal(&cfg))
	return cfg
}

// fakeApprovedSet is an in-memory ApprovedSetAccessor: a gated-allowlist admits
// a governed write only when its call identity is present here.
type fakeApprovedSet struct {
	allowed map[string]map[CallIdentity]bool
}

func (f fakeApprovedSet) Record(ctx context.Context, stateKey string, ids []CallIdentity) error {
	return nil
}

func (f fakeApprovedSet) ForgetSession(sessionID string) {}

func (f fakeApprovedSet) Contains(ctx context.Context, stateKey string, id CallIdentity) (bool, error) {
	set, ok := f.allowed[stateKey]
	if !ok {
		return false, nil
	}
	return set[id], nil
}

// stubCustomRegistry resolves custom binding names to constructors, standing in
// for the D-8 registry so the "custom" kind's build path is exercised.
type stubCustomRegistry struct {
	ctors map[string]func(HookBinding) (Hook, error)
}

func (r stubCustomRegistry) lookup(name string) (func(HookBinding) (Hook, error), bool) {
	c, ok := r.ctors[name]
	return c, ok
}

// registerDeny is a custom-hook constructor that denies every call to its
// binding's scope — enough to prove a registered custom name builds and lands
// in the chain with the binding's selection applied.
func registerDeny(b HookBinding) (Hook, error) {
	scope := NewToolScope(b.Scope)
	return fixedHook{
		match:    func(req AdmissionRequest) bool { return scope.MatchesTool(req.ToolName) },
		decision: Decision{Kind: Deny, Reason: "denied by custom hook"},
	}, nil
}

// execFor builds an executor with the given tools registered and the chain
// attached, ready to drive Execute.
func execFor(chain *Chain, tools ...*MockTool) *Executor {
	reg := NewRegistry()
	for _, tl := range tools {
		reg.Register(tl)
	}
	exec := NewExecutor(reg)
	exec.SetAdmissionChain(chain)
	return exec
}

// -- AC1: a hook enabled in a tools.hooks fixture takes effect, and the same
// effect is reachable by editing config alone (no source change) -------------

func TestBuildChain_AC1_ConfigEnablesHook_AndConfigAloneFlipsEffect(t *testing.T) {
	// The SAME code path and the SAME call; only the config text differs between
	// the two runs. A binding present ⇒ denied; the binding removed ⇒ admitted.
	const withHook = `
hooks:
  - kind: denylist
    scope: sql_execute
    matcher:
      param_path: sql
      op: contains
      value: DROP
`
	const withoutHook = `
hooks: []
`
	call := map[string]interface{}{"sql": "DROP TABLE users"}

	// Config that enables the denylist: the governed call is blocked at serve
	// with no source change.
	chainOn, err := BuildChainFromConfig(hooksFromYAML(t, withHook), ChainDeps{})
	require.NoError(t, err)
	toolOn := countingTool("sql_execute")
	resOn, err := execFor(chainOn, toolOn).Execute(context.Background(), "sql_execute", call)
	require.NoError(t, err)
	require.Equal(t, "permission_denied", resOn.Error.Code, "the config-enabled hook denies the call")
	require.Equal(t, 0, toolOn.ExecuteCount)

	// Editing config alone (dropping the binding) reaches the opposite effect:
	// the identical call now runs.
	chainOff, err := BuildChainFromConfig(hooksFromYAML(t, withoutHook), ChainDeps{})
	require.NoError(t, err)
	toolOff := countingTool("sql_execute")
	resOff, err := execFor(chainOff, toolOff).Execute(context.Background(), "sql_execute", call)
	require.NoError(t, err)
	require.True(t, resOff.Success, "with the binding removed from config, the same call is admitted")
	require.Equal(t, 1, toolOff.ExecuteCount)
}

// -- AC2: each library policy (gated-allowlist, denylist, audit, ask) is
// enable-able and parameterizable — scope, matcher, and its own params — purely
// from config

func TestBuildChain_AC2_EachPolicyParameterizedFromConfig(t *testing.T) {
	const fixture = `
hooks:
  - kind: gated-allowlist
    scope: sql_execute
    matcher: {}
    state_key: approved_grants
    source_tool: sql_render
    read_pattern: "^SELECT"
    stmt_param: sql
  - kind: denylist
    scope: shell
    matcher:
      param_path: cmd
      op: contains
      value: "rm -rf"
  - kind: audit
    scope: file_write
    matcher: {}
  - kind: ask
    scope: admin_grant
    matcher:
      param_path: sql
      op: contains
      value: GRANT
`
	approvedGrant := "GRANT SELECT ON t TO alice"
	deps := ChainDeps{
		ApprovedSet: fakeApprovedSet{allowed: map[string]map[CallIdentity]bool{
			"approved_grants": {CallIdentity(approvedGrant): true},
		}},
		// A resolver that refuses: the ask binding's held call surfaces its
		// refusal, which is what makes the binding's selection observable.
		Ask: resolveTo{decision: Decision{Kind: Deny, Reason: "ask resolver refused"}},
	}

	chain, err := BuildChainFromConfig(hooksFromYAML(t, fixture), deps)
	require.NoError(t, err)

	// gated-allowlist: the match-all matcher governs every sql_execute call, so
	// read_pattern "^SELECT" is the determinative exemption — it admits a read
	// with no approved entry. This proves read_pattern (and the stmt_param it
	// reads) are applied from config: absent or wrong, this read would fall
	// through to the empty approved set and be denied.
	t.Run("gated-allowlist read_pattern admits a read", func(t *testing.T) {
		tool := countingTool("sql_execute")
		res, err := execFor(chain, tool).Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": "SELECT 1"})
		require.NoError(t, err)
		require.True(t, res.Success)
		require.Equal(t, 1, tool.ExecuteCount)
	})

	// gated-allowlist: a write is not exempted by read_pattern, so it is gated;
	// one whose identity is in the approved set at state_key is admitted
	// (proves state_key + stmt_param threaded).
	t.Run("gated-allowlist admits an approved write", func(t *testing.T) {
		tool := countingTool("sql_execute")
		res, err := execFor(chain, tool).Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": approvedGrant})
		require.NoError(t, err)
		require.True(t, res.Success)
		require.Equal(t, 1, tool.ExecuteCount)
	})

	// gated-allowlist: a gated write absent from the approved set is denied.
	t.Run("gated-allowlist denies an unapproved write", func(t *testing.T) {
		tool := countingTool("sql_execute")
		res, err := execFor(chain, tool).Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": "GRANT ALL ON t TO mallory"})
		require.NoError(t, err)
		require.Equal(t, "permission_denied", res.Error.Code)
		require.Equal(t, "call not in approved set", res.Error.Message)
		require.Equal(t, 0, tool.ExecuteCount)
	})

	// denylist: its pattern (matcher value) denies a matching call.
	t.Run("denylist denies its configured pattern", func(t *testing.T) {
		tool := countingTool("shell")
		res, err := execFor(chain, tool).Execute(context.Background(), "shell", map[string]interface{}{"cmd": "rm -rf /"})
		require.NoError(t, err)
		require.Equal(t, "permission_denied", res.Error.Code)
		require.Equal(t, 0, tool.ExecuteCount)
	})

	// audit: an audit binding built from config is scoped to file_write (its
	// empty matcher governs every call to that tool). Audit observes, never
	// gates, so the scoped call is admitted and runs; a call to a tool outside
	// its scope is left unaffected — proving the policy is enabled and scoped
	// purely from config.
	t.Run("audit is scoped and admits its call", func(t *testing.T) {
		fileTool := countingTool("file_write")
		res, err := execFor(chain, fileTool).Execute(context.Background(), "file_write", map[string]interface{}{"path": "/tmp/x"})
		require.NoError(t, err)
		require.True(t, res.Success, "audit observes but does not gate: the scoped call runs")
		require.Equal(t, 1, fileTool.ExecuteCount)

		otherTool := countingTool("other_tool")
		res2, err := execFor(chain, otherTool).Execute(context.Background(), "other_tool", map[string]interface{}{"path": "/tmp/y"})
		require.NoError(t, err)
		require.True(t, res2.Success, "a tool outside the audit scope is unaffected")
		require.Equal(t, 1, otherTool.ExecuteCount)
	})

	// ask: an ask binding built from config is scoped to admin_grant and
	// parameterized by its matcher over "sql". A call meeting both is held for
	// the resolver — which refuses, so the refusal reaches the caller and the
	// body never runs. A call to the same tool whose sql misses the matcher is
	// not governed and runs, proving scope and matcher both came from config.
	t.Run("ask holds its configured selection for the resolver", func(t *testing.T) {
		heldTool := countingTool("admin_grant")
		res, err := execFor(chain, heldTool).Execute(context.Background(), "admin_grant", map[string]interface{}{"sql": "GRANT ROLE admin TO alice"})
		require.NoError(t, err)
		require.Equal(t, "permission_denied", res.Error.Code)
		require.Equal(t, "ask resolver refused", res.Error.Message)
		require.Equal(t, 0, heldTool.ExecuteCount)

		missTool := countingTool("admin_grant")
		res2, err := execFor(chain, missTool).Execute(context.Background(), "admin_grant", map[string]interface{}{"sql": "SELECT 1"})
		require.NoError(t, err)
		require.True(t, res2.Success, "a call whose sql misses the ask matcher is unaffected")
		require.Equal(t, 1, missTool.ExecuteCount)
	})
}

// -- AC3: scope T + matcher M applies only to matching calls; a non-match, or a
// call to a different tool, is left unaffected — incl. the "matcher selects no
// calls" edge -----------------------------------------------------------------

func TestBuildChain_AC3_ScopeAndMatcherTargetOnlyMatchingCalls(t *testing.T) {
	const fixture = `
hooks:
  - kind: denylist
    scope: sql_execute
    matcher:
      param_path: sql
      op: contains
      value: DROP
`
	chain, err := BuildChainFromConfig(hooksFromYAML(t, fixture), ChainDeps{})
	require.NoError(t, err)

	// In scope T and meeting matcher M: the decision applies.
	t.Run("in scope and matching is denied", func(t *testing.T) {
		tool := countingTool("sql_execute")
		res, err := execFor(chain, tool).Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": "DROP TABLE t"})
		require.NoError(t, err)
		require.Equal(t, "permission_denied", res.Error.Code)
		require.Equal(t, 0, tool.ExecuteCount)
	})

	// In scope T but NOT meeting M: unaffected (runs, no admission metadata).
	t.Run("in scope but not matching is unaffected", func(t *testing.T) {
		tool := countingTool("sql_execute")
		res, err := execFor(chain, tool).Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": "SELECT 1"})
		require.NoError(t, err)
		require.True(t, res.Success)
		require.Equal(t, 1, tool.ExecuteCount)
		require.NotContains(t, res.Metadata, "admission.decision")
	})

	// A call to a different tool: out of scope, unaffected even with a DROP.
	t.Run("out of scope tool is unaffected", func(t *testing.T) {
		tool := countingTool("shell")
		res, err := execFor(chain, tool).Execute(context.Background(), "shell", map[string]interface{}{"sql": "DROP TABLE t"})
		require.NoError(t, err)
		require.True(t, res.Success)
		require.Equal(t, 1, tool.ExecuteCount)
	})

	// Edge "matcher selects no calls": a binding whose matcher matches nothing
	// has no effect — the governed tool's call is admitted like the no-chain
	// baseline.
	t.Run("matcher selects no calls => policy has no effect", func(t *testing.T) {
		const noMatch = `
hooks:
  - kind: denylist
    scope: sql_execute
    matcher:
      param_path: sql
      op: contains
      value: THIS_TOKEN_NEVER_APPEARS
`
		deadChain, err := BuildChainFromConfig(hooksFromYAML(t, noMatch), ChainDeps{})
		require.NoError(t, err)
		tool := countingTool("sql_execute")
		res, err := execFor(deadChain, tool).Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": "DROP TABLE t"})
		require.NoError(t, err)
		require.True(t, res.Success, "a matcher that selects no calls leaves the call admitted")
		require.Equal(t, 1, tool.ExecuteCount)
		require.NotContains(t, res.Metadata, "admission.decision")
	})
}

// -- AC4: a hook scoped to a dispatch tool with a matcher over its CARRIED
// params denies a governed op identically to the directly-named tool ----------

func TestBuildChain_AC4_DispatchCarriedParamDeniedIdenticallyToDirect(t *testing.T) {
	// dispatch carries the inner op nested under "params"; the direct tool takes
	// the op at top level. A binding per path (no auto-unwrap) reaches the same
	// deny for the same self-GRANT.
	const fixture = `
hooks:
  - kind: denylist
    scope: dispatch
    matcher:
      param_path: params.sql
      op: regex
      value: "(?i)GRANT .* TO CURRENT_USER"
  - kind: denylist
    scope: sql_execute
    matcher:
      param_path: sql
      op: regex
      value: "(?i)GRANT .* TO CURRENT_USER"
`
	chain, err := BuildChainFromConfig(hooksFromYAML(t, fixture), ChainDeps{})
	require.NoError(t, err)

	selfGrant := "GRANT ROLE admin TO CURRENT_USER"

	// Through the dispatch/passthrough tool: the matcher traverses the carried
	// op and denies it.
	dispatchTool := countingTool("dispatch")
	dispatchRes, err := execFor(chain, dispatchTool).Execute(context.Background(), "dispatch", map[string]interface{}{
		"tool":   "sql_execute",
		"params": map[string]interface{}{"sql": selfGrant},
	})
	require.NoError(t, err)
	require.Equal(t, "permission_denied", dispatchRes.Error.Code, "carried self-GRANT via dispatch is denied")
	require.Equal(t, 0, dispatchTool.ExecuteCount)

	// Through the directly-named execute tool: the same op is denied identically.
	directTool := countingTool("sql_execute")
	directRes, err := execFor(chain, directTool).Execute(context.Background(), "sql_execute", map[string]interface{}{"sql": selfGrant})
	require.NoError(t, err)
	require.Equal(t, "permission_denied", directRes.Error.Code, "the same self-GRANT via the direct tool is denied")
	require.Equal(t, 0, directTool.ExecuteCount)

	require.Equal(t, directRes.Error.Code, dispatchRes.Error.Code, "dispatch and direct reach an identical decision")
}

// -- AC5 (config-obligation half): with NO hook scoped to the dispatch path, the
// same carried call is admitted — an uncovered path re-opens the hole ----------

func TestBuildChain_AC5_UnscopedDispatchPathIsAdmitted(t *testing.T) {
	// Only the direct tool is governed; the dispatch path is left unbound. The
	// carried self-GRANT slips through — demonstrating path-completeness is a
	// config obligation, not an automatic property.
	const directOnly = `
hooks:
  - kind: denylist
    scope: sql_execute
    matcher:
      param_path: sql
      op: regex
      value: "(?i)GRANT .* TO CURRENT_USER"
`
	chain, err := BuildChainFromConfig(hooksFromYAML(t, directOnly), ChainDeps{})
	require.NoError(t, err)

	dispatchTool := countingTool("dispatch")
	res, err := execFor(chain, dispatchTool).Execute(context.Background(), "dispatch", map[string]interface{}{
		"tool":   "sql_execute",
		"params": map[string]interface{}{"sql": "GRANT ROLE admin TO CURRENT_USER"},
	})
	require.NoError(t, err)
	require.True(t, res.Success, "with no binding on the dispatch path the carried self-GRANT is admitted")
	require.Equal(t, 1, dispatchTool.ExecuteCount)
}

// -- AC6 (builder fail-closed half): a malformed/unresolvable binding is a build
// error — never a silent empty chain -----------------------------------------

func TestBuildChain_AC6_MalformedBindingFailsClosed(t *testing.T) {
	valid := ChainDeps{
		ApprovedSet: fakeApprovedSet{},
		Custom:      stubCustomRegistry{ctors: map[string]func(HookBinding) (Hook, error){"clearance": registerDeny}},
	}

	cases := []struct {
		name string
		yaml string
		deps ChainDeps
	}{
		{
			name: "unknown kind",
			yaml: `
hooks:
  - kind: bogus
    scope: sql_execute
`,
			deps: valid,
		},
		{
			name: "empty scope",
			yaml: `
hooks:
  - kind: denylist
    scope: ""
`,
			deps: valid,
		},
		{
			name: "bad matcher regex",
			yaml: `
hooks:
  - kind: denylist
    scope: sql_execute
    matcher:
      param_path: sql
      op: regex
      value: "("
`,
			deps: valid,
		},
		{
			name: "gated-allowlist missing state_key",
			yaml: `
hooks:
  - kind: gated-allowlist
    scope: sql_execute
    source_tool: sql_render
`,
			deps: valid,
		},
		{
			name: "gated-allowlist missing source_tool",
			yaml: `
hooks:
  - kind: gated-allowlist
    scope: sql_execute
    state_key: approved_grants
`,
			deps: valid,
		},
		{
			name: "gated-allowlist bad read_pattern",
			yaml: `
hooks:
  - kind: gated-allowlist
    scope: sql_execute
    state_key: approved_grants
    source_tool: sql_render
    read_pattern: "("
`,
			deps: valid,
		},
		{
			name: "custom missing name",
			yaml: `
hooks:
  - kind: custom
    scope: sql_execute
`,
			deps: valid,
		},
		{
			name: "custom name not registered",
			yaml: `
hooks:
  - kind: custom
    scope: sql_execute
    name: not_a_real_hook
`,
			deps: valid,
		},
		{
			name: "custom named but no registry configured",
			yaml: `
hooks:
  - kind: custom
    scope: sql_execute
    name: clearance
`,
			deps: ChainDeps{}, // Custom registry deliberately absent
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chain, err := BuildChainFromConfig(hooksFromYAML(t, tc.yaml), tc.deps)
			require.Error(t, err, "a malformed binding must fail the build, never yield a silent chain")
			require.Nil(t, chain, "no chain is returned on a build error")
		})
	}
}

// A well-formed binding of every kind builds without error — the control that
// proves AC6's errors are specific to malformed input, not a build that always
// fails. The registered custom name resolves through the registry.
func TestBuildChain_AC6_WellFormedBindingsBuild(t *testing.T) {
	const fixture = `
hooks:
  - kind: denylist
    scope: shell
    matcher:
      param_path: cmd
      op: contains
      value: "rm -rf"
  - kind: gated-allowlist
    scope: sql_execute
    matcher:
      param_path: sql
      op: regex
      value: "^GRANT"
    state_key: approved_grants
    source_tool: sql_render
    stmt_param: sql
  - kind: audit
    scope: file_write
    matcher: {}
  - kind: ask
    scope: admin_grant
    matcher:
      param_path: sql
      op: contains
      value: GRANT
  - kind: custom
    scope: admin_tool
    name: clearance
`
	deps := ChainDeps{
		ApprovedSet: fakeApprovedSet{},
		Custom:      stubCustomRegistry{ctors: map[string]func(HookBinding) (Hook, error){"clearance": registerDeny}},
	}
	chain, err := BuildChainFromConfig(hooksFromYAML(t, fixture), deps)
	require.NoError(t, err)
	require.NotNil(t, chain)

	// The registered custom hook is live in the built chain: its scope denies.
	tool := countingTool("admin_tool")
	res, err := execFor(chain, tool).Execute(context.Background(), "admin_tool", map[string]interface{}{"op": "x"})
	require.NoError(t, err)
	require.Equal(t, "permission_denied", res.Error.Code)
	require.Equal(t, 0, tool.ExecuteCount)
}
