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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// failingStreamLLM fails its first `failures` ChatStream calls with err. Each
// failed call first sends `tokensBeforeFail` tokens; 20 or more make the
// streaming path flush a progress event to the caller before the failure.
type failingStreamLLM struct {
	mu               sync.Mutex
	calls            int
	failures         int
	tokensBeforeFail int
	err              error
}

func (s *failingStreamLLM) Name() string  { return "failing-stream" }
func (s *failingStreamLLM) Model() string { return "test-model" }

func (s *failingStreamLLM) Chat(context.Context, []Message, []shuttle.Tool) (*LLMResponse, error) {
	return nil, errors.New("failingStreamLLM: Chat must not be called on the streaming path")
}

func (s *failingStreamLLM) ChatStream(_ context.Context, _ []Message, _ []shuttle.Tool, cb llmtypes.TokenCallback) (*LLMResponse, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if n <= s.failures {
		for i := 0; i < s.tokensBeforeFail; i++ {
			cb("x")
		}
		return nil, s.err
	}
	cb("ok")
	return &LLMResponse{Content: "ok", StopReason: "end_turn"}, nil
}

func (s *failingStreamLLM) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// fastRetry is a retry-enabled config with negligible backoff.
func fastRetry(maxRetries int) RetryConfig {
	return RetryConfig{Enabled: true, MaxRetries: maxRetries, InitialDelay: time.Millisecond, Multiplier: 2, MaxDelay: time.Millisecond}
}

var errConnReset = errors.New("read tcp: connection reset by peer")

// A streaming call that fails with a transient error before it sends any
// progress is retried under the agent's Retry config, as the direct path
// is. Before, the streaming path returned the error at once, so a caller
// that set a progress callback lost the failure retry.
func TestChatWithStreaming_RetriesFailureBeforeAnyOutput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		retry     RetryConfig
		stub      *failingStreamLLM
		wantErr   string
		wantIs    error
		wantCalls int
	}{
		{
			name:      "transient failure is retried and succeeds",
			retry:     fastRetry(3),
			stub:      &failingStreamLLM{failures: 2, err: errConnReset},
			wantCalls: 3,
		},
		{
			name:      "budget spent returns the wrapped error",
			retry:     fastRetry(2),
			stub:      &failingStreamLLM{failures: 10, err: errConnReset},
			wantErr:   "llm call failed after 3 attempts",
			wantIs:    errConnReset,
			wantCalls: 3,
		},
		{
			name:      "retry disabled returns the error at once",
			retry:     RetryConfig{},
			stub:      &failingStreamLLM{failures: 1, err: errConnReset},
			wantIs:    errConnReset,
			wantCalls: 1,
		},
		{
			name:      "context too long is never retried",
			retry:     fastRetry(3),
			stub:      &failingStreamLLM{failures: 1, err: fmt.Errorf("provider: %w", llm.ErrContextTooLong)},
			wantIs:    llm.ErrContextTooLong,
			wantCalls: 1,
		},
		{
			name:      "failure after output reached the caller is not retried",
			retry:     fastRetry(3),
			stub:      &failingStreamLLM{failures: 1, tokensBeforeFail: 25, err: errConnReset},
			wantIs:    errConnReset,
			wantCalls: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var events []ProgressEvent
			cb := func(e ProgressEvent) {
				mu.Lock()
				events = append(events, e)
				mu.Unlock()
			}
			a := &Agent{llm: tc.stub, config: &Config{Retry: tc.retry}}

			resp, err := a.chatWithStreaming(newTestContextWithProgress(cb), []Message{{Role: "user", Content: "hi"}}, nil, cb)

			assert.Equal(t, tc.wantCalls, tc.stub.callCount(), "ChatStream calls")
			if tc.wantIs == nil {
				require.NoError(t, err)
				assert.Equal(t, "ok", resp.Content)
				mu.Lock()
				defer mu.Unlock()
				for _, e := range events {
					assert.NotContains(t, e.PartialContent, "x", "a failed attempt's tokens must not reach the caller")
				}
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.wantIs)
			if tc.wantErr != "" {
				assert.True(t, strings.Contains(err.Error(), tc.wantErr), "error %q lacks %q", err, tc.wantErr)
			}
		})
	}
}

// A context that ends during the backoff ends the call with the context error.
func TestStreamFailureRetry_ContextEndsDuringBackoff(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	r := &streamFailureRetry{
		cfg:   fastRetry(3),
		delay: time.Hour,
		after: func(time.Duration) <-chan time.Time { cancel(); return make(chan time.Time) },
	}
	err := r.wait(&agentContext{Context: ctx}, errConnReset, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
