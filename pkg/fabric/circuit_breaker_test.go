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
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestNewCircuitBreaker(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	cb := NewCircuitBreaker(config)

	if cb == nil {
		t.Fatal("NewCircuitBreaker returned nil")
	}

	if cb.state != StateClosed {
		t.Errorf("expected initial state Closed, got %v", cb.state)
	}

	if cb.failureCount != 0 {
		t.Errorf("expected failureCount 0, got %d", cb.failureCount)
	}

	if cb.successCount != 0 {
		t.Errorf("expected successCount 0, got %d", cb.successCount)
	}

	if cb.consecutiveOpens != 0 {
		t.Errorf("expected consecutiveOpens 0, got %d", cb.consecutiveOpens)
	}
}

func TestDefaultCircuitBreakerConfig(t *testing.T) {
	config := DefaultCircuitBreakerConfig()

	if config.FailureThreshold != 5 {
		t.Errorf("expected FailureThreshold 5, got %d", config.FailureThreshold)
	}

	if config.SuccessThreshold != 2 {
		t.Errorf("expected SuccessThreshold 2, got %d", config.SuccessThreshold)
	}

	if config.Timeout != 30*time.Second {
		t.Errorf("expected Timeout 30s, got %v", config.Timeout)
	}
}

func TestCircuitStateString(t *testing.T) {
	tests := []struct {
		state CircuitState
		want  string
	}{
		{StateClosed, "closed"},
		{StateOpen, "open"},
		{StateHalfOpen, "half-open"},
		{CircuitState(999), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.state.String()
			if got != tt.want {
				t.Errorf("CircuitState.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecuteSuccess(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	cb := NewCircuitBreaker(config)

	operation := func() error {
		return nil
	}

	err := cb.Execute(operation)

	if err != nil {
		t.Errorf("Execute() returned error: %v", err)
	}

	if cb.failureCount != 0 {
		t.Errorf("expected failureCount 0, got %d", cb.failureCount)
	}

	if cb.state != StateClosed {
		t.Errorf("expected state Closed, got %v", cb.state)
	}
}

func TestExecuteFailure(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 3
	cb := NewCircuitBreaker(config)

	testError := errors.New("test error")

	operation := func() error {
		return testError
	}

	// First failure
	err := cb.Execute(operation)
	if err != testError {
		t.Errorf("expected error %v, got %v", testError, err)
	}
	if cb.failureCount != 1 {
		t.Errorf("expected failureCount 1, got %d", cb.failureCount)
	}
	if cb.state != StateClosed {
		t.Errorf("expected state Closed after 1 failure, got %v", cb.state)
	}

	// Second failure
	err = cb.Execute(operation)
	if err != testError {
		t.Errorf("expected error %v, got %v", testError, err)
	}
	if cb.failureCount != 2 {
		t.Errorf("expected failureCount 2, got %d", cb.failureCount)
	}
	if cb.state != StateClosed {
		t.Errorf("expected state Closed after 2 failures, got %v", cb.state)
	}

	// Third failure - should open circuit
	err = cb.Execute(operation)
	if err != testError {
		t.Errorf("expected error %v, got %v", testError, err)
	}
	if cb.failureCount != 3 {
		t.Errorf("expected failureCount 3, got %d", cb.failureCount)
	}
	if cb.state != StateOpen {
		t.Errorf("expected state Open after threshold, got %v", cb.state)
	}
	if cb.consecutiveOpens != 1 {
		t.Errorf("expected consecutiveOpens 1, got %d", cb.consecutiveOpens)
	}
}

func TestExecuteCircuitOpen(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 2
	config.Timeout = 100 * time.Millisecond
	cb := NewCircuitBreaker(config)

	testError := errors.New("test error")

	// Fail enough to open circuit
	for i := 0; i < config.FailureThreshold; i++ {
		_ = cb.Execute(func() error { return testError })
	}

	if cb.state != StateOpen {
		t.Fatalf("circuit not open after failures")
	}

	// Try to execute - should be rejected immediately
	err := cb.Execute(func() error { return nil })

	if err == nil {
		t.Fatal("expected error from open circuit, got nil")
	}

	if cb.state != StateOpen {
		t.Errorf("expected state to remain Open, got %v", cb.state)
	}
}

func TestExecuteHalfOpenSuccess(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 2
	config.SuccessThreshold = 2
	config.Timeout = 50 * time.Millisecond
	cb := NewCircuitBreaker(config)

	testError := errors.New("test error")

	// Open the circuit
	for i := 0; i < config.FailureThreshold; i++ {
		_ = cb.Execute(func() error { return testError })
	}

	if cb.state != StateOpen {
		t.Fatalf("circuit not open")
	}

	// Wait for timeout
	time.Sleep(config.Timeout + 10*time.Millisecond)

	// First success in half-open - should allow request
	err := cb.Execute(func() error { return nil })
	if err != nil {
		t.Errorf("expected success, got error: %v", err)
	}

	if cb.state != StateHalfOpen {
		t.Errorf("expected state HalfOpen, got %v", cb.state)
	}

	if cb.successCount != 1 {
		t.Errorf("expected successCount 1, got %d", cb.successCount)
	}

	// Second success - should close circuit
	err = cb.Execute(func() error { return nil })
	if err != nil {
		t.Errorf("expected success, got error: %v", err)
	}

	if cb.state != StateClosed {
		t.Errorf("expected state Closed, got %v", cb.state)
	}

	if cb.consecutiveOpens != 0 {
		t.Errorf("expected consecutiveOpens reset to 0, got %d", cb.consecutiveOpens)
	}
}

func TestExecuteHalfOpenFailure(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 2
	config.Timeout = 50 * time.Millisecond
	cb := NewCircuitBreaker(config)

	testError := errors.New("test error")

	// Open the circuit
	for i := 0; i < config.FailureThreshold; i++ {
		_ = cb.Execute(func() error { return testError })
	}

	// Wait for timeout
	time.Sleep(config.Timeout + 10*time.Millisecond)

	// Failure in half-open - should reopen circuit
	err := cb.Execute(func() error { return testError })
	if err != testError {
		t.Errorf("expected test error, got %v", err)
	}

	if cb.state != StateOpen {
		t.Errorf("expected state Open, got %v", cb.state)
	}

	if cb.successCount != 0 {
		t.Errorf("expected successCount reset to 0, got %d", cb.successCount)
	}
}

func TestExecuteExValidationMode(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 2
	cb := NewCircuitBreaker(config)

	testError := errors.New("validation error")

	// Validation errors should NOT count toward threshold
	for i := 0; i < 10; i++ {
		err := cb.ExecuteEx(func() error { return testError }, true) // isValidation=true
		if err != testError {
			t.Errorf("expected error %v, got %v", testError, err)
		}
	}

	// Circuit should still be closed
	if cb.state != StateClosed {
		t.Errorf("expected state Closed after validation errors, got %v", cb.state)
	}

	if cb.failureCount != 0 {
		t.Errorf("expected failureCount 0 (validation errors don't count), got %d", cb.failureCount)
	}

	// Now test real execution errors
	for i := 0; i < config.FailureThreshold; i++ {
		_ = cb.ExecuteEx(func() error { return testError }, false) // isValidation=false
	}

	// Circuit should now be open
	if cb.state != StateOpen {
		t.Errorf("expected state Open after real errors, got %v", cb.state)
	}
}

func TestReset(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 2
	cb := NewCircuitBreaker(config)

	testError := errors.New("test error")

	// Open the circuit
	for i := 0; i < config.FailureThreshold; i++ {
		_ = cb.Execute(func() error { return testError })
	}

	if cb.state != StateOpen {
		t.Fatalf("circuit not open")
	}

	// Reset
	cb.Reset()

	// Verify reset
	if cb.state != StateClosed {
		t.Errorf("expected state Closed after reset, got %v", cb.state)
	}

	if cb.failureCount != 0 {
		t.Errorf("expected failureCount 0 after reset, got %d", cb.failureCount)
	}

	if cb.successCount != 0 {
		t.Errorf("expected successCount 0 after reset, got %d", cb.successCount)
	}

	if cb.consecutiveOpens != 0 {
		t.Errorf("expected consecutiveOpens 0 after reset, got %d", cb.consecutiveOpens)
	}
}

func TestGetState(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	cb := NewCircuitBreaker(config)

	if cb.GetState() != StateClosed {
		t.Errorf("expected GetState() Closed, got %v", cb.GetState())
	}

	// Manually set state to test getter
	cb.setState(StateOpen)

	if cb.GetState() != StateOpen {
		t.Errorf("expected GetState() Open, got %v", cb.GetState())
	}
}

func TestGetStats(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 3
	cb := NewCircuitBreaker(config)

	testError := errors.New("test error")

	// Generate some failures
	for i := 0; i < 2; i++ {
		_ = cb.Execute(func() error { return testError })
	}

	stats := cb.GetStats()

	if stats.State != StateClosed {
		t.Errorf("expected State Closed, got %v", stats.State)
	}

	if stats.FailureCount != 2 {
		t.Errorf("expected FailureCount 2, got %d", stats.FailureCount)
	}

	if stats.FailureThreshold != config.FailureThreshold {
		t.Errorf("expected FailureThreshold %d, got %d", config.FailureThreshold, stats.FailureThreshold)
	}

	if stats.SuccessThreshold != config.SuccessThreshold {
		t.Errorf("expected SuccessThreshold %d, got %d", config.SuccessThreshold, stats.SuccessThreshold)
	}
}

func TestCalculateTimeoutExponential(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.Timeout = 5 * time.Second
	cb := NewCircuitBreaker(config)

	tests := []struct {
		name             string
		consecutiveOpens int
		wantTimeout      time.Duration
	}{
		{
			name:             "no opens",
			consecutiveOpens: 0,
			wantTimeout:      5 * time.Second, // Base timeout
		},
		{
			name:             "first open",
			consecutiveOpens: 1,
			wantTimeout:      5 * time.Second, // base * 2^0 = 5s
		},
		{
			name:             "second open",
			consecutiveOpens: 2,
			wantTimeout:      10 * time.Second, // base * 2^1 = 10s
		},
		{
			name:             "third open",
			consecutiveOpens: 3,
			wantTimeout:      20 * time.Second, // base * 2^2 = 20s
		},
		{
			name:             "fourth open",
			consecutiveOpens: 4,
			wantTimeout:      40 * time.Second, // base * 2^3 = 40s
		},
		{
			name:             "fifth open (capped)",
			consecutiveOpens: 5,
			wantTimeout:      60 * time.Second, // Capped at 60s
		},
		{
			name:             "tenth open (capped)",
			consecutiveOpens: 10,
			wantTimeout:      60 * time.Second, // Still capped at 60s
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb.consecutiveOpens = tt.consecutiveOpens

			got := cb.GetTimeout()

			if got != tt.wantTimeout {
				t.Errorf("GetTimeout() = %v, want %v", got, tt.wantTimeout)
			}
		})
	}
}

func TestCalculateTimeoutWithShortBase(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.Timeout = 100 * time.Millisecond // Short base for testing
	cb := NewCircuitBreaker(config)

	tests := []struct {
		name             string
		consecutiveOpens int
		wantTimeout      time.Duration
	}{
		{
			name:             "first open",
			consecutiveOpens: 1,
			wantTimeout:      100 * time.Millisecond, // 100ms
		},
		{
			name:             "second open",
			consecutiveOpens: 2,
			wantTimeout:      200 * time.Millisecond, // 200ms
		},
		{
			name:             "third open",
			consecutiveOpens: 3,
			wantTimeout:      400 * time.Millisecond, // 400ms
		},
		{
			name:             "fourth open",
			consecutiveOpens: 4,
			wantTimeout:      800 * time.Millisecond, // 800ms
		},
		{
			name:             "many opens (capped)",
			consecutiveOpens: 20,
			wantTimeout:      60 * time.Second, // Capped at 60s
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb.consecutiveOpens = tt.consecutiveOpens

			got := cb.GetTimeout()

			if got != tt.wantTimeout {
				t.Errorf("GetTimeout() = %v, want %v", got, tt.wantTimeout)
			}
		})
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantType string
	}{
		{
			name:     "nil error",
			err:      nil,
			wantType: "success",
		},
		{
			name:     "syntax error",
			err:      errors.New("syntax error at position 10"),
			wantType: "syntax_error",
		},
		{
			name:     "timeout",
			err:      errors.New("query timeout exceeded"),
			wantType: "timeout",
		},
		{
			name:     "connection error",
			err:      errors.New("connection refused"),
			wantType: "connection",
		},
		{
			name:     "network error",
			err:      errors.New("network unreachable"),
			wantType: "connection",
		},
		{
			name:     "permission denied",
			err:      errors.New("permission denied"),
			wantType: "permission_denied",
		},
		{
			name:     "access denied",
			err:      errors.New("access denied for user"),
			wantType: "permission_denied",
		},
		{
			name:     "table not found",
			err:      errors.New("table not found: customers"),
			wantType: "table_not_found",
		},
		{
			name:     "object not found",
			err:      errors.New("object does not exist"),
			wantType: "table_not_found",
		},
		{
			name:     "column not found",
			err:      errors.New("column not found: email"),
			wantType: "column_not_found",
		},
		{
			name:     "unknown error",
			err:      errors.New("something went wrong"),
			wantType: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err)

			if got != tt.wantType {
				t.Errorf("ClassifyError() = %q, want %q", got, tt.wantType)
			}
		})
	}
}

