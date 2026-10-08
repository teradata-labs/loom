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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

func shellData(t *testing.T, r *shuttle.Result) map[string]interface{} {
	t.Helper()
	require.NotNil(t, r)
	data, ok := r.Data.(map[string]interface{})
	require.True(t, ok, "result has no data map: %+v", r)
	return data
}

// The server's own secrets must not reach commands: the sensitive-name filter
// used to apply only to the call's env param, while os.Environ() was passed
// through whole.
func TestShellExecute_ServerSecretsAreNotInherited(t *testing.T) {
	secrets := map[string]string{
		"LOOMTEST_ANTHROPIC_API_KEY": "sk-should-not-leak-1",
		"LOOMTEST_SERVICE_TOKEN":     "should-not-leak-2",
		"LOOMTEST_DB_PASSWORD":       "should-not-leak-3",
		"LOOMTEST_CLIENT_SECRET":     "should-not-leak-4",
		"LOOMTEST_GCP_CREDENTIALS":   "should-not-leak-5",
		"LOOMTEST_AWS_ACCESS_KEY_ID": "should-not-leak-6",
		"LOOMTEST_DATABASE_URL":      "postgres://u:should-not-leak-7@h/db",
		"LOOMTEST_PROXY":             "http://u:should-not-leak-8@proxy:3128",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	t.Setenv("LOOMTEST_PLAIN_SETTING", "plain-value")

	res, err := NewShellExecuteTool("").Execute(context.Background(), map[string]interface{}{"command": "env"})
	require.NoError(t, err)
	require.True(t, res.Success, "%+v", res.Error)
	stdout := shellData(t, res)["stdout"].(string)

	// Never print stdout on failure: it is the test process's environment,
	// which holds real credentials on developer machines and CI runners.
	assert.True(t, strings.Contains(stdout, "LOOMTEST_PLAIN_SETTING=plain-value"), "ordinary variables are still inherited")
	for k, v := range secrets {
		assert.False(t, strings.Contains(stdout, v), "%s leaked into the command's environment", k)
	}
}

// An operator can still hand a named secret to commands on purpose.
func TestShellExecute_PassEnvAllowsNamedSecrets(t *testing.T) {
	t.Setenv("GH_TOKEN", "deliberately-passed")
	t.Setenv("LOOMTEST_OTHER_TOKEN", "not-passed")
	tool := NewShellExecuteTool("")
	tool.SetPassEnv([]string{"GH_TOKEN"})

	res, err := tool.Execute(context.Background(), map[string]interface{}{"command": "env"})
	require.NoError(t, err)
	stdout := shellData(t, res)["stdout"].(string)
	assert.True(t, strings.Contains(stdout, "GH_TOKEN=deliberately-passed"), "a passed name reaches the command")
	assert.False(t, strings.Contains(stdout, "not-passed"), "an unlisted token stays withheld")
}

func TestParsePassEnv(t *testing.T) {
	assert.Equal(t, map[string]bool{"GH_TOKEN": true, "NPM_TOKEN": true}, parsePassEnv(" GH_TOKEN, ,NPM_TOKEN,"))
	assert.Empty(t, parsePassEnv(""))
}

func TestHasURLCredentials(t *testing.T) {
	for value, want := range map[string]bool{
		"postgres://u:pw@h/db":   true,
		"http://u:pw@proxy:3128": true,
		"http://u@proxy:3128":    false,
		"https://example.com/a":  false,
		"not a url @ all":        false,
		"user@example.com":       false,
	} {
		assert.Equal(t, want, hasURLCredentials(value), value)
	}
}

func TestInheritedEnv(t *testing.T) {
	got := inheritedEnv([]string{
		"PATH=/usr/bin", "GH_TOKEN=a", "HF_TOKEN=b", "PROXY=http://u:p@h", "EMPTY=", "NOEQUALS",
	}, map[string]bool{"GH_TOKEN": true})
	assert.Equal(t, []string{"PATH=/usr/bin", "GH_TOKEN=a", "EMPTY=", "NOEQUALS"}, got)
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitGone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !processAlive(pid)
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	var raw []byte
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(path)
		raw = b
		return err == nil && len(strings.TrimSpace(string(b))) > 0
	}, 5*time.Second, 20*time.Millisecond, "the command never wrote its child's pid")
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	return pid
}

