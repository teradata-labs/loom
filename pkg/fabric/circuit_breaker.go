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
package fabric

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// CircuitState represents the current state of the circuit breaker.
type CircuitState int

const (
	StateClosed   CircuitState = iota // Normal operation
	StateOpen                         // Failing - reject requests immediately
	StateHalfOpen                     // Testing - allow limited requests
)

func (s CircuitState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig defines circuit breaker behavior.
type CircuitBreakerConfig struct {
	FailureThreshold int           // Number of consecutive failures to open circuit (default: 5)
	SuccessThreshold int           // Number of consecutive successes to close from half-open (default: 2)
	Timeout          time.Duration // Time to wait before attempting half-open (default: 30s)
	OnStateChange    func(from, to CircuitState)
}

// DefaultCircuitBreakerConfig returns sensible defaults.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold: 5, // Allow retries before opening circuit
		SuccessThreshold: 2,
		Timeout:          30 * time.Second,
		OnStateChange:    nil,
	}
}

// CircuitBreaker implements the circuit breaker pattern to prevent cascading failures.
// It tracks failures per-tool and uses exponential backoff for automatic recovery.
type CircuitBreaker struct {
	mu               sync.RWMutex
	state            CircuitState
	failureCount     int
	successCount     int
	consecutiveOpens int // Number of times circuit has opened (for exponential backoff)
	lastFailureTime  time.Time
	lastStateChange  time.Time
	lastError        error // Last error that triggered a failure
	config           CircuitBreakerConfig
}

// NewCircuitBreaker creates a new circuit breaker.
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		state:           StateClosed,
		config:          config,
		lastStateChange: time.Now(),
	}
}

// Execute wraps an operation with circuit breaker logic.
func (cb *CircuitBreaker) Execute(operation func() error) error {
	// Check if we can proceed
	if err := cb.beforeRequest(); err != nil {
		return err
	}

	// Execute the operation
	err := operation()

	// Record the result
	cb.afterRequest(err)

	return err
}

// ExecuteEx wraps an operation with circuit breaker logic, with optional validation flag.
// When isValidation=true, errors are logged but do NOT count toward circuit breaker threshold.
// This is used for pre-flight validation checks that are expected to catch errors.
func (cb *CircuitBreaker) ExecuteEx(operation func() error, isValidation bool) error {
	// Check if we can proceed
	if err := cb.beforeRequest(); err != nil {
		return err
	}

	// Execute the operation
	err := operation()

	// Record the result (with validation flag)
	cb.afterRequestEx(err, isValidation)

	return err
}

// beforeRequest checks if the request should be allowed.
func (cb *CircuitBreaker) beforeRequest() error {
	cb.mu.RLock()
	state := cb.state
	lastFailure := cb.lastFailureTime
	cb.mu.RUnlock()

	switch state {
	case StateClosed:
		// Allow request
		return nil

	case StateOpen:
		// Calculate exponential backoff timeout
		timeout := cb.calculateTimeout()

		// Check if timeout has elapsed
		if time.Since(lastFailure) >= timeout {
			// Try half-open
			cb.setState(StateHalfOpen)
			zap.L().Info("circuit_breaker_half_open",
				zap.String("reason", "timeout_elapsed"),
				zap.Duration("elapsed", time.Since(lastFailure)),
				zap.Duration("timeout_used", timeout),
				zap.Int("consecutive_opens", cb.consecutiveOpens))
			return nil
		}

		// Still open, reject immediately
		timeRemaining := timeout - time.Since(lastFailure)
		return fmt.Errorf("circuit breaker open: too many consecutive failures (%d), retry after %v",
			cb.config.FailureThreshold,
			timeRemaining)

	case StateHalfOpen:
		// Allow limited requests
		return nil

	default:
		return fmt.Errorf("unknown circuit breaker state: %v", state)
	}
}

// afterRequest records the result and updates circuit state.
func (cb *CircuitBreaker) afterRequest(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err == nil {
		cb.onSuccess()
	} else {
		cb.onFailure(err)
	}
}

// afterRequestEx records the result with validation awareness.
// When isValidation=true, errors are logged but do NOT count toward threshold.
func (cb *CircuitBreaker) afterRequestEx(err error, isValidation bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err == nil {
		cb.onSuccess()
	} else {
		if isValidation {
			// Log validation error but don't count toward breaker threshold
			zap.L().Debug("circuit_breaker_validation_error",
				zap.Error(err),
				zap.String("reason", "validation_check_failed"),
				zap.String("note", "not counted toward threshold"))
			// Do NOT call onFailure - validation errors are expected
		} else {
			// Real execution error - count toward threshold
			cb.onFailure(err)
		}
	}
}