func TestNewCircuitBreakerManager(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	manager := NewCircuitBreakerManager(config)

	if manager == nil {
		t.Fatal("NewCircuitBreakerManager returned nil")
	}

	if manager.breakers == nil {
		t.Error("breakers map not initialized")
	}
}

func TestCircuitBreakerManagerGetBreaker(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	manager := NewCircuitBreakerManager(config)

	toolName := "test_tool"

	// First call should create breaker
	breaker1 := manager.GetBreaker(toolName)

	if breaker1 == nil {
		t.Fatal("GetBreaker returned nil")
	}

	// Second call should return same breaker
	breaker2 := manager.GetBreaker(toolName)

	if breaker1 != breaker2 {
		t.Error("GetBreaker returned different breakers for same tool")
	}

	// Different tool should get different breaker
	breaker3 := manager.GetBreaker("another_tool")

	if breaker1 == breaker3 {
		t.Error("GetBreaker returned same breaker for different tools")
	}
}

func TestCircuitBreakerManagerGetAllStats(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	manager := NewCircuitBreakerManager(config)

	// Create breakers for multiple tools
	manager.GetBreaker("tool1")
	manager.GetBreaker("tool2")
	manager.GetBreaker("tool3")

	stats := manager.GetAllStats()

	if len(stats) != 3 {
		t.Errorf("expected 3 stats entries, got %d", len(stats))
	}

	if _, ok := stats["tool1"]; !ok {
		t.Error("stats missing tool1")
	}

	if _, ok := stats["tool2"]; !ok {
		t.Error("stats missing tool2")
	}

	if _, ok := stats["tool3"]; !ok {
		t.Error("stats missing tool3")
	}
}

