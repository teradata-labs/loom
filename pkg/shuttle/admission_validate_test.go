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
	"testing"

	"github.com/stretchr/testify/require"
)

// D-4 acceptance tests for ValidateHooksConfig — the CT-4 producer-owned,
// deps-free config validity check a host calls at save time (before any live
// ChainDeps exists) — followed by D-14's for the `ask` kind. Each case decodes a
// `tools.hooks` fixture through the same Viper mapstructure path serve uses
// (hooksFromYAML, defined in admission_config_test.go) and drives a real
// exported entry point, so the assertion is on that function's own return, not
// on internal wiring: ValidateHooksConfig throughout, plus BuildChainFromConfig
// for the unknown-kind message, which both doors emit.

// -- AC1: a well-formed binding of every known Kind validates clean (nil). ----

// gated-allowlist (scope+state_key+source_tool), denylist, audit, and ask are
// fully validated at save time; a custom binding validates on non-empty Name
// alone — its registry membership is a build-time check, not a save-time one, so
// this deps-free validator has no runtime registry to consult. The ask kind
// carries no kind-specific fields, so scope plus a compilable matcher is the
// whole of its validity.
func TestValidateHooksConfig_AC1_WellFormedEachKind_ReturnsNil(t *testing.T) {
	const fixture = `
hooks:
  - kind: gated-allowlist
    scope: sql_execute
    matcher:
      param_path: sql
      op: regex
      value: "^GRANT"
    state_key: approved_grants
    source_tool: sql_render
    stmt_param: sql
    read_pattern: "^SELECT"
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
    scope: sql_execute
    matcher:
      param_path: sql
      op: contains
      value: GRANT
  - kind: custom
    scope: admin_tool
    name: clearance
`
	require.NoError(t, ValidateHooksConfig(hooksFromYAML(t, fixture)),
		"a well-formed binding of each known Kind is structurally valid")
}

// A custom binding whose Name matches no registered constructor still validates
// clean: ValidateHooksConfig is deps-free, so registry membership is deferred to
// build (an unregistered name fails closed there, not here). This pins the
// save-time/build-time boundary the contract draws.
func TestValidateHooksConfig_AC1_CustomUnregisteredName_ReturnsNil(t *testing.T) {
	const fixture = `
hooks:
  - kind: custom
    scope: admin_tool
    name: never_registered_anywhere
`
	require.NoError(t, ValidateHooksConfig(hooksFromYAML(t, fixture)),
		"a custom binding with a non-empty name validates at save time regardless of registry membership")
}

// -- AC2: an unknown Kind is a structured error wrapped with the offending
// binding's index and kind, matching BuildChainFromConfig's style. ------------

func TestValidateHooksConfig_AC2_UnknownKind_StructuredError(t *testing.T) {
	// A well-formed denylist precedes the offending binding so the reported index
	// must be the real position (1), not a hardcoded 0.
	const fixture = `
hooks:
  - kind: denylist
    scope: sql_execute
    matcher:
      param_path: sql
      op: contains
      value: DROP
  - kind: bogus
    scope: sql_execute
`
	err := ValidateHooksConfig(hooksFromYAML(t, fixture))
	require.Error(t, err, "an unknown Kind is rejected")
	require.Contains(t, err.Error(), `tools.hooks[1]`, "the error names the offending binding's index")
	require.Contains(t, err.Error(), `kind "bogus"`, "the error names the offending Kind")
}

// -- AC3: a gated-allowlist missing a Kind-required field, and an empty scope,
// are each a structured error naming the offending binding's index and Kind. --

func TestValidateHooksConfig_AC3_GatedAllowlistMissingRequiredField_StructuredError(t *testing.T) {
	// state_key and source_tool are each required by the gated-allowlist Kind;
	// omitting either (with the other present) is rejected on its own.
	cases := []struct {
		name  string
		yaml  string
		field string
	}{
		{
			name: "missing state_key",
			yaml: `
hooks:
  - kind: gated-allowlist
    scope: sql_execute
    source_tool: sql_render
`,
			field: "state_key",
		},
		{
			name: "missing source_tool",
			yaml: `
hooks:
  - kind: gated-allowlist
    scope: sql_execute
    state_key: approved_grants
`,
			field: "source_tool",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateHooksConfig(hooksFromYAML(t, tc.yaml))
			require.Error(t, err, "a gated-allowlist missing %s is rejected", tc.field)
			require.Contains(t, err.Error(), `tools.hooks[0]`, "the error names the offending binding's index")
			require.Contains(t, err.Error(), `kind "gated-allowlist"`, "the error names the offending Kind")
			require.Contains(t, err.Error(), tc.field, "the error names the missing required field")
		})
	}
}