// onSuccess handles successful requests.
func (cb *CircuitBreaker) onSuccess() {
	switch cb.state {
	case StateClosed:
		// Reset failure count on success
		if cb.failureCount > 0 {
			zap.L().Debug("circuit_breaker_reset",
				zap.Int("previous_failures", cb.failureCount))
			cb.failureCount = 0
		}

	case StateHalfOpen:
		// Count successes to close circuit
		cb.successCount++
		zap.L().Info("circuit_breaker_half_open_success",
			zap.Int("success_count", cb.successCount),
			zap.Int("threshold", cb.config.SuccessThreshold))

		if cb.successCount >= cb.config.SuccessThreshold {
			cb.failureCount = 0
			cb.successCount = 0
			cb.consecutiveOpens = 0 // Reset exponential backoff on successful recovery
			cb.setStateLocked(StateClosed)
			zap.L().Info("circuit_breaker_closed",
				zap.String("reason", "success_threshold_reached"))
		}
	}
}

// onFailure handles failed requests.
func (cb *CircuitBreaker) onFailure(err error) {
	cb.failureCount++
	cb.lastFailureTime = time.Now()
	cb.lastError = err

	switch cb.state {
	case StateClosed:
		zap.L().Warn("circuit_breaker_failure",
			zap.Error(err),
			zap.Int("failure_count", cb.failureCount),
			zap.Int("threshold", cb.config.FailureThreshold))

		if cb.failureCount >= cb.config.FailureThreshold {
			cb.consecutiveOpens++ // Increment for exponential backoff
			cb.setStateLocked(StateOpen)
			timeout := cb.calculateTimeoutLocked()
			zap.L().Error("circuit_breaker_opened",
				zap.Int("consecutive_failures", cb.failureCount),
				zap.Int("consecutive_opens", cb.consecutiveOpens),
				zap.Duration("exponential_timeout", timeout))
		}

	case StateHalfOpen:
		// Failure in half-open immediately reopens circuit
		cb.setStateLocked(StateOpen)
		cb.successCount = 0
		zap.L().Warn("circuit_breaker_reopened",
			zap.Error(err),
			zap.String("reason", "half_open_failure"))
	}
}

// setState transitions to a new state (with locking).
func (cb *CircuitBreaker) setState(newState CircuitState) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.setStateLocked(newState)
}

// setStateLocked transitions to a new state (caller must hold lock).
func (cb *CircuitBreaker) setStateLocked(newState CircuitState) {
	if cb.state == newState {
		return
	}

	oldState := cb.state
	cb.state = newState
	cb.lastStateChange = time.Now()

	// Invoke callback if configured
	if cb.config.OnStateChange != nil {
		cb.config.OnStateChange(oldState, newState)
	}
}

// GetState returns the current circuit state (thread-safe).
func (cb *CircuitBreaker) GetState() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// GetStats returns current circuit breaker statistics.
func (cb *CircuitBreaker) GetStats() CircuitBreakerStats {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return CircuitBreakerStats{
		State:            cb.state,
		FailureCount:     cb.failureCount,
		SuccessCount:     cb.successCount,
		LastFailureTime:  cb.lastFailureTime,
		LastStateChange:  cb.lastStateChange,
		FailureThreshold: cb.config.FailureThreshold,
		SuccessThreshold: cb.config.SuccessThreshold,
		ConsecutiveOpens: cb.consecutiveOpens,
	}
}

// CircuitBreakerStats contains circuit breaker statistics.
type CircuitBreakerStats struct {
	State            CircuitState
	FailureCount     int
	SuccessCount     int
	LastFailureTime  time.Time
	LastStateChange  time.Time
	FailureThreshold int
	SuccessThreshold int
	ConsecutiveOpens int
}

// Reset manually resets the circuit breaker to closed state.
// This allows manual recovery without waiting for the timeout.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	oldState := cb.state
	cb.state = StateClosed
	cb.failureCount = 0
	cb.successCount = 0
	cb.lastFailureTime = time.Time{}
	cb.lastStateChange = time.Now()
	cb.consecutiveOpens = 0 // Reset exponential backoff

	zap.L().Info("circuit_breaker_manually_reset",
		zap.String("previous_state", oldState.String()),
		zap.String("new_state", "closed"))

	// Invoke callback if configured
	if cb.config.OnStateChange != nil && oldState != StateClosed {
		cb.config.OnStateChange(oldState, StateClosed)
	}
}

