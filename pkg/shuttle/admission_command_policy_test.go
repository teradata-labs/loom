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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandPolicyBinding_Validation(t *testing.T) {
	for name, tc := range map[string]struct {
		b   HookBinding
		err string
	}{
		"valid":                {b: HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly"}},
		"valid static":         {b: HookBinding{Kind: "command-policy", Scope: "shell_execute_sandbox", Policy: "sandbox", Enforcement: "static"}},
		"valid runtime":        {b: HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly", Enforcement: "runtime"}},
		"no policy":            {b: HookBinding{Kind: "command-policy", Scope: "shell_execute"}, err: "requires policy"},
		"bad enforcement":      {b: HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "p", Enforcement: "lazy"}, err: "enforcement"},
		"policy on other kind": {b: HookBinding{Kind: "ask", Scope: "x", Policy: "readonly"}, err: "command-policy fields"},
		"enforcement on other": {b: HookBinding{Kind: "denylist", Scope: "x", Enforcement: "static"}, err: "command-policy fields"},
		"no scope":             {b: HookBinding{Kind: "command-policy", Policy: "readonly"}, err: "scope is required"},
	} {
		err := ValidateHooksConfig(HooksConfig{Bindings: []HookBinding{tc.b}})
		if tc.err == "" {
			assert.NoError(t, err, name)
		} else {
			require.Error(t, err, name)
			assert.Contains(t, err.Error(), tc.err, name)
		}
	}
}

func TestCommandPolicyBinding_Build(t *testing.T) {
	b := HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly"}
	_, err := BuildChainFromConfig(HooksConfig{Bindings: []HookBinding{b}}, ChainDeps{})
	require.Error(t, err, "no factory wired fails closed")
	assert.Contains(t, err.Error(), "no command policies are configured")

	_, err = BuildChainFromConfig(HooksConfig{Bindings: []HookBinding{b}}, ChainDeps{
		CommandPolicy: func(HookBinding) (func(AdmissionRequest) Decision, error) { return nil, errors.New("unknown policy") },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown policy")

	var got HookBinding
	chain, err := BuildChainFromConfig(HooksConfig{Bindings: []HookBinding{
		{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly", Matcher: MatcherSpec{ParamPath: "command", Op: "regex", Value: "^rm"}},
	}}, ChainDeps{
		CommandPolicy: func(b HookBinding) (func(AdmissionRequest) Decision, error) {
			got = b
			return func(AdmissionRequest) Decision { return Decision{Kind: Deny, Reason: "policy"} }, nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "readonly", got.Policy, "the factory receives the whole binding")
	ctx := context.Background()
	assert.Equal(t, Deny, chain.Admit(AdmissionRequest{Ctx: ctx, ToolName: "shell_execute", Params: map[string]interface{}{"command": "rm x"}}).Decision.Kind)
	assert.Equal(t, NoDecision, chain.Admit(AdmissionRequest{Ctx: ctx, ToolName: "shell_execute", Params: map[string]interface{}{"command": "ls"}}).Decision.Kind,
		"the matcher applies around the policy, as for every kind")
	assert.Equal(t, NoDecision, chain.Admit(AdmissionRequest{Ctx: ctx, ToolName: "file_read", Params: map[string]interface{}{"command": "rm x"}}).Decision.Kind,
		"the scope applies")
}

func TestCommandPolicyBinding_JSON(t *testing.T) {
	cfg, err := ParseHooksConfig([]byte(`{"bindings":[{"kind":"command-policy","scope":"shell_execute","Policy":"readonly","enforcement":"static"}]}`))
	require.NoError(t, err)
	require.Len(t, cfg.Bindings, 1)
	assert.Equal(t, "readonly", cfg.Bindings[0].Policy)
	assert.Equal(t, "static", cfg.Bindings[0].Enforcement)
}
