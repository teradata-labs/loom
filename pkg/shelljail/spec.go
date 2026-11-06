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

// Package shelljail runs a shell command in a pure-Go shell interpreter
// (mvdan.cc/sh) inside a short-lived child process, where every program
// launch, file the shell opens, and directory it lists passes a
// shellpolicy check on the real, expanded arguments.
//
// The child is the host's own binary, started with MarkerEnv set; the host
// must call Main first thing in main(). The interpreter never runs in the
// server process: it has no memory budget of its own (design 06, F3), so the
// child carries operating-system limits (Linux) and a heap watchdog (every OS),
// and the host kills the child's whole process group when the call ends.
//
// The jail decides which programs start. It does not contain what a started
// program does; that is the job of an OS sandbox or a container.
package shelljail

import "github.com/teradata-labs/loom/pkg/shellpolicy"

// MarkerEnv marks a process started as a jail child.
const MarkerEnv = "LOOM_SHELLJAIL_CHILD"

const markerValue = "1"

// blockedMarker replaces a refused builtin's argv in the interpreter's call
// handler, so the refusal reaches the exec handler and fails that one command
// with status 126 instead of halting the whole run. A NUL byte cannot appear
// in a real program name.
const blockedMarker = "\x00loom-jail-blocked"

// maxSpecBytes bounds the spec a child reads from stdin.
const maxSpecBytes = 4 << 20

// Spec is everything the child needs, sent as JSON on its stdin (never on
// argv, so it does not show in a process listing).
type Spec struct {
	Command string             `json:"command"`
	Dir     string             `json:"dir"`
	Roots   shellpolicy.Roots  `json:"roots"`
	Policy  shellpolicy.Spec   `json:"policy"`
	Grant   *shellpolicy.Grant `json:"grant,omitempty"`
	// Env is the interpreter's starting environment (already filtered).
	Env []string `json:"env"`
	// Pinned maps each allowlisted program to the absolute path resolved at
	// startup; only that file is executed for the name.
	Pinned map[string]string `json:"pinned"`
	// SearchPath lists the directories a granted program name is looked up
	// in, and is every program's PATH.
	SearchPath []string `json:"search_path"`
	Limits     Limits   `json:"limits"`
	// Strict runs the interpreter with set -u.
	Strict bool `json:"strict"`
}

// Limits bound one jailed command. Zero means no limit of that kind.
type Limits struct {
	// MemoryBytes is RLIMIT_DATA on Linux (inherited by every program the
	// command starts) and, on every OS, the budget the heap watchdog enforces
	// on the interpreter (it trips at a third of it).
	MemoryBytes uint64 `json:"memory_bytes"`
	// FileBytes is RLIMIT_FSIZE: the largest file the command can write.
	FileBytes uint64 `json:"file_bytes"`
	// CPUSeconds is RLIMIT_CPU.
	CPUSeconds uint64 `json:"cpu_seconds"`
}

// Blocked is one launch, builtin, open or listing the jail refused.
type Blocked struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Pos    string `json:"pos,omitempty"`
}

// Result is what the child reports besides the command's own output and exit
// status. It travels on file descriptor 3.
type Result struct {
	Blocked    []Blocked `json:"blocked,omitempty"`
	EnvDropped []string  `json:"env_dropped,omitempty"`
	// Limit names the limit that ended the command: "memory", "cpu",
	// "file_size", or "timeout" (set by the host).
	Limit string `json:"limit,omitempty"`
	// Error is a failure of the jail itself (an unreadable spec).
	Error string `json:"error,omitempty"`
}
