// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build !windows

package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shelljail"
	"github.com/teradata-labs/loom/pkg/shellpolicy"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

const jailSession = "sess-jailed-tool"

type jailedFixture struct {
	runner  *shelljail.Runner
	tool    *ShellExecuteTool
	ctx     context.Context
	scratch string
}

func newJailedFixture(t *testing.T) *jailedFixture {
	t.Helper()
	data, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("LOOM_DATA_DIR", data)
	r, _, err := shelljail.NewRunner(shelljail.Config{Policy: shellpolicy.Readonly(), LoomDataDir: data, Strict: true})
	require.NoError(t, err)
	tool := NewShellExecuteTool("")
	tool.SetLoomDataDir(data)
	tool.SetJail(r)
	opt, err := r.Options(jailSession, "")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(opt.WorkingDir, "notes.txt"), []byte("alpha\nbeta\n"), 0o600))
	return &jailedFixture{runner: r, tool: tool, ctx: session.WithSessionID(context.Background(), jailSession), scratch: opt.WorkingDir}
}

func jailOf(t *testing.T, res *shuttle.Result) map[string]interface{} {
	t.Helper()
	j, ok := shellData(t, res)["jail"].(map[string]interface{})
	require.True(t, ok, "jailed results carry a jail block: %+v", res.Data)
	return j
}

func blockedNames(j map[string]interface{}) []string {
	var out []string
	for _, b := range j["blocked"].([]map[string]interface{}) {
		out = append(out, b["name"].(string))
	}
	return out
}

func TestJailedShell_AllowedCommandRuns(t *testing.T) {
	f := newJailedFixture(t)
	res, err := f.tool.Execute(f.ctx, map[string]interface{}{"command": "cat notes.txt | grep beta && ls"})
	require.NoError(t, err)
	require.True(t, res.Success, "%+v", res.Error)
	data := shellData(t, res)
	assert.Contains(t, data["stdout"], "beta")
	assert.Contains(t, data["stdout"], "notes.txt")
	assert.Equal(t, "jailed", data["shell"])
	assert.Equal(t, f.scratch, data["working_dir"], "jailed commands start in the session scratchpad")
	assert.Empty(t, blockedNames(jailOf(t, res)))
}

func TestJailedShell_OffListProgramIsBlockedWithAnActionableError(t *testing.T) {
	f := newJailedFixture(t)
	res, err := f.tool.Execute(f.ctx, map[string]interface{}{"command": "x=rm; $x notes.txt"})
	require.NoError(t, err)
	require.False(t, res.Success)
	assert.Equal(t, "EXIT_ERROR", res.Error.Code)
	assert.Contains(t, res.Error.Message, "the shell policy blocked: rm (1:7)")
	assert.Contains(t, res.Error.Suggestion, "written out")
	assert.Equal(t, []string{"rm"}, blockedNames(jailOf(t, res)))
	_, err = os.Stat(filepath.Join(f.scratch, "notes.txt"))
	assert.NoError(t, err, "the blocked rm deleted nothing")
}

// What a person approved (the admission grants) is exactly what runs.
func TestJailedShell_ApprovedProgramRuns(t *testing.T) {
	f := newJailedFixture(t)
	ctx := shuttle.ContextWithAdmissionGrants(f.ctx, []any{&shellpolicy.Grant{Programs: []string{"rm"}}})
	res, err := f.tool.Execute(ctx, map[string]interface{}{"command": "rm notes.txt"})
	require.NoError(t, err)
	require.True(t, res.Success, "%+v", res.Error)
	_, err = os.Stat(filepath.Join(f.scratch, "notes.txt"))
	assert.True(t, os.IsNotExist(err), "the approved rm ran")
}

func TestJailedShell_ServerEnvironmentNeverReachesCommands(t *testing.T) {
	f := newJailedFixture(t)
	t.Setenv("LOOMTEST_JAIL_SERVER_VAR", "server-only")
	res, err := f.tool.Execute(f.ctx, map[string]interface{}{
		"command": `echo "server:${LOOMTEST_JAIL_SERVER_VAR:-absent} call:$CALL_VAR session:$SESSION_ID"`,
		"env":     map[string]interface{}{"CALL_VAR": "yes", "LD_PRELOAD": "/x.so", "GH_TOKEN": "t"},
	})
	require.NoError(t, err)
	require.True(t, res.Success, "%+v", res.Error)
	assert.Contains(t, shellData(t, res)["stdout"], "server:absent call:yes session:"+jailSession)
	assert.Equal(t, []string{"GH_TOKEN", "LD_PRELOAD"}, jailOf(t, res)["env_dropped"])
}

