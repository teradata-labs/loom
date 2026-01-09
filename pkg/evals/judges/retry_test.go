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
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// retryMockJudge is a mock judge for testing retry logic
type retryMockJudge struct {
	id           string
	name         string
	config       *loomv1.JudgeConfig
	evaluateFunc func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error)
	callCount    int32
}

func (m *retryMockJudge) ID() string {
	return m.id
}

func (m *retryMockJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	atomic.AddInt32(&m.callCount, 1)
	if m.evaluateFunc != nil {
		return m.evaluateFunc(ctx, evalCtx)
	}
	return &loomv1.JudgeResult{
		JudgeId:      "mock-judge",
		JudgeName:    m.name,
		Verdict:      "PASS",
		OverallScore: 100.0,
		JudgedAt:     timestamppb.Now(),
	}, nil
}

func (m *retryMockJudge) Name() string {
	return m.name
}

func (m *retryMockJudge) Criteria() []string {
	return []string{"test criteria"}
}

func (m *retryMockJudge) Config() *loomv1.JudgeConfig {
	return m.config
}

func (m *retryMockJudge) Criticality() loomv1.JudgeCriticality {
	if m.config != nil {
		return m.config.Criticality
	}
	return loomv1.JudgeCriticality_JUDGE_CRITICALITY_NON_CRITICAL
}

func (m *retryMockJudge) Weight() float64 {
	if m.config != nil && m.config.Weight > 0 {
		return m.config.Weight
	}
	return 1.0
}

func (m *retryMockJudge) Dimensions() []loomv1.JudgeDimension {
	if m.config != nil && len(m.config.Dimensions) > 0 {
		return m.config.Dimensions
	}
	return []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY}
}

func (m *retryMockJudge) GetCallCount() int32 {
	return atomic.LoadInt32(&m.callCount)
}

func TestNewRetryableJudge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         *loomv1.JudgeConfig
		expectedConfig *loomv1.RetryConfig
	}{
		{
			name:   "nil retry config uses defaults",
			config: &loomv1.JudgeConfig{},
			expectedConfig: &loomv1.RetryConfig{
				MaxAttempts:       3,
				InitialBackoffMs:  1000,
				MaxBackoffMs:      8000,
				BackoffMultiplier: 2.0,
				RetryOnStatus:     []int32{429, 500, 502, 503},
			},
		},
		{
			name: "custom retry config applied",
			config: &loomv1.JudgeConfig{
				RetryConfig: &loomv1.RetryConfig{
					MaxAttempts:       5,
					InitialBackoffMs:  500,
					MaxBackoffMs:      4000,
					BackoffMultiplier: 1.5,
					RetryOnStatus:     []int32{429, 500},
				},
			},
			expectedConfig: &loomv1.RetryConfig{
				MaxAttempts:       5,
				InitialBackoffMs:  500,
				MaxBackoffMs:      4000,
				BackoffMultiplier: 1.5,
				RetryOnStatus:     []int32{429, 500},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			judge := &retryMockJudge{id: "test-judge", name: "test-judge", config: tt.config}
			rj := NewRetryableJudge(judge, tt.config, zap.NewNop())

			require.NotNil(t, rj)
			assert.Equal(t, judge.name, rj.Name())
			assert.Equal(t, tt.expectedConfig.MaxAttempts, rj.retryConfig.MaxAttempts)
			assert.Equal(t, tt.expectedConfig.InitialBackoffMs, rj.retryConfig.InitialBackoffMs)
			assert.Equal(t, tt.expectedConfig.MaxBackoffMs, rj.retryConfig.MaxBackoffMs)
			assert.Equal(t, tt.expectedConfig.BackoffMultiplier, rj.retryConfig.BackoffMultiplier)
		})
	}
}

func TestRetryableJudgeSuccess(t *testing.T) {
	t.Parallel()

	config := &loomv1.JudgeConfig{
		RetryConfig: &loomv1.RetryConfig{
			MaxAttempts:      3,
			InitialBackoffMs: 10,
		},
	}

	judge := &retryMockJudge{
		id:     "success-judge",
		name:   "success-judge",
		config: config,
	}

	rj := NewRetryableJudge(judge, config, zap.NewNop())

	ctx := context.Background()
	evalCtx := &loomv1.EvaluationContext{
		AgentId:  "test-agent",
		Prompt:   "test prompt",
		Response: "test response",
	}

	result, err := rj.Evaluate(ctx, evalCtx)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "PASS", result.Verdict)

	// Should only be called once
	assert.Equal(t, int32(1), judge.GetCallCount())
}

