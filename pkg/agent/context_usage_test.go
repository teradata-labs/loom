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
package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestContextUsage_FoldAtIsG — the meter's denominator is G: the profile's
// critical percent of the window less the reply reservation. Not the window,
// and not the usable window.
func TestContextUsage_FoldAtIsG(t *testing.T) {
	dataIntensive := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE]
	tests := []struct {
		name string
		sm   *SegmentedMemory
		want int
	}{
		{"defaults: 90% of 200K less a 20K reply", NewSegmentedMemory("ROM", 200000, 20000), 162000},
		{"a larger reply reservation lowers G", NewSegmentedMemory("ROM", 200000, 64000), 122400},
		{"max_context_tokens moves G", NewSegmentedMemory("ROM", 900000, 90000), 729000},
		{"critical_threshold_percent moves G", NewSegmentedMemoryWithCompression("ROM", 200000, 20000, dataIntensive), 144000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, foldAt := tt.sm.ContextUsage()
			assert.Equal(t, tt.want, foldAt)
		})
	}
}

// TestContextUsage_UsedIsTheWholePromptAsTheProviderCountsIt — the numerator is
// everything that would be sent: the in-flight batch the fold trigger leaves
// out is in it, and it is scaled by the provider calibration.
func TestContextUsage_UsedIsTheWholePromptAsTheProviderCountsIt(t *testing.T) {
	sm := parallelBatchMemory(t, 200000, 20000, 10, "run_query")
	sm.mu.Lock()
	counted := sm.accurateTokensLocked(sm.compileLocked())
	trigger := sm.estimateOptLocked(true)
	sm.mu.Unlock()
	require.GreaterOrEqual(t, counted, calibrationMinSampleTokens)

	used, _ := sm.ContextUsage()
	assert.Equal(t, counted, used, "uncalibrated, used is the tokenizer's count of the compiled prompt")
	assert.Greater(t, used, trigger, "the in-flight batch is sent, so the meter counts it")

	for i := 0; i < 8; i++ {
		sm.ObservePromptTokens(counted * 5 / 4) // the provider counts 25% more
	}
	used, _ = sm.ContextUsage()
	assert.InDelta(t, float64(counted)*1.25, float64(used), float64(counted)*0.01)
}

// TestContextUsage_FallsWhenTheContextIsFolded — the reason the meter reads
// the context and not a running sum of what was billed.
func TestContextUsage_FallsWhenTheContextIsFolded(t *testing.T) {
	sm := newCompileMemory(t)
	sm.SetProtectedRecentTurns(2)
	seedInloopSession(sm)
	before, g := sm.ContextUsage()

	applied, _ := sm.ApplyFoldReply(context.Background(), "state of the work")
	require.True(t, applied)

	after, gAfter := sm.ContextUsage()
	assert.Less(t, after, before)
	assert.Positive(t, after)
	assert.Equal(t, g, gAfter, "a fold does not move G")
}

// TestAgentContextUsage — an agent answers for the sessions it holds, and says
// so when it holds none or the session has no segmented memory.
func TestAgentContextUsage(t *testing.T) {
	a := &Agent{memory: NewMemory()}

	_, _, ok := a.ContextUsage("nobody")
	assert.False(t, ok, "a session the agent does not hold has no context to measure")

	sess := a.memory.GetOrCreateSession(context.Background(), "s1")
	segMem := sessionSegmentedMemory(t, a, "s1")
	segMem.AddMessage(context.Background(), Message{Role: "user", Content: "hello there", Turn: 1})
	wantUsed, wantFoldAt := segMem.ContextUsage()
	used, foldAt, ok := a.ContextUsage("s1")
	require.True(t, ok)
	assert.Equal(t, wantUsed, used)
	assert.Equal(t, wantFoldAt, foldAt)
	assert.Positive(t, used)
	assert.Positive(t, foldAt)

	sess.SegmentedMem = nil
	_, _, ok = a.ContextUsage("s1")
	assert.False(t, ok, "a session without segmented memory has no context to measure")
}
