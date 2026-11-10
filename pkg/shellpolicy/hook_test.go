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
	osexec "os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

func fixedOptions(opt Options) OptionsFunc {
	return func(shuttle.AdmissionRequest) (Options, error) { return opt, nil }
}

// judgeAs makes the hook judge as it would on osName for the rest of the test.
// The hook tests that check how a call is judged run as "linux", so they still
// check the judgement on a Windows host, where the hook otherwise refuses
// every call. Their commands do not depend on POSIX path semantics. The judged
// host has bash on PATH (bashOnPath), whatever the test host has.
func judgeAs(t *testing.T, osName string) {
	t.Helper()
	prev := goos
	goos = osName
	t.Cleanup(func() { goos = prev })
	bashOnPath(t, true)
}

// bashOnPath makes the hook find bash on PATH, or not, for the rest of the
// test. Every other program is missing.
func bashOnPath(t *testing.T, found bool) {
	t.Helper()
	prev := lookPath
	lookPath = func(file string) (string, error) {
		if found && file == "bash" {
			return "/bin/bash", nil
		}
		return "", osexec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = prev })
}

func decide(t *testing.T, opt Options, static bool, params map[string]interface{}) shuttle.Decision {
	t.Helper()
	return Decide(Readonly(), shuttle.AdmissionRequest{Ctx: context.Background(), ToolName: "shell_execute", Params: params}, fixedOptions(opt), static)
}

