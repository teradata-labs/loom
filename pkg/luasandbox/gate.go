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

package luasandbox

import (
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
)

// Gate bounds concurrent runs per process and per key (a user or an agent).
// It never blocks: callers that find it full fail fast and fall back to
// direct tool calls, so a busy gate can never hold up a turn.
type Gate struct {
	mu        sync.Mutex
	maxTotal  int
	maxPerKey int
	total     int
	perKey    map[string]int
}

// NewGate returns a gate admitting maxTotal runs at once, at most maxPerKey
// for any one key. Values below 1 become 1.
func NewGate(maxTotal, maxPerKey int) *Gate {
	return &Gate{maxTotal: max(maxTotal, 1), maxPerKey: max(maxPerKey, 1), perKey: map[string]int{}}
}

// TryAcquire takes a slot for key. When ok is false no slot was taken.
// release returns the slot; calling it more than once is harmless.
func (g *Gate) TryAcquire(key string) (release func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.total >= g.maxTotal || g.perKey[key] >= g.maxPerKey {
		return func() {}, false
	}
	g.total++
	g.perKey[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.total--
			if g.perKey[key]--; g.perKey[key] <= 0 {
				delete(g.perKey, key)
			}
		})
	}, true
}

// Resize changes the limits. Runs already admitted keep their slots; new runs
// are refused until usage drops below the new limits.
func (g *Gate) Resize(maxTotal, maxPerKey int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.maxTotal, g.maxPerKey = max(maxTotal, 1), max(maxPerKey, 1)
}

// Stats reports slots in use and the current limits.
func (g *Gate) Stats() (inUse, maxTotal, maxPerKey int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.total, g.maxTotal, g.maxPerKey
}

// Capacity derivation constants.
const (
	// PeakMemoryOverhead is the measured ratio of peak process memory to a
	// run's memory budget: a budget-killed run reached 116 MB of resident
	// memory against a 50 MB budget, because growing buffers briefly hold
	// both the old and the new copy. GOMEMLIMIT does not lower it.
	PeakMemoryOverhead = 2.5
	// ScriptMemoryShare is the fraction of process memory concurrent runs may
	// use in the worst case.
	ScriptMemoryShare = 0.4
	// MinSlots and MaxSlots bound DeriveCapacity's slot count.
	MinSlots = 2
	MaxSlots = 16
	// minDerivedMemory is the smallest per-run budget DeriveCapacity returns.
	minDerivedMemory = 16 << 20
)

// DeriveCapacity sizes the gate from the process memory limit so that
// MaxSlots concurrent runs, each at its worst-case peak, use at most
// ScriptMemoryShare of memLimit. When even MinSlots runs do not fit at
// perRunBytes, it lowers the per-run budget instead of the slot count.
func DeriveCapacity(memLimit, perRunBytes uint64) (slots int, runBytes uint64) {
	if perRunBytes == 0 {
		perRunBytes = DefaultLimits().MemoryBytes
	}
	budget := ScriptMemoryShare * float64(memLimit)
	n := int(math.Floor(budget / (PeakMemoryOverhead * float64(perRunBytes))))
	if n >= MinSlots {
		return min(n, MaxSlots), perRunBytes
	}
	fit := uint64(budget / (PeakMemoryOverhead * MinSlots))
	return MinSlots, max(min(fit, perRunBytes), minDerivedMemory)
}

// ProcessMemoryLimit returns the memory the process may use: the cgroup limit
// when the process runs in a container, else GOMEMLIMIT when set, else 1 GiB.
func ProcessMemoryLimit() uint64 {
	if v, ok := cgroupMemoryLimit(cgroupMemoryFiles); ok {
		return v
	}
	if l := debug.SetMemoryLimit(-1); l > 0 && l < math.MaxInt64 {
		return uint64(l)
	}
	return 1 << 30
}

// cgroupMemoryFiles are cgroup v2 memory.max, then cgroup v1's limit file.
var cgroupMemoryFiles = []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"}

// cgroupMemoryLimit reads the first readable file. "max" and values of 2^60
// or more mean "unlimited".
func cgroupMemoryLimit(files []string) (uint64, bool) {
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(b))
		if s == "max" {
			return 0, false
		}
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil || v == 0 || v >= 1<<60 {
			return 0, false
		}
		return v, true
	}
	return 0, false
}
