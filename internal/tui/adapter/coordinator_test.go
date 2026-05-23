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
package adapter

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionAccumulator(t *testing.T) {
	t.Run("accumulates across multiple turns", func(t *testing.T) {
		accum := &sessionAccumulator{}

		// Turn 1
		accum.totalCost += 0.05
		accum.totalInputTokens += 1000
		accum.totalOutputTokens += 500

		// Turn 2
		accum.totalCost += 0.08
		accum.totalInputTokens += 1500
		accum.totalOutputTokens += 800

		// Turn 3
		accum.totalCost += 0.03
		accum.totalInputTokens += 800
		accum.totalOutputTokens += 300

		assert.InDelta(t, 0.16, accum.totalCost, 0.001)
		assert.Equal(t, int64(3300), accum.totalInputTokens)
		assert.Equal(t, int64(1600), accum.totalOutputTokens)
	})

	t.Run("concurrent access is safe under mutex", func(t *testing.T) {
		accum := make(map[string]*sessionAccumulator)
		accum["sess1"] = &sessionAccumulator{}
		var mu sync.Mutex
		var wg sync.WaitGroup

		// Simulate 100 concurrent turns updating the same session
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				mu.Lock()
				a := accum["sess1"]
				a.totalCost += 0.01
				a.totalInputTokens += 100
				a.totalOutputTokens += 50
				mu.Unlock()
			}()
		}
		wg.Wait()

		assert.InDelta(t, 1.0, accum["sess1"].totalCost, 0.001)
		assert.Equal(t, int64(10000), accum["sess1"].totalInputTokens)
		assert.Equal(t, int64(5000), accum["sess1"].totalOutputTokens)
	})

	t.Run("separate sessions accumulate independently", func(t *testing.T) {
		accum := make(map[string]*sessionAccumulator)
		accum["sess1"] = &sessionAccumulator{}
		accum["sess2"] = &sessionAccumulator{}

		accum["sess1"].totalCost += 0.10
		accum["sess1"].totalInputTokens += 2000

		accum["sess2"].totalCost += 0.20
		accum["sess2"].totalInputTokens += 3000

		assert.InDelta(t, 0.10, accum["sess1"].totalCost, 0.001)
		assert.Equal(t, int64(2000), accum["sess1"].totalInputTokens)
		assert.InDelta(t, 0.20, accum["sess2"].totalCost, 0.001)
		assert.Equal(t, int64(3000), accum["sess2"].totalInputTokens)
	})
}

func TestNewCoordinatorAdapterInitializesAccumulator(t *testing.T) {
	ca := NewCoordinatorAdapter(nil, nil)
	require.NotNil(t, ca.sessionAccum)
	assert.Empty(t, ca.sessionAccum)
}
