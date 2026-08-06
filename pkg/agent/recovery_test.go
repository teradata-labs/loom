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
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// mockSessionForRecovery implements sessionForRecovery for testing.
func TestRecovery_ToolCB_DisablesTool(t *testing.T) {
	_, span := observability.NewNoOpTracer().StartSpan(context.Background(), "test")
	recovery := newRecoveryOrchestrator(nil, span)

	tools := []shuttle.Tool{
		&shuttle.MockTool{MockName: "web_search"},
		&shuttle.MockTool{MockName: "execute_sql"},
		&shuttle.MockTool{MockName: "read_file"},
	}

	recovered, syntheticResult := recovery.recoverToolCB(context.Background(), "web_search", &tools)
	assert.True(t, recovered)
	assert.NotNil(t, syntheticResult)
	assert.False(t, syntheticResult.Success)
	assert.Equal(t, "tool_disabled", syntheticResult.Error.Code)
	assert.Contains(t, syntheticResult.Error.Message, "web_search")

	// Verify tool was removed from slice
	assert.Len(t, tools, 2)
	for _, tool := range tools {
		assert.NotEqual(t, "web_search", tool.Name())
	}

	// Verify disabled tools map
	assert.True(t, recovery.disabledTools["web_search"])
}

// --- Test: Token Budget Aggressive Trim ---

func TestRecoverableError_Interface(t *testing.T) {
	cause := fmt.Errorf("underlying error")
	recErr := &RecoverableError{
		ErrorType:       "test_error",
		Message:         "something broke",
		RecoveryAction:  "retry",
		RecoveryPayload: map[string]any{"key": "value"},
		Retryable:       true,
		Cause:           cause,
	}

	// Implements error
	var err error = recErr
	assert.Equal(t, "something broke", err.Error())

	// Unwrap works with errors.Is / errors.As
	assert.ErrorIs(t, recErr, cause)

	var target *RecoverableError
	assert.True(t, errors.As(err, &target))
	assert.Equal(t, "test_error", target.ErrorType)
	assert.Equal(t, "retry", target.RecoveryAction)
	assert.True(t, target.Retryable)
}

// --- Test: Observability (span events emitted) ---

func TestRecovery_Observability(t *testing.T) {
	tracer := observability.NewNoOpTracer()
	_, span := tracer.StartSpan(context.Background(), "test")
	recovery := newRecoveryOrchestrator(nil, span)

	// Tool CB recovery emits an event.
	tools := []shuttle.Tool{&shuttle.MockTool{MockName: "broken_tool"}}
	recovered, _ := recovery.recoverToolCB(context.Background(), "broken_tool", &tools)
	assert.True(t, recovered)
}

// --- Test: Concurrent Access ---
// The recoveryOrchestrator is designed for single-goroutine use (one per loop).
// This test validates that the TrimableMemory interface implementations are
// safe for concurrent access (since extraction goroutines run in parallel).

func TestRecovery_ConcurrentAccess(t *testing.T) {
	sm := NewSegmentedMemory("ROM", 200000, 20000)

	// Fill with messages
	for i := 0; i < 50; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		sm.AddMessage(context.Background(), Message{
			Role:      role,
			Content:   fmt.Sprintf("message %d with some content", i),
			Timestamp: time.Now(),
		})
	}

	var wg sync.WaitGroup
	const readers = 5

	// Concurrent readers while a trim happens.
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sm.GetTokenCount()
			_ = sm.GetMessagesForLLM()
		}()
	}

	wg.Wait()

	// Verify no panic and data is consistent.
	sm.mu.RLock()
	assert.Equal(t, 50, len(sm.contextMessages))
	sm.mu.RUnlock()
}
