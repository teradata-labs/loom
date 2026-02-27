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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// poolTestAgent creates a minimal Agent with no backend and no LLM for pool testing.
func poolTestAgent() *Agent {
	return NewAgent(nil, newRoleMockLLM("default", "default-model"))
}

// TestSetActiveProvider verifies switching to named providers in the pool.
func TestSetActiveProvider(t *testing.T) {
	t.Run("switches to named provider", func(t *testing.T) {
		a := poolTestAgent()
		pool := map[string]LLMProvider{
			"claude": newRoleMockLLM("anthropic", "claude-opus"),
			"llama":  newRoleMockLLM("ollama", "llama3"),
		}
		err := a.SetProviderPool(pool, "", nil)
		require.NoError(t, err)

		err = a.SetActiveProvider("claude")
		require.NoError(t, err)
		assert.Equal(t, "claude", a.GetActiveProviderName())
		assert.Equal(t, "anthropic", a.GetLLMProviderName())
		assert.Equal(t, "claude-opus", a.GetLLMModel())
	})

	t.Run("switches between providers", func(t *testing.T) {
		a := poolTestAgent()
		pool := map[string]LLMProvider{
			"claude": newRoleMockLLM("anthropic", "claude-opus"),
			"llama":  newRoleMockLLM("ollama", "llama3"),
		}
		require.NoError(t, a.SetProviderPool(pool, "claude", nil))
		assert.Equal(t, "claude", a.GetActiveProviderName())

		require.NoError(t, a.SetActiveProvider("llama"))
		assert.Equal(t, "llama", a.GetActiveProviderName())
		assert.Equal(t, "ollama", a.GetLLMProviderName())
		assert.Equal(t, "llama3", a.GetLLMModel())
	})

	t.Run("returns error for unknown provider", func(t *testing.T) {
		a := poolTestAgent()
		pool := map[string]LLMProvider{
			"claude": newRoleMockLLM("anthropic", "claude-opus"),
		}
		require.NoError(t, a.SetProviderPool(pool, "", nil))

		err := a.SetActiveProvider("nonexistent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found in pool")
	})

	t.Run("returns error when pool not configured", func(t *testing.T) {
		a := poolTestAgent()
		err := a.SetActiveProvider("anything")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "provider pool not configured")
	})

	t.Run("respects allowed_providers list", func(t *testing.T) {
		a := poolTestAgent()
		pool := map[string]LLMProvider{
			"claude": newRoleMockLLM("anthropic", "claude-opus"),
			"llama":  newRoleMockLLM("ollama", "llama3"),
			"gpt4":   newRoleMockLLM("openai", "gpt-4"),
		}
		// Only allow claude and llama
		allowed := []string{"claude", "llama"}
		require.NoError(t, a.SetProviderPool(pool, "", allowed))

		// Allowed providers should succeed
		require.NoError(t, a.SetActiveProvider("claude"))
		require.NoError(t, a.SetActiveProvider("llama"))

		// Non-allowed provider should fail even though it's in the pool
		err := a.SetActiveProvider("gpt4")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not in allowed providers list")
	})

	t.Run("SetProviderPool with active sets provider immediately", func(t *testing.T) {
		a := poolTestAgent()
		pool := map[string]LLMProvider{
			"fast": newRoleMockLLM("ollama", "llama3"),
		}
		err := a.SetProviderPool(pool, "fast", nil)
		require.NoError(t, err)
		assert.Equal(t, "fast", a.GetActiveProviderName())
		assert.Equal(t, "ollama", a.GetLLMProviderName())
	})

	t.Run("GetProviderPool returns nil when not configured", func(t *testing.T) {
		a := poolTestAgent()
		assert.Nil(t, a.GetProviderPool())
	})

	t.Run("GetProviderPool returns pool after configuration", func(t *testing.T) {
		a := poolTestAgent()
		pool := map[string]LLMProvider{
			"test": newRoleMockLLM("test-provider", "test-model"),
		}
		require.NoError(t, a.SetProviderPool(pool, "", nil))
		got := a.GetProviderPool()
		require.NotNil(t, got)
		assert.Len(t, got, 1)
		assert.Contains(t, got, "test")
	})

	t.Run("concurrent SetActiveProvider is race-free", func(t *testing.T) {
		a := poolTestAgent()
		pool := map[string]LLMProvider{
			"p1": newRoleMockLLM("provider1", "model1"),
			"p2": newRoleMockLLM("provider2", "model2"),
		}
		require.NoError(t, a.SetProviderPool(pool, "", nil))

		var wg sync.WaitGroup
		var errCount atomic.Int32
		const goroutines = 20

		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				name := "p1"
				if idx%2 == 0 {
					name = "p2"
				}
				if err := a.SetActiveProvider(name); err != nil {
					errCount.Add(1)
				}
				_ = a.GetActiveProviderName()
				_ = a.GetLLMProviderName()
				_ = a.GetLLMModel()
			}(i)
		}
		wg.Wait()
		assert.Equal(t, int32(0), errCount.Load(), "no errors expected during concurrent access")
	})

	t.Run("concurrent GetProviderPool is race-free", func(t *testing.T) {
		a := poolTestAgent()
		pool := map[string]LLMProvider{
			"alpha": newRoleMockLLM("anthropic", "claude-sonnet"),
		}
		require.NoError(t, a.SetProviderPool(pool, "alpha", nil))

		var wg sync.WaitGroup
		const goroutines = 20
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p := a.GetProviderPool()
				assert.NotNil(t, p)
				_ = a.GetActiveProviderName()
			}()
		}
		wg.Wait()
	})
}
