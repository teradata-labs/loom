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

package shellpolicy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

func fixedOptions(opt Options) OptionsFunc {
	return func(shuttle.AdmissionRequest) (Options, error) { return opt, nil }
}

func decide(t *testing.T, opt Options, static bool, params map[string]interface{}) shuttle.Decision {
	t.Helper()
	return Decide(Readonly(), shuttle.AdmissionRequest{Ctx: context.Background(), ToolName: "shell_execute", Params: params}, fixedOptions(opt), static)
}

func TestDecide(t *testing.T) {
	opt := session(t)

	d := decide(t, opt, false, map[string]interface{}{"command": "ls -la"})
	assert.Equal(t, shuttle.Allow, d.Kind)

	d = decide(t, opt, false, map[string]interface{}{"command": "rm nope"})
	require.Equal(t, shuttle.Ask, d.Kind)
	assert.Contains(t, d.Reason, "rm (1:1): not on the shell allowlist")
	g, ok := d.Grant.(*Grant)
	require.True(t, ok, "the grant is a *Grant")
	assert.Equal(t, []string{"rm"}, g.Programs)

	d = decide(t, opt, false, map[string]interface{}{"command": "sudo ls"})
	assert.Equal(t, shuttle.Deny, d.Kind)
	assert.Nil(t, d.Grant)

	d = decide(t, opt, true, map[string]interface{}{"command": "$cmd x"})
	assert.Equal(t, shuttle.Deny, d.Kind, "static enforcement refuses what it cannot see")

	for _, params := range []map[string]interface{}{{}, {"command": "  "}, {"command": 7}} {
		d = decide(t, opt, false, params)
		assert.Equal(t, shuttle.Deny, d.Kind, "%v", params)
		assert.Contains(t, d.Reason, "no command")
	}

	failing := func(shuttle.AdmissionRequest) (Options, error) { return Options{}, errors.New("no session") }
	d = Decide(Readonly(), shuttle.AdmissionRequest{Params: map[string]interface{}{"command": "ls"}}, failing, false)
	assert.Equal(t, shuttle.Deny, d.Kind)
	assert.Contains(t, d.Reason, "no session")
}

// An approval card cuts params past shuttle.ApprovalParamsMaxBytes, so a call
// that would need approval but is that long is refused instead.
func TestDecide_TooLongToReview(t *testing.T) {
	opt := session(t)
	long := "echo " + strings.Repeat("x", shuttle.ApprovalParamsMaxBytes)

	d := decide(t, opt, false, map[string]interface{}{"command": long})
	assert.Equal(t, shuttle.Allow, d.Kind, "a long command that needs no approval still runs")

	d = decide(t, opt, false, map[string]interface{}{"command": long + "; rm x"})
	assert.Equal(t, shuttle.Deny, d.Kind)
	assert.Contains(t, d.Reason, "too long to show in full on an approval card")
	assert.Contains(t, d.Reason, "rm (1:")
}

func TestFactory(t *testing.T) {
	opt := session(t)
	f := Factory(map[string]*Policy{"readonly": Readonly()}, fixedOptions(opt))
	_, err := f(shuttle.HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown policy "nope" (known: readonly)`)

	eval, err := f(shuttle.HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly", Enforcement: "static"})
	require.NoError(t, err)
	assert.Equal(t, shuttle.Deny, eval(shuttle.AdmissionRequest{Params: map[string]interface{}{"command": "$x"}}).Kind)
}

// End to end through the chain builder and the executor: an approved call's
// tool body receives the *Grant, an outright Allow receives none, and the
// binding's scope keeps other tools out of it.
func TestCommandPolicy_ThroughTheExecutor(t *testing.T) {
	opt := session(t)
	var seen []*Grant
	shell := &shuttle.MockTool{MockName: "shell_execute", MockExecute: func(ctx context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
		seen = append(seen, MergeGrants(shuttle.AdmissionGrantsFromContext(ctx)))
		return &shuttle.Result{Success: true}, nil
	}}
	other := &shuttle.MockTool{MockName: "file_read"}
	reg := shuttle.NewRegistry()
	reg.Register(shell)
	reg.Register(other)

	chain, err := shuttle.BuildChainFromConfig(shuttle.HooksConfig{Bindings: []shuttle.HookBinding{
		{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly"},
	}}, shuttle.ChainDeps{
		Ask:           approveAll{},
		CommandPolicy: Factory(map[string]*Policy{"readonly": Readonly()}, fixedOptions(opt)),
	})
	require.NoError(t, err)
	exec := shuttle.NewExecutor(reg)
	exec.SetAdmissionChain(chain)
	ctx := context.Background()

	res, err := exec.Execute(ctx, "shell_execute", map[string]interface{}{"command": "ls"})
	require.NoError(t, err)
	require.True(t, res.Success)
	res, err = exec.Execute(ctx, "shell_execute", map[string]interface{}{"command": "rm x; npm test"})
	require.NoError(t, err)
	require.True(t, res.Success)
	res, err = exec.Execute(ctx, "shell_execute", map[string]interface{}{"command": "sudo rm x"})
	require.NoError(t, err)
	require.False(t, res.Success)
	assert.Equal(t, "permission_denied", res.Error.Code)
	res, err = exec.Execute(ctx, "file_read", map[string]interface{}{"command": "rm x"})
	require.NoError(t, err)
	assert.True(t, res.Success, "the binding's scope keeps other tools out")

	require.Len(t, seen, 2)
	assert.Nil(t, seen[0], "an outright Allow carries no grant")
	require.NotNil(t, seen[1])
	assert.Equal(t, []string{"rm", "npm"}, seen[1].Programs)
}

type approveAll struct{}

func (approveAll) Resolve(shuttle.AdmissionRequest, shuttle.Decision) shuttle.Decision {
	return shuttle.Decision{Kind: shuttle.Allow}
}
