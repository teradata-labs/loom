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
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGateLimits(t *testing.T) {
	g := NewGate(3, 2)
	r1, ok := g.TryAcquire("alice")
	require.True(t, ok)
	r2, ok := g.TryAcquire("alice")
	require.True(t, ok)
	_, ok = g.TryAcquire("alice")
	assert.False(t, ok, "per-key limit")
	r3, ok := g.TryAcquire("bob")
	require.True(t, ok)
	_, ok = g.TryAcquire("carol")
	assert.False(t, ok, "total limit")

	r1()
	r1() // double release is harmless
	inUse, _, _ := g.Stats()
	assert.Equal(t, 2, inUse)
	r4, ok := g.TryAcquire("carol")
	assert.True(t, ok)
	r2()
	r3()
	r4()
	inUse, _, _ = g.Stats()
	assert.Equal(t, 0, inUse)
}

func TestGateResize(t *testing.T) {
	g := NewGate(2, 2)
	r1, _ := g.TryAcquire("a")
	r2, _ := g.TryAcquire("b")
	g.Resize(1, 1)
	_, ok := g.TryAcquire("c")
	assert.False(t, ok, "admitted runs keep their slots")
	r1()
	_, ok = g.TryAcquire("c")
	assert.False(t, ok, "still at the new limit of one")
	r2()
	r3, ok := g.TryAcquire("c")
	assert.True(t, ok)
	r3()
	_, total, perKey := g.Stats()
	assert.Equal(t, 1, total)
	assert.Equal(t, 1, perKey)

	z := NewGate(0, -1)
	_, total, perKey = z.Stats()
	assert.Equal(t, 1, total, "limits below one become one")
	assert.Equal(t, 1, perKey)
}

func TestGateNeverBlocksAndNeverOverAdmits(t *testing.T) {
	g := NewGate(8, 3)
	var (
		active, peak, refused atomic.Int64
		wg                    sync.WaitGroup
	)
	start := time.Now()
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, ok := g.TryAcquire(fmt.Sprintf("user-%d", i%5))
			if !ok {
				refused.Add(1)
				return
			}
			n := active.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			active.Add(-1)
			release()
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, peak.Load(), int64(8))
	assert.Positive(t, refused.Load(), "a full gate refuses instead of queueing")
	assert.Less(t, time.Since(start), slack(500*time.Millisecond))
}

func TestDeriveCapacity(t *testing.T) {
	const gib, mib = uint64(1 << 30), uint64(1 << 20)
	tests := []struct {
		name     string
		mem, per uint64
		slots    int
		runBytes uint64
	}{
		{"4 GiB pod, 16 MiB runs", 4 * gib, 16 * mib, 8, 16 * mib},
		{"4 GiB pod, 32 MiB runs", 4 * gib, 32 * mib, 4, 32 * mib},
		{"4 GiB pod, 64 MiB runs", 4 * gib, 64 * mib, 2, 64 * mib},
		{"4 GiB pod lowers a 256 MiB budget", 4 * gib, 256 * mib, 2, 68 * mib},
		{"1 GiB pod lowers the per-run budget", gib, 64 * mib, 2, 17 * mib},
		{"huge pod is capped", 256 * gib, 16 * mib, MaxSlots, 16 * mib},
		{"tiny pod keeps a floor", 64 * mib, 256 * mib, 2, minDerivedMemory},
		{"zero per-run uses the default", 4 * gib, 0, 2, DefaultLimits().MemoryBytes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slots, runBytes := DeriveCapacity(tt.mem, tt.per)
			assert.Equal(t, tt.slots, slots)
			assert.InDelta(t, float64(tt.runBytes), float64(runBytes), float64(mib))
			worst := PeakMemoryOverhead * float64(slots) * float64(runBytes)
			if runBytes > minDerivedMemory {
				assert.LessOrEqual(t, worst, ScriptMemoryShare*float64(tt.mem)+1, "worst case stays within the share")
			}
		})
	}
}

func TestProcessMemoryLimit(t *testing.T) {
	assert.Positive(t, ProcessMemoryLimit())
}

func TestCgroupMemoryLimit(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
		return p
	}
	missing := filepath.Join(dir, "missing")
	tests := []struct {
		name  string
		files []string
		want  uint64
		ok    bool
	}{
		{"v2 limit", []string{write("v2", "4294967296\n")}, 4 << 30, true},
		{"v2 unlimited", []string{write("v2max", "max\n")}, 0, false},
		{"v1 fallback", []string{missing, write("v1", "1073741824")}, 1 << 30, true},
		{"v1 unlimited sentinel", []string{write("v1big", "9223372036854771712")}, 0, false},
		{"garbage", []string{write("bad", "lots")}, 0, false},
		{"zero", []string{write("zero", "0")}, 0, false},
		{"nothing readable", []string{missing}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := cgroupMemoryLimit(tt.files)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