func TestRetryableJudgeRetryOnTransientError(t *testing.T) {
	t.Parallel()

	config := &loomv1.JudgeConfig{
		RetryConfig: &loomv1.RetryConfig{
			MaxAttempts:      3,
			InitialBackoffMs: 10,
			MaxBackoffMs:     50,
		},
	}

	attemptCount := int32(0)
	judge := &retryMockJudge{id: "test-judge",
		name:   "retry-judge",
		config: config,
		evaluateFunc: func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
			count := atomic.AddInt32(&attemptCount, 1)
			if count < 3 {
				// Fail first 2 attempts with retryable error
				return nil, &HTTPError{StatusCode: 500, Message: "Internal Server Error"}
			}
			// Succeed on third attempt
			return &loomv1.JudgeResult{
				JudgeId:      "retry-judge",
				JudgeName:    "retry-judge",
				Verdict:      "PASS",
				OverallScore: 100.0,
				JudgedAt:     timestamppb.Now(),
			}, nil
		},
	}

	rj := NewRetryableJudge(judge, config, zap.NewNop())

	ctx := context.Background()
	evalCtx := &loomv1.EvaluationContext{
		AgentId:  "test-agent",
		Prompt:   "test prompt",
		Response: "test response",
	}

	result, err := rj.Evaluate(ctx, evalCtx)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "PASS", result.Verdict)
	assert.Equal(t, int32(3), attemptCount)
}

func TestRetryableJudgeMaxAttemptsExceeded(t *testing.T) {
	t.Parallel()

	config := &loomv1.JudgeConfig{
		RetryConfig: &loomv1.RetryConfig{
			MaxAttempts:      2,
			InitialBackoffMs: 10,
		},
	}

	judge := &retryMockJudge{id: "test-judge",
		name:   "failing-judge",
		config: config,
		evaluateFunc: func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
			return nil, &HTTPError{StatusCode: 500, Message: "Internal Server Error"}
		},
	}

	rj := NewRetryableJudge(judge, config, zap.NewNop())

	ctx := context.Background()
	evalCtx := &loomv1.EvaluationContext{
		AgentId:  "test-agent",
		Prompt:   "test prompt",
		Response: "test response",
	}

	result, err := rj.Evaluate(ctx, evalCtx)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed after 3 attempts")
	assert.Equal(t, int32(3), judge.GetCallCount()) // 1 initial + 2 retries
}

func TestRetryableJudgeNonRetryableError(t *testing.T) {
	t.Parallel()

	config := &loomv1.JudgeConfig{
		RetryConfig: &loomv1.RetryConfig{
			MaxAttempts:      3,
			InitialBackoffMs: 10,
		},
	}

	judge := &retryMockJudge{id: "test-judge",
		name:   "non-retryable-judge",
		config: config,
		evaluateFunc: func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
			// Non-retryable error (400 Bad Request)
			return nil, &HTTPError{StatusCode: 400, Message: "Bad Request"}
		},
	}

	rj := NewRetryableJudge(judge, config, zap.NewNop())

	ctx := context.Background()
	evalCtx := &loomv1.EvaluationContext{
		AgentId:  "test-agent",
		Prompt:   "test prompt",
		Response: "test response",
	}

	result, err := rj.Evaluate(ctx, evalCtx)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "non-retryable error")

	// Should only be called once
	assert.Equal(t, int32(1), judge.GetCallCount())
}

func TestRetryableJudgeCircuitBreaker(t *testing.T) {
	t.Parallel()

	config := &loomv1.JudgeConfig{
		RetryConfig: &loomv1.RetryConfig{
			MaxAttempts:      3,
			InitialBackoffMs: 10,
			CircuitBreaker: &loomv1.CircuitBreakerConfig{
				FailureThreshold: 2,
				ResetTimeoutMs:   100,
				SuccessThreshold: 1,
				Enabled:          true,
			},
		},
	}

	judge := &retryMockJudge{id: "test-judge",
		name:   "circuit-breaker-judge",
		config: config,
		evaluateFunc: func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
			return nil, &HTTPError{StatusCode: 500, Message: "Internal Server Error"}
		},
	}

	rj := NewRetryableJudge(judge, config, zap.NewNop())

	ctx := context.Background()
	evalCtx := &loomv1.EvaluationContext{
		AgentId:  "test-agent",
		Prompt:   "test prompt",
		Response: "test response",
	}

	// First call should fail and record failure
	_, err := rj.Evaluate(ctx, evalCtx)
	require.Error(t, err)

	// Second call should fail and open circuit
	_, err = rj.Evaluate(ctx, evalCtx)
	require.Error(t, err)

	// Third call should fail immediately due to open circuit
	_, err = rj.Evaluate(ctx, evalCtx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker open")
}

func TestRetryableJudgeContextCancellation(t *testing.T) {
	t.Parallel()

	config := &loomv1.JudgeConfig{
		RetryConfig: &loomv1.RetryConfig{
			MaxAttempts:      5,
			InitialBackoffMs: 100, // Longer backoff
		},
	}

	judge := &retryMockJudge{id: "test-judge",
		name:   "slow-judge",
		config: config,
		evaluateFunc: func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
			return nil, &HTTPError{StatusCode: 500, Message: "Internal Server Error"}
		},
	}

	rj := NewRetryableJudge(judge, config, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	evalCtx := &loomv1.EvaluationContext{
		AgentId:  "test-agent",
		Prompt:   "test prompt",
		Response: "test response",
	}

	result, err := rj.Evaluate(ctx, evalCtx)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "cancelled during retry")
}

