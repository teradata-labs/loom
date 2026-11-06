// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build windows

package builtin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// startChildCommand makes PowerShell start a long-lived descendant, record its
// PID, and wait on it, so only killProcessTree (taskkill /T) can end it early.
func startChildCommand(pidFile string) string {
	return "$p = Start-Process -FilePath ping.exe -ArgumentList '-n','60','127.0.0.1' -NoNewWindow -PassThru; " +
		"Set-Content -Path '" + pidFile + "' -Value $p.Id -Encoding ascii; Wait-Process -Id $p.Id"
}

func windowsProcessAlive(pid int) bool {
	// #nosec G204 -- fixed program and flags; the only argument is a test child's PID.
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
	return err == nil && strings.Contains(string(out), fmt.Sprintf("\"%d\"", pid))
}

func windowsWaitGone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !windowsProcessAlive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !windowsProcessAlive(pid)
}

func windowsReadPID(t *testing.T, path string) int {
	t.Helper()
	var raw []byte
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(path)
		raw = b
		return err == nil && len(strings.TrimSpace(string(b))) > 0
	}, 15*time.Second, 50*time.Millisecond, "the command never wrote its child's pid")
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	return pid
}

func killPID(pid int) {
	// #nosec G204 -- fixed program and flags; the only argument is a test child's PID.
	_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// A timeout must end the shell's descendants (taskkill /T), not just the shell.
func TestShellExecute_WindowsTimeoutKillsDescendants(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	res, err := NewShellExecuteTool("").Execute(context.Background(), map[string]interface{}{
		"command":         startChildCommand(pidFile),
		"shell":           "powershell",
		"timeout_seconds": float64(10),
	})
	require.NoError(t, err)
	require.NotNil(t, res.Error)
	assert.Equal(t, "TIMEOUT", res.Error.Code)

	pid := windowsReadPID(t, pidFile)
	t.Cleanup(func() { killPID(pid) })
	assert.True(t, windowsWaitGone(pid, 5*time.Second), "descendant %d survived the timeout", pid)
}

// Cancelling the turn must do the same.
func TestShellExecute_WindowsCancelKillsDescendants(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *shuttle.Result, 1)
	go func() {
		res, _ := NewShellExecuteTool("").Execute(ctx, map[string]interface{}{
			"command": startChildCommand(pidFile),
			"shell":   "powershell",
		})
		done <- res
	}()
	pid := windowsReadPID(t, pidFile)
	t.Cleanup(func() { killPID(pid) })
	cancel()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Execute did not return after cancellation")
	}
	assert.True(t, windowsWaitGone(pid, 5*time.Second), "descendant %d survived cancellation", pid)
}