func TestCircuitBreakerManagerReset(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 2
	manager := NewCircuitBreakerManager(config)

	toolName := "test_tool"
	breaker := manager.GetBreaker(toolName)

	testError := errors.New("test error")

	// Open the circuit
	for i := 0; i < config.FailureThreshold; i++ {
		_ = breaker.Execute(func() error { return testError })
	}

	if breaker.GetState() != StateOpen {
		t.Fatalf("circuit not open")
	}

	// Reset via manager
	manager.Reset(toolName)

	if breaker.GetState() != StateClosed {
		t.Errorf("expected state Closed after reset, got %v", breaker.GetState())
	}
}

func TestCircuitBreakerManagerResetAll(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 2
	manager := NewCircuitBreakerManager(config)

	testError := errors.New("test error")

	// Create and open multiple breakers
	breaker1 := manager.GetBreaker("tool1")
	breaker2 := manager.GetBreaker("tool2")

	for i := 0; i < config.FailureThreshold; i++ {
		_ = breaker1.Execute(func() error { return testError })
		_ = breaker2.Execute(func() error { return testError })
	}

	if breaker1.GetState() != StateOpen || breaker2.GetState() != StateOpen {
		t.Fatal("circuits not open")
	}

	// Reset all
	manager.ResetAll()

	if breaker1.GetState() != StateClosed {
		t.Errorf("expected breaker1 Closed after ResetAll, got %v", breaker1.GetState())
	}

	if breaker2.GetState() != StateClosed {
		t.Errorf("expected breaker2 Closed after ResetAll, got %v", breaker2.GetState())
	}
}

