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

package shelljail

import (
	"io"
	"os"
	"os/exec"
	"syscall"
)

// resultFile is the child's end of the result pipe (fd 3). It is marked
// close-on-exec so the programs the command starts do not inherit it.
func resultFile() io.Writer {
	syscall.CloseOnExec(3)
	return os.NewFile(3, "shelljail-result")
}

// signalNumber returns the signal that ended a program, or 0.
func signalNumber(e *exec.ExitError) int {
	if ws, ok := e.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return int(ws.Signal())
	}
	return 0
}

// newProcessGroup starts the child as the leader of its own process group.
func newProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// supported reports whether jailed mode works on this OS.
const supported = true

// KillGroup kills the child's whole process group, then the child itself.
func KillGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
