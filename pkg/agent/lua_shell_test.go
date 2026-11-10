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

//go:build !windows

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/shelljail"
	"github.com/teradata-labs/loom/pkg/shellpolicy"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
)

// TestMain lets this test binary serve as the jailed shell's child.
func TestMain(m *testing.M) {
	shelljail.Main()
	os.Exit(m.Run())
}

// A script's shell_execute call is judged by the command-policy hook and run
// by the jail, like the model's own: an allowed command runs, an off-list one
// comes back approval_required (a script never waits for a person), and a
// program computed at run time is stopped. Under runtime enforcement the hook
// lets the computed program through and the jail blocks it (EXIT_ERROR); under
// static enforcement (the default) the hook denies it before the jail runs.
func TestRunLua_ShellThroughTheHookAndTheJail(t *testing.T) {
	for _, tc := range []struct {
		enforcement  string
		wantComputed string
	}{
		{enforcement: shuttle.CommandPolicyRuntime, wantComputed: "EXIT_ERROR"},
		{enforcement: shuttle.CommandPolicyStatic, wantComputed: luasandbox.CodePermissionDenied},
		{enforcement: "", wantComputed: luasandbox.CodePermissionDenied},
	} {
		name := tc.enforcement
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			data, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			t.Setenv("LOOM_DATA_DIR", data)
			runner, _, err := shelljail.NewRunner(shelljail.Config{Policy: shellpolicy.Readonly(), LoomDataDir: data, Strict: true})
			require.NoError(t, err)
			shell := builtin.NewShellExecuteTool("")
			shell.SetLoomDataDir(data)
			shell.SetJail(runner)

			chain, err := shuttle.BuildChainFromConfig(shuttle.HooksConfig{Bindings: []shuttle.HookBinding{
				{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly", Enforcement: tc.enforcement},
			}}, shuttle.ChainDeps{
				CommandPolicy: shellpolicy.Factory(map[string]*shellpolicy.Policy{"readonly": runner.Policy()}, runner.OptionsFunc()),
			})
			require.NoError(t, err)
			ag := newLuaTestAgent(t, chain, shell)
			tool, err := NewRunLuaTool(ag, RunLuaToolOptions{Policy: staticLuaPolicy(luasandbox.Policy{}), Gate: luasandbox.NewGate(2, 1)})
			require.NoError(t, err)

			opt, err := runner.Options("lua-test-session", "")
			require.NoError(t, err)
			notes := filepath.Join(opt.WorkingDir, "notes.txt")
			require.NoError(t, os.WriteFile(notes, []byte("alpha\n"), 0o600))

			res, err := tool.Execute(runLuaCtx("shell_execute"), map[string]interface{}{"script": `
local listed = tools.must("shell_execute", {command = "cat notes.txt"})
local asked = tools.call("shell_execute", {command = "rm notes.txt"})
local computed = tools.call("shell_execute", {command = "x=rm; $x notes.txt"})
return {listed.stdout, asked.error.code, computed.error.code}`})
			require.NoError(t, err)
			require.True(t, res.Success, "%+v", res.Error)
			assert.Equal(t, []any{"alpha", luasandbox.CodeApprovalRequired, tc.wantComputed}, res.Data)
			_, err = os.Stat(notes)
			assert.NoError(t, err, "neither rm ran")

			// Review #432 F1: a file outside the session, named directly or
			// inside a --files0-from list, is never read through the gate.
			db := filepath.Join(data, "loom.db")
			require.NoError(t, os.WriteFile(db, []byte("SECRETDB\n"), 0o600))
			res, err = tool.Execute(runLuaCtx("shell_execute"), map[string]interface{}{"script": `
local direct = tools.call("shell_execute", {command = "sort ` + db + `"})
local listed = tools.call("shell_execute", {command = "printf '` + db + `\\0' > l; sort --files0-from=l"})
return {direct.error and direct.error.code or direct.stdout, listed.error and listed.error.code or listed.stdout}`})
			require.NoError(t, err)
			require.True(t, res.Success, "%+v", res.Error)
			assert.Equal(t, []any{luasandbox.CodeApprovalRequired, luasandbox.CodeApprovalRequired}, res.Data)
			assert.NotContains(t, fmt.Sprint(res.Data), "SECRETDB")
		})
	}
}