// TestConcurrentExecute tests concurrent Execute calls with -race detector
func TestConcurrentExecute(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 100 // High threshold to avoid opening during test
	cb := NewCircuitBreaker(config)

	const numGoroutines = 10
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				operation := func() error {
					if (workerID+j)%2 == 0 {
						return nil
					}
					return errors.New("test error")
				}

				_ = cb.Execute(operation)
			}
		}(i)
	}

	wg.Wait()
}

// TestConcurrentGetState tests concurrent state reads with -race detector
func TestConcurrentGetState(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	cb := NewCircuitBreaker(config)

	const numGoroutines = 10
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				_ = cb.GetState()
			}
		}()
	}

	wg.Wait()
}

// TestConcurrentReset tests concurrent Reset calls with -race detector
func TestConcurrentReset(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	cb := NewCircuitBreaker(config)

	const numGoroutines = 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			cb.Reset()
		}()
	}

	wg.Wait()
}

// TestConcurrentManagerGetBreaker tests concurrent GetBreaker with -race detector
func TestConcurrentManagerGetBreaker(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	manager := NewCircuitBreakerManager(config)

	const numGoroutines = 10
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				toolName := fmt.Sprintf("tool_%d", workerID%5)
				_ = manager.GetBreaker(toolName)
			}
		}(i)
	}

	wg.Wait()
}

