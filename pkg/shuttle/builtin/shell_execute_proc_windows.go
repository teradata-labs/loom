// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build windows

package builtin

import (
	"os/exec"
	"strconv"
	"syscall"
)

// setProcessGroup starts the command in a new process group. Windows has no
// group-wide kill signal; killProcessTree uses taskkill /T instead.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killProcessTree ends the shell and every process it started with
// "taskkill /T /F", then kills the shell directly in case taskkill could not
// run. Errors are ignored: the processes may already have exited.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// #nosec G204 -- fixed program and flags; the only argument is our child's PID.
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
}
