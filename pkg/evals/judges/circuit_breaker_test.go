// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package judges

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestNewCircuitBreaker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         *loomv1.CircuitBreakerConfig
		expectedState  CircuitState
		expectedConfig *loomv1.CircuitBreakerConfig
	}{
		{
			name:          "nil config uses defaults",
			config:        nil,
			expectedState: CircuitClosed,
			expectedConfig: &loomv1.CircuitBreakerConfig{
				FailureThreshold: 5,
				ResetTimeoutMs:   60000,
				SuccessThreshold: 2,
				Enabled:          true,
			},
		},
		{
			name: "custom config applied",
			config: &loomv1.CircuitBreakerConfig{
				FailureThreshold: 3,
				ResetTimeoutMs:   30000,
				SuccessThreshold: 1,
				Enabled:          true,
			},
			expectedState: CircuitClosed,
			expectedConfig: &loomv1.CircuitBreakerConfig{
				FailureThreshold: 3,
				ResetTimeoutMs:   30000,
				SuccessThreshold: 1,
				Enabled:          true,
			},
		},
		{
			name: "zero values use defaults",
			config: &loomv1.CircuitBreakerConfig{
				FailureThreshold: 0,
				ResetTimeoutMs:   0,
				SuccessThreshold: 0,
				Enabled:          true,
			},
			expectedState: CircuitClosed,
			expectedConfig: &loomv1.CircuitBreakerConfig{
				FailureThreshold: 5,
				ResetTimeoutMs:   60000,
				SuccessThreshold: 2,
				Enabled:          true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cb := NewCircuitBreaker(tt.config)
			require.NotNil(t, cb)
			assert.Equal(t, tt.expectedState, cb.GetState())
			assert.Equal(t, tt.expectedConfig.FailureThreshold, cb.config.FailureThreshold)
			assert.Equal(t, tt.expectedConfig.ResetTimeoutMs, cb.config.ResetTimeoutMs)
			assert.Equal(t, tt.expectedConfig.SuccessThreshold, cb.config.SuccessThreshold)
		})
	}
}

func TestCircuitBreakerStateTransitions(t *testing.T) {
	t.Parallel()

	config := &loomv1.CircuitBreakerConfig{
		FailureThreshold: 3,
		ResetTimeoutMs:   100, // Short timeout for testing
		SuccessThreshold: 2,
		Enabled:          true,
	}

	t.Run("CLOSED to OPEN after threshold failures", func(t *testing.T) {
		t.Parallel()

		cb := NewCircuitBreaker(config)
		assert.Equal(t, CircuitClosed, cb.GetState())
		assert.True(t, cb.AllowRequest())

		// Record failures below threshold
		cb.RecordFailure()
		assert.Equal(t, CircuitClosed, cb.GetState())
		assert.True(t, cb.AllowRequest())

		cb.RecordFailure()
		assert.Equal(t, CircuitClosed, cb.GetState())
		assert.True(t, cb.AllowRequest())

		// Third failure should open circuit
		cb.RecordFailure()
		assert.Equal(t, CircuitOpen, cb.GetState())
		assert.False(t, cb.AllowRequest())
	})

	t.Run("OPEN to HALF_OPEN after timeout", func(t *testing.T) {
		t.Parallel()

		cb := NewCircuitBreaker(config)

		// Open circuit
		for i := 0; i < 3; i++ {
			cb.RecordFailure()
		}
		assert.Equal(t, CircuitOpen, cb.GetState())
		assert.False(t, cb.AllowRequest())

		// Wait for reset timeout
		time.Sleep(150 * time.Millisecond)

		// Next request should transition to HALF_OPEN
		assert.True(t, cb.AllowRequest())
		assert.Equal(t, CircuitHalfOpen, cb.GetState())
	})

	t.Run("HALF_OPEN to CLOSED after success threshold", func(t *testing.T) {
		t.Parallel()

		cb := NewCircuitBreaker(config)

		// Open circuit
		for i := 0; i < 3; i++ {
			cb.RecordFailure()
		}

		// Wait and transition to HALF_OPEN
		time.Sleep(150 * time.Millisecond)
		cb.AllowRequest()
		assert.Equal(t, CircuitHalfOpen, cb.GetState())

		// Record successes below threshold
		cb.RecordSuccess()
		assert.Equal(t, CircuitHalfOpen, cb.GetState())

		// Second success should close circuit
		cb.RecordSuccess()
		assert.Equal(t, CircuitClosed, cb.GetState())
		assert.True(t, cb.AllowRequest())
	})

	t.Run("HALF_OPEN to OPEN on failure", func(t *testing.T) {
		t.Parallel()

		cb := NewCircuitBreaker(config)

		// Open circuit
		for i := 0; i < 3; i++ {
			cb.RecordFailure()
		}

		// Wait and transition to HALF_OPEN
		time.Sleep(150 * time.Millisecond)
		cb.AllowRequest()
		assert.Equal(t, CircuitHalfOpen, cb.GetState())

		// Record one success
		cb.RecordSuccess()
		assert.Equal(t, CircuitHalfOpen, cb.GetState())

		// Failure should reopen circuit
		cb.RecordFailure()
		assert.Equal(t, CircuitOpen, cb.GetState())
		assert.False(t, cb.AllowRequest())
	})

	t.Run("success in CLOSED resets failure count", func(t *testing.T) {
		t.Parallel()

		cb := NewCircuitBreaker(config)

		// Record some failures (but below threshold)
		cb.RecordFailure()
		cb.RecordFailure()
		assert.Equal(t, CircuitClosed, cb.GetState())

		// Success should reset failure count
		cb.RecordSuccess()

		// Can now handle threshold failures again
		cb.RecordFailure()
		cb.RecordFailure()
		assert.Equal(t, CircuitClosed, cb.GetState())

		cb.RecordFailure()
		assert.Equal(t, CircuitOpen, cb.GetState())
	})
}