func TestDecide(t *testing.T) {
	judgeAs(t, "linux")
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
	judgeAs(t, "linux")
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
	judgeAs(t, "linux")
	opt := session(t)
	f := Factory(map[string]*Policy{"readonly": Readonly()}, fixedOptions(opt))
	_, err := f(shuttle.HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown policy "nope" (known: readonly)`)

	eval, err := f(shuttle.HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly", Enforcement: "static"})
	require.NoError(t, err)
	assert.Equal(t, shuttle.Deny, eval(shuttle.AdmissionRequest{Params: map[string]interface{}{"command": "$x"}}).Kind)
}

// An empty enforcement means static: no runner exists to re-check what the
// analyzer cannot see, so a computed program name is not allowed by default.
func TestFactory_EmptyEnforcementIsStatic(t *testing.T) {
	judgeAs(t, "linux")
	f := Factory(map[string]*Policy{"readonly": Readonly()}, fixedOptions(session(t)))
	for _, enforcement := range []string{"", shuttle.CommandPolicyStatic, shuttle.CommandPolicyRuntime} {
		eval, err := f(shuttle.HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly", Enforcement: enforcement})
		require.NoError(t, err)
		for _, cmd := range []string{`$X -rf /`, `"$(echo rm)" file`} {
			d := eval(shuttle.AdmissionRequest{Params: map[string]interface{}{"command": cmd}})
			if enforcement == shuttle.CommandPolicyRuntime {
				assert.Equal(t, shuttle.Allow, d.Kind, "%q under runtime is left to the runner", cmd)
				continue
			}
			assert.Equal(t, shuttle.Deny, d.Kind, "%q under enforcement %q: %s", cmd, enforcement, d.Reason)
		}
	}
}

// End to end through the chain builder and the executor: an approved call's
// tool body receives the *Grant, an outright Allow receives none, and the
// binding's scope keeps other tools out of it.
func TestCommandPolicy_ThroughTheExecutor(t *testing.T) {
	judgeAs(t, "linux")
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

// N10: the hook parses the command as bash, so a call that runs it under
// another shell cannot be judged and is refused.
// On Windows the hook refuses every call before it reads the shell parameter
// (TestDecide_RefusesOnWindows); the shell check still treats "default" there
// as PowerShell or cmd.
func TestDecide_ShellParam(t *testing.T) {
	opt := session(t)
	for _, osName := range []string{"linux", "darwin"} {
		judgeAs(t, osName)
		for _, tc := range []struct {
			shell interface{}
			want  shuttle.DecisionKind
		}{
			{shell: "powershell", want: shuttle.Deny},
			{shell: "cmd", want: shuttle.Deny},
			{shell: "zsh", want: shuttle.Deny},
			{shell: 7, want: shuttle.Deny},
			{shell: "bash", want: shuttle.Allow},
			{shell: "sh", want: shuttle.Deny},
			{shell: "", want: shuttle.Allow},
			{shell: nil, want: shuttle.Allow},
			{shell: "default", want: shuttle.Allow},
		} {
			for _, static := range []bool{false, true} {
				d := decide(t, opt, static, map[string]interface{}{"command": "echo @(Remove-Item x)", "shell": tc.shell})
				assert.Equal(t, tc.want, d.Kind, "%s: shell %v (static=%v): %s", osName, tc.shell, static, d.Reason)
				if tc.want == shuttle.Deny {
					assert.Contains(t, d.Reason, "only bash commands can be judged", "%s: shell %v", osName, tc.shell)
				}
			}
		}
		d := decide(t, opt, true, map[string]interface{}{"command": "ls"})
		assert.Equal(t, shuttle.Allow, d.Kind, "%s: no shell parameter is the default shell", osName)
	}

	judgeAs(t, "windows")
	assert.Contains(t, unanalyzableShell(map[string]interface{}{"shell": "default"}), "only bash commands can be judged", `"default" is PowerShell or cmd on Windows`)
	assert.Empty(t, unanalyzableShell(map[string]interface{}{"shell": "bash"}))
}

// The analyzer judges POSIX paths; on Windows path/filepath does not treat
// "/etc/x" as absolute, so `echo x > /etc/x` under shell: bash would be
// allowed there. The hook refuses every call on Windows instead, whatever the
// shell parameter and the enforcement, and a command-policy binding still
// builds, so a Windows server starts.
func TestDecide_RefusesOnWindows(t *testing.T) {
	opt := session(t)
	const reason = "command-policy: the command policy judges POSIX paths and cannot judge commands on Windows"
	shells := []interface{}{nil, "", "default", "bash", "sh", "powershell"}
	commands := []string{"ls", "rm x", "sudo ls", "echo x > /etc/x"}

	judgeAs(t, "windows")
	for _, shell := range shells {
		for _, cmd := range commands {
			for _, static := range []bool{false, true} {
				params := map[string]interface{}{"command": cmd}
				if shell != nil {
					params["shell"] = shell
				}
				d := decide(t, opt, static, params)
				assert.Equal(t, shuttle.Deny, d.Kind, "shell %v, %q (static=%v)", shell, cmd, static)
				assert.Equal(t, reason, d.Reason, "shell %v, %q (static=%v)", shell, cmd, static)
				assert.Nil(t, d.Grant)
			}
		}
	}

	f := Factory(map[string]*Policy{"readonly": Readonly()}, fixedOptions(opt))
	for _, enforcement := range []string{"", shuttle.CommandPolicyStatic, shuttle.CommandPolicyRuntime} {
		eval, err := f(shuttle.HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly", Enforcement: enforcement})
		require.NoError(t, err, "the binding builds on Windows (enforcement %q)", enforcement)
		for _, shell := range []string{"bash", "default"} {
			d := eval(shuttle.AdmissionRequest{Params: map[string]interface{}{"command": "ls", "shell": shell}})
			assert.Equal(t, shuttle.Deny, d.Kind, "enforcement %q, shell %s", enforcement, shell)
			assert.Equal(t, reason, d.Reason, "enforcement %q, shell %s", enforcement, shell)
		}
	}

	reg := shuttle.NewRegistry()
	ran := false
	reg.Register(&shuttle.MockTool{MockName: "shell_execute", MockExecute: func(context.Context, map[string]interface{}) (*shuttle.Result, error) {
		ran = true
		return &shuttle.Result{Success: true}, nil
	}})
	chain, err := shuttle.BuildChainFromConfig(shuttle.HooksConfig{Bindings: []shuttle.HookBinding{
		{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly"},
	}}, shuttle.ChainDeps{Ask: approveAll{}, CommandPolicy: f})
	require.NoError(t, err, "a config with a command-policy binding loads on Windows")
	exec := shuttle.NewExecutor(reg)
	exec.SetAdmissionChain(chain)
	res, err := exec.Execute(context.Background(), "shell_execute", map[string]interface{}{"command": "ls", "shell": "bash"})
	require.NoError(t, err)
	require.False(t, res.Success)
	assert.Equal(t, "permission_denied", res.Error.Code)
	assert.False(t, ran, "the tool body never runs")

	// The same calls are judged, not refused, on any other OS. These commands
	// do not depend on path semantics, so this holds on a Windows host too.
	for _, osName := range []string{"linux", "darwin"} {
		judgeAs(t, osName)
		for _, static := range []bool{false, true} {
			for _, shell := range []interface{}{nil, "bash", "default"} {
				params := func(cmd string) map[string]interface{} {
					p := map[string]interface{}{"command": cmd}
					if shell != nil {
						p["shell"] = shell
					}
					return p
				}
				d := decide(t, opt, static, params("ls"))
				assert.Equal(t, shuttle.Allow, d.Kind, "%s: shell %v (static=%v): %s", osName, shell, static, d.Reason)
				d = decide(t, opt, static, params("rm x"))
				assert.Equal(t, shuttle.Ask, d.Kind, "%s: shell %v (static=%v): %s", osName, shell, static, d.Reason)
				d = decide(t, opt, static, params("sudo ls"))
				assert.Equal(t, shuttle.Deny, d.Kind, "%s: shell %v (static=%v)", osName, shell, static)
				assert.NotEqual(t, reason, d.Reason, "%s: denied by the policy, not the OS", osName)
			}
		}
		for _, enforcement := range []string{shuttle.CommandPolicyStatic, shuttle.CommandPolicyRuntime} {
			eval, err := f(shuttle.HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly", Enforcement: enforcement})
			require.NoError(t, err)
			d := eval(shuttle.AdmissionRequest{Params: map[string]interface{}{"command": "ls", "shell": "bash"}})
			assert.Equal(t, shuttle.Allow, d.Kind, "%s: enforcement %q: %s", osName, enforcement, d.Reason)
		}
		ran = false
		res, err = exec.Execute(context.Background(), "shell_execute", map[string]interface{}{"command": "ls", "shell": "bash"})
		require.NoError(t, err)
		assert.True(t, res.Success, osName)
		assert.True(t, ran, osName)
	}
}

// Found while fixing the review: shell_execute merges its env parameter into
// the shell's environment, so env: {BASH_ENV: script} ran the script before
// the command (bash 5.2). The env parameter is judged like an assignment the
// command makes.
func TestDecide_EnvParam(t *testing.T) {
	judgeAs(t, "linux")
	opt := session(t)
	for _, static := range []bool{false, true} {
		d := decide(t, opt, static, map[string]interface{}{"command": "ls", "env": map[string]interface{}{"BASH_ENV": "/tmp/x.sh", "FOO": "1"}})
		require.Equal(t, shuttle.Ask, d.Kind, "static=%v: %s", static, d.Reason)
		assert.Contains(t, d.Reason, "BASH_ENV (env parameter): sets BASH_ENV, which changes what programs run or load")
		g, ok := d.Grant.(*Grant)
		require.True(t, ok)
		assert.Equal(t, []string{"BASH_ENV"}, g.Env)

		d = decide(t, opt, static, map[string]interface{}{"command": "ls", "env": map[string]interface{}{"FOO": "1", "LANG": "C"}})
		assert.Equal(t, shuttle.Allow, d.Kind, "static=%v: %s", static, d.Reason)
	}
	d := decide(t, opt, true, map[string]interface{}{"command": "cd o", "env": map[string]interface{}{"CDPATH": ".."}})
	assert.Equal(t, shuttle.Deny, d.Kind, d.Reason)
	assert.Contains(t, d.Reason, "CDPATH (env parameter)")
	d = decide(t, opt, false, map[string]interface{}{"command": "ls", "env": map[string]interface{}{"PATH": 7}})
	assert.Equal(t, shuttle.Allow, d.Kind, "the tool ignores a value that is not a string")
}

// R3 (PR #430 review, Josh-Schoen): the policy parses the command as bash,
// but shell_execute runs "sh" from PATH for shell: sh, and for an absent,
// empty or "default" shell when bash is not on PATH. On Debian and Ubuntu sh
// is dash, which has no $'...' quoting, so echo $'\' ; touch pwned ; #' was
// one argument to bash and three commands to dash. Only bash is judged.
func TestDecide_ShellNeedsBash(t *testing.T) {
	opt := session(t)
	const dashSplit = `echo $'\' ; touch pwned ; #'`
	judgeAs(t, "linux")
	for _, tc := range []struct {
		shell interface{}
		bash  bool
		want  shuttle.DecisionKind
		why   string
	}{
		{shell: "sh", bash: true, want: shuttle.Deny, why: `runs under shell "sh"`},
		{shell: "sh", bash: false, want: shuttle.Deny, why: `runs under shell "sh"`},
		{shell: "bash", bash: true, want: shuttle.Allow},
		{shell: nil, bash: true, want: shuttle.Allow},
		{shell: "", bash: true, want: shuttle.Allow},
		{shell: "default", bash: true, want: shuttle.Allow},
		{shell: nil, bash: false, want: shuttle.Deny, why: "bash is not on PATH"},
		{shell: "", bash: false, want: shuttle.Deny, why: "bash is not on PATH"},
		{shell: "default", bash: false, want: shuttle.Deny, why: "bash is not on PATH"},
	} {
		bashOnPath(t, tc.bash)
		for _, static := range []bool{false, true} {
			params := map[string]interface{}{"command": dashSplit}
			if tc.shell != nil {
				params["shell"] = tc.shell
			}
			d := decide(t, opt, static, params)
			assert.Equal(t, tc.want, d.Kind, "shell %v, bash on PATH %v (static=%v): %s", tc.shell, tc.bash, static, d.Reason)
			if tc.why != "" {
				assert.Contains(t, d.Reason, tc.why, "shell %v, bash on PATH %v", tc.shell, tc.bash)
				assert.Contains(t, d.Reason, "only bash commands can be judged", "shell %v, bash on PATH %v", tc.shell, tc.bash)
			}
		}
	}
}
