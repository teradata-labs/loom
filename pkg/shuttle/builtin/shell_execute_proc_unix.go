// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build !windows

package builtin

import (
	"os/exec"
	"syscall"
)

// processTree is the shell and every process it starts. On Unix it is the
// shell's process group, identified by the shell's PID.
type processTree struct {
	cmd *exec.Cmd
}

// setProcessGroup starts the command as the leader of a new process group, so
// killProcessTree reaches every process the shell starts (background jobs,
// pipeline members, their children). A process that calls setsid itself leaves
// the group and is not reached.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// containProcessTree is called right after cmd.Start. On Unix the group was
// formed by Setpgid at start, so there is nothing more to do and it never
// fails.
func containProcessTree(cmd *exec.Cmd) (*processTree, error) {
	return &processTree{cmd: cmd}, nil
}

// killProcessTree sends SIGKILL to the command's whole process group, then to
// the shell itself in case the group was never formed. Errors are ignored: the
// processes may already have exited.
func killProcessTree(tree *processTree) {
	if tree == nil || tree.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-tree.cmd.Process.Pid, syscall.SIGKILL)
	_ = tree.cmd.Process.Kill()
}

// rekillProcessGroup sends SIGKILL to the command's process group again. It is
// called only before cmd.Wait has reaped the shell: the unreaped leader keeps
// its PID, and so the group ID, from being reused.
func rekillProcessGroup(tree *processTree) {
	if tree == nil || tree.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-tree.cmd.Process.Pid, syscall.SIGKILL)
}

// releaseProcessTree does nothing on Unix: a process group holds no handle.
// A normally completed command's background jobs keep running.
func releaseProcessTree(_ *processTree) {}
