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
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// CircuitState represents the state of the circuit breaker
type CircuitState int

const (
	// CircuitClosed - Normal operation, requests allowed
	CircuitClosed CircuitState = iota
	// CircuitOpen - Failing, requests blocked
	CircuitOpen
	// CircuitHalfOpen - Testing if recovered, limited requests
	CircuitHalfOpen
)

// String returns the string representation of the circuit state
func (cs CircuitState) String() string {
	switch cs {
	case CircuitClosed:
		return "CLOSED"
	case CircuitOpen:
		return "OPEN"
	case CircuitHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}

// CircuitBreaker implements the circuit breaker pattern for judge evaluations.
// It prevents cascading failures by temporarily blocking requests to failing judges.
//
// States:
//   - CLOSED: Normal operation, all requests allowed
//   - OPEN: Judge failing, all requests blocked
//   - HALF_OPEN: Testing recovery, limited requests allowed
//
// State transitions:
//   - CLOSED -> OPEN: After failure_threshold consecutive failures
//   - OPEN -> HALF_OPEN: After reset_timeout_ms elapsed
//   - HALF_OPEN -> CLOSED: After success_threshold consecutive successes
//   - HALF_OPEN -> OPEN: After any failure
type CircuitBreaker struct {
	config          *loomv1.CircuitBreakerConfig
	state           CircuitState
	failureCount    int32
	successCount    int32
	lastStateChange time.Time
	mu              sync.RWMutex
}

// NewCircuitBreaker creates a new circuit breaker with the given configuration
func NewCircuitBreaker(config *loomv1.CircuitBreakerConfig) *CircuitBreaker {
	if config == nil {
		config = DefaultCircuitBreakerConfig()
	}

	// Apply defaults
	if config.FailureThreshold == 0 {
		config.FailureThreshold = 5
	}
	if config.ResetTimeoutMs == 0 {
		config.ResetTimeoutMs = 60000 // 1 minute
	}
	if config.SuccessThreshold == 0 {
		config.SuccessThreshold = 2
	}

	return &CircuitBreaker{
		config:          config,
		state:           CircuitClosed,
		failureCount:    0,
		successCount:    0,
		lastStateChange: time.Now(),
	}
}

// DefaultCircuitBreakerConfig returns the default circuit breaker configuration
func DefaultCircuitBreakerConfig() *loomv1.CircuitBreakerConfig {
	return &loomv1.CircuitBreakerConfig{
		FailureThreshold: 5,
		ResetTimeoutMs:   60000, // 1 minute
		SuccessThreshold: 2,
		Enabled:          true,
	}
}

// IsOpen returns true if the circuit breaker is open (blocking requests)
func (cb *CircuitBreaker) IsOpen() bool {
	if cb.config == nil || !cb.config.Enabled {
		return false
	}

	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return cb.state == CircuitOpen
}

// AllowRequest returns true if the circuit breaker allows a request to proceed
func (cb *CircuitBreaker) AllowRequest() bool {
	if cb.config == nil || !cb.config.Enabled {
		return true
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		// Normal operation - allow all requests
		return true

	case CircuitOpen:
		// Check if reset timeout elapsed
		resetTimeout := time.Duration(cb.config.ResetTimeoutMs) * time.Millisecond
		if time.Since(cb.lastStateChange) > resetTimeout {
			// Transition to HALF_OPEN
			cb.state = CircuitHalfOpen
			cb.successCount = 0
			cb.failureCount = 0
			cb.lastStateChange = time.Now()
			return true
		}
		// Still open, block request
		return false

	case CircuitHalfOpen:
		// Allow limited requests to test recovery
		return true

	default:
		return true
	}
}

// RecordSuccess records a successful judge evaluation
func (cb *CircuitBreaker) RecordSuccess() {
	if cb.config == nil || !cb.config.Enabled {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount = 0

	switch cb.state {
	case CircuitHalfOpen:
		cb.successCount++
		if cb.successCount >= cb.config.SuccessThreshold {
			// Recovered! Close circuit
			cb.state = CircuitClosed
			cb.successCount = 0
			cb.lastStateChange = time.Now()
		}

	case CircuitClosed:
		// Normal success, no state change
	}
}

// RecordFailure records a failed judge evaluation
func (cb *CircuitBreaker) RecordFailure() {
	if cb.config == nil || !cb.config.Enabled {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.successCount = 0

	switch cb.state {
	case CircuitClosed:
		if cb.failureCount >= cb.config.FailureThreshold {
			// Too many failures, open circuit
			cb.state = CircuitOpen
			cb.lastStateChange = time.Now()
		}

	case CircuitHalfOpen:
		// Failed during recovery test, reopen circuit
		cb.state = CircuitOpen
		cb.lastStateChange = time.Now()

	case CircuitOpen:
		// Already open, no state change
	}
}

// GetState returns the current circuit breaker state
func (cb *CircuitBreaker) GetState() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Reset resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.state = CircuitClosed
	cb.failureCount = 0
	cb.successCount = 0
	cb.lastStateChange = time.Now()
}

// GetStats returns current circuit breaker statistics
func (cb *CircuitBreaker) GetStats() CircuitBreakerStats {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return CircuitBreakerStats{
		State:               cb.state,
		FailureCount:        cb.failureCount,
		SuccessCount:        cb.successCount,
		LastStateChange:     cb.lastStateChange,
		TimeSinceLastChange: time.Since(cb.lastStateChange),
	}
}

// CircuitBreakerStats contains circuit breaker statistics
type CircuitBreakerStats struct {
	State               CircuitState
	FailureCount        int32
	SuccessCount        int32
	LastStateChange     time.Time
	TimeSinceLastChange time.Duration
}