func TestValidateHooksConfig_AC3_EmptyScope_StructuredError(t *testing.T) {
	const fixture = `
hooks:
  - kind: denylist
    scope: ""
`
	err := ValidateHooksConfig(hooksFromYAML(t, fixture))
	require.Error(t, err, "a binding with an empty scope is rejected")
	require.Contains(t, err.Error(), `tools.hooks[0]`, "the error names the offending binding's index")
	require.Contains(t, err.Error(), "scope", "the error identifies the missing scope")
}

// -- D-14 AC3: a malformed `ask` binding is rejected with the same structured
// error shape its siblings get — the offending binding's index and Kind. ------

func TestValidateHooksConfig_D14_AC3_MalformedAskBinding_StructuredError(t *testing.T) {
	cases := []struct {
		name  string
		yaml  string
		fault string
	}{
		{
			name: "empty scope",
			yaml: `
hooks:
  - kind: denylist
    scope: shell
    matcher: {}
  - kind: ask
    scope: ""
    matcher:
      param_path: sql
      op: contains
      value: GRANT
`,
			fault: "scope is required",
		},
		{
			name: "uncompilable matcher",
			yaml: `
hooks:
  - kind: denylist
    scope: shell
    matcher: {}
  - kind: ask
    scope: sql_execute
    matcher:
      param_path: sql
      op: regex
      value: "("
`,
			fault: "invalid regex",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A well-formed denylist precedes the offending ask binding, so the
			// reported index must be the real position (1), not a hardcoded 0.
			err := ValidateHooksConfig(hooksFromYAML(t, tc.yaml))
			require.Error(t, err, "an ask binding with %s is rejected", tc.name)
			require.Contains(t, err.Error(), `tools.hooks[1]`, "the error names the offending binding's index")
			require.Contains(t, err.Error(), `kind "ask"`, "the error names the offending Kind")
			require.Contains(t, err.Error(), tc.fault, "the error identifies the fault")
		})
	}
}

// -- D-14 AC4: an unknown kind is still rejected, and the error lists `ask`
// among the valid kinds — on both the save-time and the build-time door. ------

func TestUnknownKind_D14_AC4_ErrorNamesAskAmongValidKinds(t *testing.T) {
	const fixture = `
hooks:
  - kind: not-a-real-kind
    scope: sql_execute
`
	cfg := hooksFromYAML(t, fixture)

	// The offending kind is "not-a-real-kind", which contains no "ask", so the
	// substring can only come from the message's valid-kinds list.
	t.Run("ValidateHooksConfig", func(t *testing.T) {
		err := ValidateHooksConfig(cfg)
		require.Error(t, err, "an unknown Kind is still rejected at save time")
		require.Contains(t, err.Error(), "unknown kind", "the save-time error reports the unknown kind")
		require.Contains(t, err.Error(), "ask", "the save-time error lists ask among the valid kinds")
	})

	t.Run("BuildChainFromConfig", func(t *testing.T) {
		chain, err := BuildChainFromConfig(cfg, ChainDeps{})
		require.Error(t, err, "an unknown Kind is still rejected at build time")
		require.Nil(t, chain, "no chain is returned on a build error")
		require.Contains(t, err.Error(), "unknown kind", "the build-time error reports the unknown kind")
		require.Contains(t, err.Error(), "ask", "the build-time error lists ask among the valid kinds")
	})
}

// with a registry supplied, an unknown custom-hook name fails at
// validation (the save door), not at the next session build.
func TestValidateWithRegistry_UnknownCustomName(t *testing.T) {
	cfg := HooksConfig{Bindings: []HookBinding{{Kind: "custom", Scope: "x", Name: "no-such-hook"}}}
	require.NoError(t, ValidateHooksConfig(cfg), "deps-free validation stays lenient")
	err := ValidateHooksConfigWithRegistry(cfg, stubCustomRegistry{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not registered")
}
