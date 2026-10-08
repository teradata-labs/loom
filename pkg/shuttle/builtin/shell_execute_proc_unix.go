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

// setProcessGroup starts the command as the leader of a new process group, so
// killProcessTree reaches every process the shell starts (background jobs,
// pipeline members, their children). A process that calls setsid itself leaves
// the group and is not reached.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree sends SIGKILL to the command's whole process group, then to
// the shell itself in case the group was never formed. Errors are ignored: the
// processes may already have exited.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
