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
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// D-8 acceptance tests for the build-time custom-hook registration entrypoint
// (§5.3, the escape hatch). Each case drives D-8's REAL write+resolve seam —
// RegisterCustomHook writing the process-global registry and
// ProcessCustomHookRegistry() resolving it as ChainDeps.Custom — not the D-2
// stubCustomRegistry double. The registry has no unregister, so registrations
// leak across a package run; every test here uses a per-test-unique name so the
// global stays deterministic under a full-package run.

// -- custom-hook constructors (a deployment's compiled-in Go decision function) --

// d8ScopedDenyCtor builds a custom hook that denies every call to the binding's
// scope. It is a compiled-in Go func value — the shape a deployment contributes
// through RegisterCustomHook, never interpreted at runtime.
func d8ScopedDenyCtor(b HookBinding) (Hook, error) {
	scope := NewToolScope(b.Scope)
	return fixedHook{
		match:    func(req AdmissionRequest) bool { return scope.MatchesTool(req.ToolName) },
		decision: Decision{Kind: Deny, Reason: "denied by custom clearance hook"},
	}, nil
}

// d8ScopedAllowCtor builds a custom hook that allows every call to the binding's
// scope, the allow counterpart to d8ScopedDenyCtor.
func d8ScopedAllowCtor(b HookBinding) (Hook, error) {
	scope := NewToolScope(b.Scope)
	return fixedHook{
		match:    func(req AdmissionRequest) bool { return scope.MatchesTool(req.ToolName) },
		decision: Decision{Kind: Allow},
	}, nil
}

// -- AC1: a custom (compiled Go) hook registered via the build-time entrypoint
// governs its scoped tool — a call it denies does not run, a call it allows runs --

func TestCustomHook_AC1_RegisteredHookGovernsScopedTool(t *testing.T) {
	// Per-test-unique names: the process registry has no unregister, so a shared
	// name would leak into other tests in the package run.
	const denyName = "d8-ac1-clearance-deny"
	const allowName = "d8-ac1-clearance-allow"

	// A deployment's compiled-in init contributes the decision functions.
	RegisterCustomHook(denyName, d8ScopedDenyCtor)
	RegisterCustomHook(allowName, d8ScopedAllowCtor)

	// tools.hooks config names the already-registered ctors (name + scope only);
	// resolution goes through the real process registry wired at serve.
	fixture := `
hooks:
  - kind: custom
    scope: d8_secret_read
    name: ` + denyName + `
  - kind: custom
    scope: d8_public_read
    name: ` + allowName + `
`
	chain, err := BuildChainFromConfig(hooksFromYAML(t, fixture), ChainDeps{Custom: ProcessCustomHookRegistry()})
	require.NoError(t, err)
	require.NotNil(t, chain)

	// The tool its custom hook denies: permission_denied and the body never runs.
	denied := countingTool("d8_secret_read")
	resDeny, err := execFor(chain, denied).Execute(context.Background(), "d8_secret_read", map[string]interface{}{"k": "v"})
	require.NoError(t, err, "a policy deny is a Result, not a transport error")
	require.NotNil(t, resDeny.Error)
	require.Equal(t, "permission_denied", resDeny.Error.Code)
	require.Equal(t, 0, denied.ExecuteCount, "a call the custom hook denies must not run")

	// The tool its custom hook allows: the body runs exactly once.
	allowed := countingTool("d8_public_read")
	resAllow, err := execFor(chain, allowed).Execute(context.Background(), "d8_public_read", map[string]interface{}{"k": "v"})
	require.NoError(t, err)
	require.True(t, resAllow.Success)
	require.Nil(t, resAllow.Error)
	require.Equal(t, 1, allowed.ExecuteCount, "a call the custom hook allows runs")
}

// -- AC2 (schema half): the config schema binds no arbitrary Go — every
// HookBinding field is pure string data, so a binding cannot carry or reference
// a compiled decision function -----------------------------------------------

