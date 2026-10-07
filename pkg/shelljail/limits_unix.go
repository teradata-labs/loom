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

//go:build !linux && !windows

package shelljail

import "syscall"

// applyLimits sets the limits this OS accepts. macOS rejects RLIMIT_DATA
// (design 06, Probe 10), so memory is bounded only by the heap watchdog here.
func applyLimits(l Limits) error {
	for _, lim := range []struct {
		res int
		v   uint64
	}{
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
	return syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{})
}
