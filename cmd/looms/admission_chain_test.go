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
package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// D-2 component tests for createAdmissionChain — the cmd/looms leg the pure
// builder-unit cannot observe: the per-binding coverage log (L-Cfg-2) and the
// builder error surfaced through serve's chain constructor (L-Cfg-1). The
// serve-process abort on that error is the integration leg; here the assertion
// is on the returned error and the captured log lines.

// observedLogger returns a zap logger whose entries are captured for assertion.
func observedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.InfoLevel)
	return zap.New(core), logs
}

// hooksConfig wraps bindings into a *Config with a `tools.hooks` block set, as
// serve holds it after unmarshalling.
func hooksConfig(bindings ...shuttle.HookBinding) *Config {
	return &Config{Tools: ToolsConfig{Hooks: shuttle.HooksConfig{Bindings: bindings}}}
}

// -- AC5: serve logs exactly one coverage line per built binding so an uncovered
// write/dispatch path is detectable ------------------------------------------

func TestCreateAdmissionChain_AC5_LogsOneCoverageLinePerBinding(t *testing.T) {
	config := hooksConfig(
		shuttle.HookBinding{
			Kind:    "denylist",
			Scope:   "sql_execute",
			Matcher: shuttle.MatcherSpec{ParamPath: "sql", Op: "contains", Value: "DROP"},
		},
		shuttle.HookBinding{
			Kind:  "audit",
			Scope: "file_write",
			// Empty matcher governs every call to the scoped tool: logged as "*".
		},
	)

	logger, logs := observedLogger()
	chain, err := createAdmissionChain(config, shuttle.ChainDeps{}, logger)
	require.NoError(t, err)
	require.NotNil(t, chain)

	lines := logs.FilterMessage("Admission hook binding").All()
	require.Len(t, lines, 2, "exactly one coverage line per built binding")

	first := lines[0].ContextMap()
	require.Equal(t, int64(0), first["index"])
	require.Equal(t, "denylist", first["kind"])
	require.Equal(t, "sql_execute", first["scope"])
	require.Equal(t, `sql contains "DROP"`, first["matcher"])

	second := lines[1].ContextMap()
	require.Equal(t, int64(1), second["index"])
	require.Equal(t, "audit", second["kind"])
	require.Equal(t, "file_write", second["scope"])
	require.Equal(t, "*", second["matcher"], "an empty matcher governs every call and is shown as *")
}

// -- AC6: a malformed/unresolvable binding fails the chain build (fail-closed);
// serve aborts on this error rather than run silently ungoverned --------------

func TestCreateAdmissionChain_AC6_MalformedBindingReturnsError(t *testing.T) {
	cases := []struct {
		name    string
		binding shuttle.HookBinding
	}{
		{"unknown kind", shuttle.HookBinding{Kind: "bogus", Scope: "sql_execute"}},
		{"empty scope", shuttle.HookBinding{Kind: "denylist", Scope: ""}},
		{"bad matcher regex", shuttle.HookBinding{Kind: "denylist", Scope: "sql_execute", Matcher: shuttle.MatcherSpec{ParamPath: "sql", Op: "regex", Value: "("}}},
		{"gated-allowlist missing state_key", shuttle.HookBinding{Kind: "gated-allowlist", Scope: "sql_execute", SourceTool: "sql_render"}},
		{"custom name not registered", shuttle.HookBinding{Kind: "custom", Scope: "sql_execute", Name: "not_a_real_hook"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, _ := observedLogger()
			chain, err := createAdmissionChain(hooksConfig(tc.binding), shuttle.ChainDeps{}, logger)
			require.Error(t, err, "serve's chain build must fail on a malformed binding, never return a silent chain")
			require.Nil(t, chain)
		})
	}
}

// -- Blast radius (additive change stays green): the new Hooks field and chain
// constructor must not disturb configs that predate tools.hooks ---------------

// With no permission checker and no bindings, the chain is a pure pass-through
// (nil) and nothing is logged — an absent tools.hooks governs nothing.
func TestCreateAdmissionChain_NoPermNoHooks_IsPassThrough(t *testing.T) {
	logger, logs := observedLogger()
	chain, err := createAdmissionChain(hooksConfig(), shuttle.ChainDeps{}, logger)
	require.NoError(t, err)
	require.Nil(t, chain, "no checker and no bindings => nil chain (pass-through)")
	require.Empty(t, logs.FilterMessage("Admission hook binding").All())
}

// A permission checker with no tools.hooks still yields a chain (the name-level
// permHook), so existing tools.permissions configs keep governing after the
// additive change; no binding coverage lines are logged.
func TestCreateAdmissionChain_PermOnly_BuildsChainWithoutBindings(t *testing.T) {
	pc := shuttle.NewPermissionChecker(shuttle.PermissionConfig{RequireApproval: true, DefaultAction: "deny"})
	logger, logs := observedLogger()
	chain, err := createAdmissionChain(hooksConfig(), shuttle.ChainDeps{Perm: pc}, logger)
	require.NoError(t, err)
	require.NotNil(t, chain, "a permission checker alone still produces a governing chain")
	require.Empty(t, logs.FilterMessage("Admission hook binding").All())
}