func TestCustomHook_AC2_ConfigSchemaBindsNoGo(t *testing.T) {
	bt := reflect.TypeOf(HookBinding{})
	for i := 0; i < bt.NumField(); i++ {
		f := bt.Field(i)
		switch f.Type.Kind() {
		case reflect.String:
			// A string field carries data only; it cannot hold a Go func value.
		case reflect.Struct:
			// The sole non-string field is the matcher spec, itself string-only —
			// param selection is data, not code.
			require.Equal(t, reflect.TypeOf(MatcherSpec{}), f.Type,
				"the only non-string HookBinding field is the matcher spec")
			for j := 0; j < f.Type.NumField(); j++ {
				mf := f.Type.Field(j)
				require.Equal(t, reflect.String, mf.Type.Kind(),
					"MatcherSpec.%s must be a string so the matcher binds no Go", mf.Name)
			}
		default:
			t.Fatalf("HookBinding.%s has kind %s; the config schema must be pure string data so it binds no Go",
				f.Name, f.Type.Kind())
		}
	}
}

// -- AC2 (fail-closed half): a kind:custom binding whose Name was never
// RegisterCustomHook'd fails the build with a nil chain — the only route from a
// config name to executable behavior is a compiled-in Go ctor ------------------

func TestCustomHook_AC2_UnregisteredNameFailsClosed(t *testing.T) {
	const neverRegistered = "d8-ac2-name-never-registered"

	// Precondition: the name is genuinely absent from the process registry, so
	// the failure below is the lookup miss, not a stale registration.
	_, ok := processCustomRegistry{}.lookup(neverRegistered)
	require.False(t, ok, "precondition: the name was never RegisterCustomHook'd")

	fixture := `
hooks:
  - kind: custom
    scope: d8_secret_read
    name: ` + neverRegistered + `
`
	chain, err := BuildChainFromConfig(hooksFromYAML(t, fixture), ChainDeps{Custom: ProcessCustomHookRegistry()})
	require.Error(t, err, "a custom name with no compiled-in ctor must fail the build (fail-closed)")
	require.Nil(t, chain, "no chain is returned; config alone cannot bind the custom decision")
	require.Contains(t, err.Error(), neverRegistered)
	require.Contains(t, err.Error(), "not registered")
}

// -- AC3: the registration entrypoint takes a compiled-in Go func value, not a
// script/path/plugin file — nothing is interpreted at runtime and no sandbox is
// used -------------------------------------------------------------------------

func TestCustomHook_AC3_EntrypointTakesCompiledGoFunc(t *testing.T) {
	regType := reflect.TypeOf(RegisterCustomHook)
	require.Equal(t, reflect.Func, regType.Kind())
	require.Equal(t, 2, regType.NumIn(), "RegisterCustomHook(name, ctor)")
	require.Equal(t, reflect.String, regType.In(0).Kind(), "first arg is the registry name")

	// The second arg is a Go func value — a path/script/plugin string would be
	// reflect.String, not reflect.Func.
	ctorType := regType.In(1)
	require.Equal(t, reflect.Func, ctorType.Kind(),
		"the entrypoint takes a compiled Go func, not a script/path/plugin file")
	require.Equal(t, 1, ctorType.NumIn())
	require.Equal(t, reflect.TypeOf(HookBinding{}), ctorType.In(0), "ctor receives the binding")
	require.Equal(t, 2, ctorType.NumOut())
	require.Equal(t, reflect.TypeOf((*Hook)(nil)).Elem(), ctorType.Out(0), "ctor returns a Hook")
	require.Equal(t, reflect.TypeOf((*error)(nil)).Elem(), ctorType.Out(1), "ctor returns an error")

	// The registry stores exactly that func type by value: config names a key, the
	// executable behavior is a compiled-in func — no runtime interpretation.
	require.Equal(t, reflect.Func, reflect.TypeOf(customHooks).Elem().Kind())
	require.Equal(t, ctorType, reflect.TypeOf(customHooks).Elem(),
		"the registry value type is exactly the compiled-in ctor func type")
}
