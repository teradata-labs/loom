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

//go:build linux

package shelljail

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"syscall"
)

// applyLimits sets the child's resource limits, which every program it starts
// inherits, and marks it first in line for the kernel's out-of-memory killer.
func applyLimits(l Limits) error {
	for _, lim := range []struct {
		res int
		v   uint64
	}{
		{syscall.RLIMIT_DATA, dataLimit(l.MemoryBytes, raceBuild, currentVmData)},
		{syscall.RLIMIT_FSIZE, l.FileBytes},
		{syscall.RLIMIT_CPU, l.CPUSeconds},
	} {
		if lim.v == 0 {
			continue
		}
		if err := syscall.Setrlimit(lim.res, &syscall.Rlimit{Cur: lim.v, Max: lim.v}); err != nil {
			return err
		}
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{}); err != nil {
		return err
	}
	// Best effort: a kernel without the file, or a container that forbids
	// raising it, still runs the jail.
	_ = os.WriteFile("/proc/self/oom_score_adj", []byte("1000"), 0)
	return nil
}

// dataLimit is the RLIMIT_DATA the child sets. RLIMIT_DATA counts the mappings
// the process already has, and the child sets it on itself after it starts.
// A normal build starts with about 40 MiB, so memoryBytes is the limit. A
// -race build starts with the ThreadSanitizer's mappings (about 1.1 GiB on
// linux/amd64, more than the 512 MiB default), and every later allocation
// failed: the jail child died with exit 2 or 66 before it ran anything. In a
// race build only, memoryBytes is counted from that starting point instead.
// The watchdog, which reads the Go heap, is the same in both builds.
func dataLimit(memoryBytes uint64, race bool, vmData func() (uint64, bool)) uint64 {
	if memoryBytes == 0 || !race {
		return memoryBytes
	}
	base, ok := vmData()
	if !ok {
		return memoryBytes
	}
	return memoryBytes + base
}

// currentVmData is the process's VmData from /proc/self/status, in bytes.
func currentVmData() (uint64, bool) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	return parseVmData(b)
}

// parseVmData reads the "VmData:  <n> kB" line of /proc/<pid>/status.
func parseVmData(status []byte) (uint64, bool) {
	sc := bufio.NewScanner(bytes.NewReader(status))
	for sc.Scan() {
		f := bytes.Fields(sc.Bytes())
		if len(f) == 3 && string(f[0]) == "VmData:" && string(f[2]) == "kB" {
			kb, err := strconv.ParseUint(string(f[1]), 10, 64)
			if err != nil {
				return 0, false
			}
			return kb << 10, true
		}
	}
	return 0, false
}