func TestRetryableJudgeExponentialBackoff(t *testing.T) {
	t.Parallel()

	config := &loomv1.JudgeConfig{
		RetryConfig: &loomv1.RetryConfig{
			MaxAttempts:       3,
			InitialBackoffMs:  100,
			MaxBackoffMs:      1000,
			BackoffMultiplier: 2.0,
		},
	}

	judge := &retryMockJudge{id: "test-judge", name: "backoff-judge", config: config}
	rj := NewRetryableJudge(judge, config, zap.NewNop())

	tests := []struct {
		attempt  int32
		expected time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
		{4, 1000 * time.Millisecond}, // Capped at max
	}

	for _, tt := range tests {
		t.Run("attempt "+string(rune(tt.attempt+'0')), func(t *testing.T) {
			backoff := rj.calculateBackoff(tt.attempt)
			assert.Equal(t, tt.expected, backoff)
		})
	}
}

func TestIsRetryable(t *testing.T) {
	t.Parallel()

	config := &loomv1.JudgeConfig{
		RetryConfig: &loomv1.RetryConfig{
			RetryOnStatus: []int32{429, 500, 502, 503},
		},
	}

	judge := &retryMockJudge{id: "test-judge", name: "test-judge", config: config}
	rj := NewRetryableJudge(judge, config, zap.NewNop())

	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name:      "nil error",
			err:       nil,
			retryable: false,
		},
		{
			name:      "HTTP 429 (rate limit)",
			err:       &HTTPError{StatusCode: 429, Message: "Too Many Requests"},
			retryable: true,
		},
		{
			name:      "HTTP 500",
			err:       &HTTPError{StatusCode: 500, Message: "Internal Server Error"},
			retryable: true,
		},
		{
			name:      "HTTP 502",
			err:       &HTTPError{StatusCode: 502, Message: "Bad Gateway"},
			retryable: true,
		},
		{
			name:      "HTTP 503",
			err:       &HTTPError{StatusCode: 503, Message: "Service Unavailable"},
			retryable: true,
		},
		{
			name:      "HTTP 400 (client error)",
			err:       &HTTPError{StatusCode: 400, Message: "Bad Request"},
			retryable: false,
		},
		{
			name:      "HTTP 404 (not found)",
			err:       &HTTPError{StatusCode: 404, Message: "Not Found"},
			retryable: false,
		},
		{
			name:      "context deadline exceeded",
			err:       context.DeadlineExceeded,
			retryable: true,
		},
		{
			name:      "connection reset",
			err:       syscall.ECONNRESET,
			retryable: true,
		},
		{
			name:      "connection refused",
			err:       syscall.ECONNREFUSED,
			retryable: true,
		},
		{
			name:      "timeout error message",
			err:       errors.New("request timeout"),
			retryable: true,
		},
		{
			name:      "rate limit error message",
			err:       errors.New("rate limit exceeded"),
			retryable: true,
		},
		{
			name:      "generic error",
			err:       errors.New("something went wrong"),
			retryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := rj.isRetryable(tt.err)
			assert.Equal(t, tt.retryable, result)
		})
	}
}

func TestNewHTTPError(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: 500,
		Status:     "500 Internal Server Error",
	}

	err := NewHTTPError(resp)
	require.NotNil(t, err)

	httpErr, ok := err.(*HTTPError)
	require.True(t, ok)
	assert.Equal(t, 500, httpErr.StatusCode)
	assert.Equal(t, "500 Internal Server Error", httpErr.Message)
	assert.Equal(t, "HTTP 500: 500 Internal Server Error", httpErr.Error())
}

func TestDefaultRetryConfig(t *testing.T) {
	t.Parallel()

	config := DefaultRetryConfig()
	require.NotNil(t, config)

	assert.Equal(t, int32(3), config.MaxAttempts)
	assert.Equal(t, int32(1000), config.InitialBackoffMs)
	assert.Equal(t, int32(8000), config.MaxBackoffMs)
	assert.Equal(t, 2.0, config.BackoffMultiplier)
	assert.Equal(t, []int32{429, 500, 502, 503}, config.RetryOnStatus)
	assert.NotNil(t, config.CircuitBreaker)
}

func TestRetryableJudgeConcurrency(t *testing.T) {
	t.Parallel()

	config := &loomv1.JudgeConfig{
		RetryConfig: &loomv1.RetryConfig{
			MaxAttempts:      2,
			InitialBackoffMs: 10,
		},
	}

	judge := &retryMockJudge{id: "test-judge",
		name:   "concurrent-judge",
		config: config,
	}

	rj := NewRetryableJudge(judge, config, zap.NewNop())

	ctx := context.Background()
	evalCtx := &loomv1.EvaluationContext{
		AgentId:  "test-agent",
		Prompt:   "test prompt",
		Response: "test response",
	}

	// Run multiple evaluations concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			result, err := rj.Evaluate(ctx, evalCtx)
			assert.NoError(t, err)
			assert.NotNil(t, result)
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	assert.Equal(t, int32(10), judge.GetCallCount())
}