// A timeout must kill everything the shell started, not just the shell.
func TestShellExecute_TimeoutKillsBackgroundChildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	res, err := NewShellExecuteTool("").Execute(context.Background(), map[string]interface{}{
		"command":         "sleep 30 & echo $! > " + pidFile + "; wait",
		"timeout_seconds": float64(1),
	})
	require.NoError(t, err)
	require.NotNil(t, res.Error)
	assert.Equal(t, "TIMEOUT", res.Error.Code)

	pid := readPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	assert.True(t, waitGone(pid, 2*time.Second), "background child %d survived the timeout", pid)
}

// Cancelling the turn must do the same.
func TestShellExecute_CancelKillsBackgroundChildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *shuttle.Result, 1)
	go func() {
		res, _ := NewShellExecuteTool("").Execute(ctx, map[string]interface{}{
			"command": "sleep 30 & echo $! > " + pidFile + "; wait",
		})
		done <- res
	}()
	pid := readPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after cancellation")
	}
	assert.True(t, waitGone(pid, 2*time.Second), "background child %d survived cancellation", pid)
}

// Output past max_output_bytes must stop the command at once, not wait for the
// timeout with the writer blocked on a full pipe.
func TestShellExecute_OutputOverflowStopsPromptly(t *testing.T) {
	start := time.Now()
	res, err := NewShellExecuteTool("").Execute(context.Background(), map[string]interface{}{
		"command":          "yes overflow",
		"max_output_bytes": float64(4096),
		"timeout_seconds":  float64(30),
	})
	require.NoError(t, err)
	require.NotNil(t, res.Error)
	assert.Equal(t, "OUTPUT_OVERFLOW", res.Error.Code)
	assert.Less(t, time.Since(start), 5*time.Second, "overflow waited for the timeout")
}

// working_dir confinement must follow symlinks: a link inside LOOM_DATA_DIR
// that points outside it is outside it.
func TestShellExecute_WorkingDirSymlinkEscapeIsRefused(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", dataDir)
	link := filepath.Join(dataDir, "escape")
	require.NoError(t, os.Symlink("/usr", link))

	tool := NewShellExecuteTool("")
	tool.SetLoomDataDir(dataDir)
	ctx := session.WithSessionID(context.Background(), "sess-s0-test")
	res, err := tool.Execute(ctx, map[string]interface{}{"command": "pwd", "working_dir": link})
	require.NoError(t, err)
	require.NotNil(t, res.Error, "a symlink out of LOOM_DATA_DIR was accepted: %+v", res.Data)
	assert.Equal(t, "PATH_RESTRICTED", res.Error.Code)

	inside := filepath.Join(dataDir, "work")
	require.NoError(t, os.Mkdir(inside, 0o700))
	res, err = tool.Execute(ctx, map[string]interface{}{"command": "pwd", "working_dir": inside})
	require.NoError(t, err)
	assert.True(t, res.Success, "a real directory inside LOOM_DATA_DIR is allowed: %+v", res.Error)
}

func TestWithinDir(t *testing.T) {
	for _, tc := range []struct {
		root, path string
		want       bool
	}{
		{"/a/data", "/a/data", true},
		{"/a/data", "/a/data/x/y", true},
		{"/a/data", "/a/data-evil", false},
		{"/a/data", "/a/datax/y", false},
		{"/a/data", "/a", false},
		{"/a/data", "/a/data/../other", false},
		{"/tmp", "/tmpfoo", false},
		{"/tmp", "/tmp/foo", true},
		{"/", "/anything", true},
	} {
		assert.Equal(t, tc.want, withinDir(tc.root, tc.path), "withinDir(%q, %q)", tc.root, tc.path)
	}
}

func TestIsSensitiveEnvName(t *testing.T) {
	for name, want := range map[string]bool{
		"ANTHROPIC_API_KEY":      true,
		"AWS_ACCESS_KEY_ID":      true,
		"AWS_SECRET_ACCESS_KEY":  true,
		"AWS_SESSION_TOKEN":      true,
		"GH_TOKEN":               true,
		"HF_TOKEN":               true,
		"MY_PRIVATE_KEY":         true,
		"OPENAI_APIKEY":          true,
		"GOOGLE_CREDENTIALS":     true,
		"DB_PASSWD":              true,
		"POSTGRES_PASSWORD":      true,
		"CLIENT_SECRET":          true,
		"DATABASE_URL":           true,
		"ANALYTICS_DSN":          true,
		"github_token":           true,
		"PATH":                   false,
		"HOME":                   false,
		"KEYBOARD_LAYOUT":        false,
		"MONKEY":                 false,
		"LANG":                   false,
		"SSH_AUTH_SOCK":          false,
		"LOOM_DATA_DIR":          false,
		"TOKENIZERS_PARALLELISM": false,
	} {
		assert.Equal(t, want, isSensitiveEnvName(name), name)
	}
}