func TestCircuitBreakerDisabled(t *testing.T) {
	t.Parallel()

	config := &loomv1.CircuitBreakerConfig{
		FailureThreshold: 3,
		ResetTimeoutMs:   100,
		SuccessThreshold: 2,
		Enabled:          false, // Disabled
	}

	cb := NewCircuitBreaker(config)

	// Record many failures
	for i := 0; i < 10; i++ {
		cb.RecordFailure()
	}

	// Circuit should remain closed when disabled
	assert.False(t, cb.IsOpen())
	assert.True(t, cb.AllowRequest())
}

func TestCircuitBreakerReset(t *testing.T) {
	t.Parallel()

	config := &loomv1.CircuitBreakerConfig{
		FailureThreshold: 3,
		ResetTimeoutMs:   100,
		SuccessThreshold: 2,
		Enabled:          true,
	}

	cb := NewCircuitBreaker(config)

	// Open circuit
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}
	assert.Equal(t, CircuitOpen, cb.GetState())

	// Reset
	cb.Reset()

	// Should be closed with zero counts
	assert.Equal(t, CircuitClosed, cb.GetState())
	assert.True(t, cb.AllowRequest())

	stats := cb.GetStats()
	assert.Equal(t, int32(0), stats.FailureCount)
	assert.Equal(t, int32(0), stats.SuccessCount)
}

func TestCircuitBreakerGetStats(t *testing.T) {
	t.Parallel()

	config := &loomv1.CircuitBreakerConfig{
		FailureThreshold: 3,
		ResetTimeoutMs:   100,
		SuccessThreshold: 2,
		Enabled:          true,
	}

	cb := NewCircuitBreaker(config)

	// Initial stats
	stats := cb.GetStats()
	assert.Equal(t, CircuitClosed, stats.State)
	assert.Equal(t, int32(0), stats.FailureCount)
	assert.Equal(t, int32(0), stats.SuccessCount)

	// Record failures
	cb.RecordFailure()
	cb.RecordFailure()

	stats = cb.GetStats()
	assert.Equal(t, CircuitClosed, stats.State)
	assert.Equal(t, int32(2), stats.FailureCount)

	// Open circuit
	cb.RecordFailure()
	stats = cb.GetStats()
	assert.Equal(t, CircuitOpen, stats.State)
	assert.Equal(t, int32(3), stats.FailureCount)
}

func TestCircuitBreakerConcurrency(t *testing.T) {
	t.Parallel()

	config := &loomv1.CircuitBreakerConfig{
		FailureThreshold: 50,
		ResetTimeoutMs:   100,
		SuccessThreshold: 10,
		Enabled:          true,
	}

	cb := NewCircuitBreaker(config)

	// Concurrent failures
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 10; j++ {
				cb.RecordFailure()
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should have opened circuit
	assert.Equal(t, CircuitOpen, cb.GetState())
}

func TestCircuitBreakerStateString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state    CircuitState
		expected string
	}{
		{CircuitClosed, "CLOSED"},
		{CircuitOpen, "OPEN"},
		{CircuitHalfOpen, "HALF_OPEN"},
		{CircuitState(999), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, tt.state.String())
		})
	}
}

func TestDefaultCircuitBreakerConfig(t *testing.T) {
	t.Parallel()

	config := DefaultCircuitBreakerConfig()
	require.NotNil(t, config)

	assert.Equal(t, int32(5), config.FailureThreshold)
	assert.Equal(t, int32(60000), config.ResetTimeoutMs)
	assert.Equal(t, int32(2), config.SuccessThreshold)
	assert.True(t, config.Enabled)
}