func TestJailedShell_TimeoutReportsTheLimit(t *testing.T) {
	f := newJailedFixture(t)
	start := time.Now()
	res, err := f.tool.Execute(f.ctx, map[string]interface{}{"command": "sleep 30", "timeout_seconds": float64(1)})
	require.NoError(t, err)
	assert.Equal(t, "TIMEOUT", res.Error.Code)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, "timeout", jailOf(t, res)["limit"])
}

func TestJailedShell_Refusals(t *testing.T) {
	f := newJailedFixture(t)
	res, err := f.tool.Execute(context.Background(), map[string]interface{}{"command": "ls"})
	require.NoError(t, err)
	assert.Equal(t, "JAIL_UNAVAILABLE", res.Error.Code, "no session, no jailed shell")

	res, err = f.tool.Execute(f.ctx, map[string]interface{}{"command": "ls", "shell": "powershell"})
	require.NoError(t, err)
	assert.Equal(t, "INVALID_PARAMS", res.Error.Code)

	start := time.Now()
	res, err = f.tool.Execute(f.ctx, map[string]interface{}{"command": "seq 1 10000000", "max_output_bytes": float64(2048)})
	require.NoError(t, err)
	assert.Equal(t, "OUTPUT_OVERFLOW", res.Error.Code)
	assert.Less(t, time.Since(start), 5*time.Second, "overflow stops the jailed command at once")
	assert.NotNil(t, res.Data.(map[string]interface{})["jail"])
}

// The whole path: command-policy hook, a person's approval, the jail.
func TestJailedShell_ThroughTheHookAndApproval(t *testing.T) {
	f := newJailedFixture(t)
	for _, tc := range []struct {
		name, command string
		approve       bool
		wantCode      string // "" means success
		notesGone     bool
	}{
		{name: "allowed outright", command: "cat notes.txt"},
		{name: "approved rm", command: "rm notes.txt", approve: true, notesGone: true},
		{name: "rejected rm", command: "rm notes.txt", approve: false, wantCode: "permission_denied"},
		{name: "computed rm passes the hook, the jail blocks it", command: "x=rm; $x notes.txt", approve: true, wantCode: "EXIT_ERROR"},
		{name: "never", command: "sudo rm notes.txt", approve: true, wantCode: "permission_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(filepath.Join(f.scratch, "notes.txt"), []byte("x\n"), 0o600))
			decision := shuttle.Decision{Kind: shuttle.Deny, Reason: "rejected"}
			if tc.approve {
				decision = shuttle.Decision{Kind: shuttle.Allow}
			}
			chain, err := shuttle.BuildChainFromConfig(shuttle.HooksConfig{Bindings: []shuttle.HookBinding{
				{Kind: "command-policy", Scope: "shell_execute", Policy: "readonly"},
			}}, shuttle.ChainDeps{
				Ask:           fixedResolver{decision},
				CommandPolicy: shellpolicy.Factory(map[string]*shellpolicy.Policy{"readonly": f.runner.Policy()}, f.runner.OptionsFunc()),
			})
			require.NoError(t, err)
			reg := shuttle.NewRegistry()
			reg.Register(f.tool)
			exec := shuttle.NewExecutor(reg)
			exec.SetAdmissionChain(chain)
			res, err := exec.Execute(f.ctx, "shell_execute", map[string]interface{}{"command": tc.command})
			require.NoError(t, err)
			if tc.wantCode == "" {
				assert.True(t, res.Success, "%+v", res.Error)
			} else {
				require.NotNil(t, res.Error)
				assert.Equal(t, tc.wantCode, res.Error.Code, res.Error.Message)
			}
			_, statErr := os.Stat(filepath.Join(f.scratch, "notes.txt"))
			assert.Equal(t, tc.notesGone, os.IsNotExist(statErr))
		})
	}
}

type fixedResolver struct{ d shuttle.Decision }

func (r fixedResolver) Resolve(shuttle.AdmissionRequest, shuttle.Decision) shuttle.Decision {
	return r.d
}

func TestSetShellJail(t *testing.T) {
	f := newJailedFixture(t)
	t.Cleanup(func() { SetShellJail(nil) })
	SetShellJail(f.runner)
	assert.Same(t, f.runner, ShellJail())
	assert.Same(t, f.runner, NewShellExecuteTool("").jail, "tools created after SetShellJail use it")
	SetShellJail(nil)
	assert.Nil(t, NewShellExecuteTool("").jail)
}