// calculateTimeout calculates exponential backoff timeout based on consecutive opens.
func (cb *CircuitBreaker) calculateTimeout() time.Duration {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.calculateTimeoutLocked()
}

// GetTimeout returns the current exponential backoff timeout.
func (cb *CircuitBreaker) GetTimeout() time.Duration {
	return cb.calculateTimeout()
}

// calculateTimeoutLocked calculates exponential backoff timeout (must hold lock).
// Uses configured timeout as base, scales exponentially: base, base*2, base*4, base*8, capped at 60s.
// Example with 30s default: 30s, 60s (capped)
// Example with 100ms (tests): 100ms, 200ms, 400ms, 800ms, etc.
func (cb *CircuitBreaker) calculateTimeoutLocked() time.Duration {
	baseDelay := cb.config.Timeout

	if cb.consecutiveOpens <= 0 {
		return cb.config.Timeout // Fallback to static timeout
	}

	// Calculate exponential: baseDelay * 2^(consecutiveOpens-1)
	delay := baseDelay * (1 << uint(cb.consecutiveOpens-1))

	// Cap at 60 seconds
	maxDelay := 60 * time.Second
	if delay > maxDelay {
		delay = maxDelay
	}

	return delay
}

// --- Circuit Breaker Manager ---

// CircuitBreakerManager manages per-tool circuit breakers to prevent one failing tool
// from blocking all other tools. Each tool gets its own circuit breaker with independent state.
type CircuitBreakerManager struct {
	mu       sync.RWMutex
	breakers map[string]*CircuitBreaker
	config   CircuitBreakerConfig
}

// NewCircuitBreakerManager creates a new manager with the given default config.
func NewCircuitBreakerManager(config CircuitBreakerConfig) *CircuitBreakerManager {
	return &CircuitBreakerManager{
		breakers: make(map[string]*CircuitBreaker),
		config:   config,
	}
}

// GetBreaker returns the circuit breaker for the given tool name, creating one if needed.
// This is thread-safe and uses double-checked locking for performance.
func (m *CircuitBreakerManager) GetBreaker(toolName string) *CircuitBreaker {
	// Fast path: read lock to check if breaker exists
	m.mu.RLock()
	breaker, exists := m.breakers[toolName]
	m.mu.RUnlock()

	if exists {
		return breaker
	}

	// Slow path: write lock to create new breaker
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check: another goroutine might have created it while we waited
	if breaker, exists := m.breakers[toolName]; exists {
		return breaker
	}

	// Create new circuit breaker for this tool
	breaker = NewCircuitBreaker(m.config)
	m.breakers[toolName] = breaker
	return breaker
}

// GetAllStats returns statistics for all circuit breakers.
// Returns a map of tool name -> circuit breaker stats.
func (m *CircuitBreakerManager) GetAllStats() map[string]CircuitBreakerStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make(map[string]CircuitBreakerStats, len(m.breakers))
	for toolName, breaker := range m.breakers {
		stats[toolName] = breaker.GetStats()
	}
	return stats
}

// Reset resets the circuit breaker for a specific tool.
func (m *CircuitBreakerManager) Reset(toolName string) {
	m.mu.RLock()
	breaker, exists := m.breakers[toolName]
	m.mu.RUnlock()

	if exists {
		breaker.Reset()
	}
}

// ResetAll resets all circuit breakers.
func (m *CircuitBreakerManager) ResetAll() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, breaker := range m.breakers {
		breaker.Reset()
	}
}

// --- Helper Functions for Error Classification ---

// ClassifyError attempts to determine error type from error message.
// This can be used by backends to categorize errors for better corrections.
func ClassifyError(err error) string {
	if err == nil {
		return "success"
	}

	errMsg := strings.ToLower(err.Error())

	if strings.Contains(errMsg, "syntax") {
		return "syntax_error"
	}
	if strings.Contains(errMsg, "timeout") {
		return "timeout"
	}
	if strings.Contains(errMsg, "connect") || strings.Contains(errMsg, "network") {
		return "connection"
	}
	if strings.Contains(errMsg, "permission") || strings.Contains(errMsg, "access denied") {
		return "permission_denied"
	}
	if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "does not exist") {
		if strings.Contains(errMsg, "table") || strings.Contains(errMsg, "object") {
			return "table_not_found"
		}
		if strings.Contains(errMsg, "column") {
			return "column_not_found"
		}
	}

	return "unknown"
}