// TestConcurrentManagerResetAll tests concurrent ResetAll with -race detector
func TestConcurrentManagerResetAll(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	manager := NewCircuitBreakerManager(config)

	// Create some breakers
	for i := 0; i < 5; i++ {
		manager.GetBreaker(fmt.Sprintf("tool_%d", i))
	}

	const numGoroutines = 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			manager.ResetAll()
		}()
	}

	wg.Wait()
}

// TestConcurrentMixedCircuitBreakerOperations tests mixed concurrent operations with -race detector
func TestConcurrentMixedCircuitBreakerOperations(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 50
	cb := NewCircuitBreaker(config)

	const numGoroutines = 10
	const numOperations = 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines * 3) // 3 types of operations

	// Concurrent Execute
	for i := 0; i < numGoroutines; i++ {
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				operation := func() error {
					if (workerID+j)%3 == 0 {
						return errors.New("test error")
					}
					return nil
				}
				_ = cb.Execute(operation)
			}
		}(i)
	}

	// Concurrent GetState
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				_ = cb.GetState()
				_ = cb.GetStats()
			}
		}()
	}

	// Occasional Reset
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				if j%20 == 0 {
					cb.Reset()
				}
				time.Sleep(1 * time.Millisecond)
			}
		}()
	}

	wg.Wait()
}
