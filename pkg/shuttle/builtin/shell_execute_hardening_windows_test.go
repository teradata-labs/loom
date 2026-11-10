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
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// startChildCommand makes PowerShell start a long-lived descendant, record its
// PID, and wait on it, so only killProcessTree can end it early.
func startChildCommand(pidFile string) string {
	return startOrphanCommand(pidFile) + "; Wait-Process -Id $p.Id"
}

// startOrphanCommand is startChildCommand without the wait: PowerShell exits
// at once and leaves ping.exe running, still holding the output pipes, with no
// live parent for "taskkill /T" to walk from (review: Ed-Jaramillo P2).
func startOrphanCommand(pidFile string) string {
	return "$p = Start-Process -FilePath ping.exe -ArgumentList '-n','60','127.0.0.1' -NoNewWindow -PassThru; " +
		"Set-Content -Path '" + pidFile + "' -Value $p.Id -Encoding ascii"
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

var warmPowerShellOnce sync.Once

// warmPowerShell runs one PowerShell Start-Process with the tool's flags before
// a timeout test, so a cold runner's first-start cost (over 10s seen in CI) is
// paid outside the timeout window instead of racing the pid write.
func warmPowerShell(t *testing.T) {
	t.Helper()
	warmPowerShellOnce.Do(func() {
		// #nosec G204 -- fixed test command.
		out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
			"Start-Process -FilePath cmd.exe -ArgumentList '/c','exit' -NoNewWindow -Wait").CombinedOutput()
		require.NoError(t, err, "PowerShell warm-up failed: %s", out)
	})
}

// windowsTimeoutSeconds leaves the shell room to start its child and write the
// pid on a slow runner before the timeout under test fires.
const windowsTimeoutSeconds = 20

func killPID(pid int) {
	// #nosec G204 -- fixed program and flags; the only argument is a test child's PID.
	_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// A timeout must end the shell's descendants, not just the shell.
func TestShellExecute_WindowsTimeoutKillsDescendants(t *testing.T) {
	warmPowerShell(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	res, err := NewShellExecuteTool("").Execute(context.Background(), map[string]interface{}{
		"command":         startChildCommand(pidFile),
		"shell":           "powershell",
		"timeout_seconds": float64(windowsTimeoutSeconds),
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

// A descendant whose parent shell has already exited must still be killed by
// the timeout. "taskkill /T" alone missed it; the Job Object reaches it.
func TestShellExecute_WindowsTimeoutKillsOrphanedDescendant(t *testing.T) {
	warmPowerShell(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	res, err := NewShellExecuteTool("").Execute(context.Background(), map[string]interface{}{
		"command":         startOrphanCommand(pidFile),
		"shell":           "powershell",
		"timeout_seconds": float64(windowsTimeoutSeconds),
	})
	require.NoError(t, err)
	require.NotNil(t, res.Error)
	assert.Equal(t, "TIMEOUT", res.Error.Code)

	pid := windowsReadPID(t, pidFile)
	t.Cleanup(func() { killPID(pid) })
	assert.True(t, windowsWaitGone(pid, 5*time.Second), "orphaned descendant %d survived the timeout", pid)
}

var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func windowsInJob(t *testing.T, process, job windows.Handle) bool {
	t.Helper()
	var in int32
	r, _, err := procIsProcessInJob.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&in)))
	require.NotZero(t, r, "IsProcessInJob: %v", err)
	return in != 0
}

// containProcessTree must put the suspended shell in a Job Object and resume
// it; the job must then take the shell's children with it on kill.
func TestContainProcessTree_WindowsAssignsJobAndResumes(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// #nosec G204 -- fixed test command.
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", startChildCommand(pidFile))
	setProcessGroup(cmd)
	require.NoError(t, cmd.Start())
	tree, err := containProcessTree(cmd)
	require.NoError(t, err)
	require.NotZero(t, tree.job, "the shell was not assigned to a Job Object")

	shell, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	require.NoError(t, err)
	defer func() { _ = windows.CloseHandle(shell) }()
	assert.True(t, windowsInJob(t, shell, tree.job), "the shell is not in its job")

	// The pid file is written only if the shell was resumed.
	pid := windowsReadPID(t, pidFile)
	t.Cleanup(func() { killPID(pid) })
	child, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	require.NoError(t, err)
	defer func() { _ = windows.CloseHandle(child) }()
	assert.True(t, windowsInJob(t, child, tree.job), "the shell's child is not in the job")

	killProcessTree(tree)
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("the shell did not exit after killProcessTree")
	}
	releaseProcessTree(tree)
	assert.Zero(t, tree.job)
	assert.True(t, windowsWaitGone(pid, 5*time.Second), "child %d survived killProcessTree", pid)
}

// After a normal completion the job is closed without killing what is left in
// it, matching Unix, where a completed command's background jobs keep running.
func TestReleaseProcessTree_WindowsNormalExitKeepsSurvivors(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// #nosec G204 -- fixed test command.
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", startOrphanCommand(pidFile))
	setProcessGroup(cmd)
	require.NoError(t, cmd.Start())
	tree, err := containProcessTree(cmd)
	require.NoError(t, err)
	require.NotZero(t, tree.job)
	// No pipes are attached, so the shell's exit is the normal completion.
	require.NoError(t, cmd.Wait())

	pid := windowsReadPID(t, pidFile)
	t.Cleanup(func() { killPID(pid) })
	releaseProcessTree(tree)
	time.Sleep(500 * time.Millisecond)
	assert.True(t, windowsProcessAlive(pid), "a normal completion killed survivor %d", pid)
}
