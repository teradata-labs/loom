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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// countingErrLLM fails every Chat with a fixed error and counts the calls.
type countingErrLLM struct {
	err   error
	calls int
}

func (m *countingErrLLM) Name() string  { return "counting" }
func (m *countingErrLLM) Model() string { return "test" }
func (m *countingErrLLM) Chat(context.Context, []Message, []shuttle.Tool) (*LLMResponse, error) {
	m.calls++
	return nil, m.err
}

func retryingAgent(llmProvider LLMProvider) *Agent {
	return &Agent{
		llm: llmProvider,
		config: &Config{Retry: RetryConfig{
			Enabled: true, MaxRetries: 3, InitialDelay: time.Millisecond, MaxDelay: time.Millisecond, Multiplier: 2,
		}},
	}
}

// The rate limiter's exhausted retry budget is final for the agent loop: one
// budget for throttling/transient errors, in one place. Before this guard a
// persistent 5xx spent the limiter's whole budget and then the agent re-ran
// that budget MaxRetries more times, multiplying the wait without changing
// the outcome.
func TestDispatchChat_DoesNotReRetryLimiterExhaustion(t *testing.T) {
	exhausted := &llm.RetriesExhaustedError{Attempts: 4, Cause: "a transient server error",
		Err: llm.NewTransientError(errors.New("API error (status 503): unavailable"), 503, 0)}
	m := &countingErrLLM{err: exhausted}
	a := retryingAgent(m)

	_, err := a.dispatchChat(&agentContext{Context: context.Background()}, []Message{{Role: "user", Content: "hi"}}, nil)
	require.Error(t, err)
	assert.Equal(t, 1, m.calls, "the agent loop must not re-run a budget the limiter already spent")
	assert.True(t, llm.IsRetriesExhausted(err))
	assert.True(t, llm.IsTransient(err), "the original cause survives")
}

// A plain error (no limiter involvement) still goes through the agent loop's
// own retries, exactly as before.
func TestDispatchChat_StillRetriesPlainErrors(t *testing.T) {
	m := &countingErrLLM{err: errors.New("dial tcp: connection refused")}
	a := retryingAgent(m)

	_, err := a.dispatchChat(&agentContext{Context: context.Background()}, []Message{{Role: "user", Content: "hi"}}, nil)
	require.Error(t, err)
	assert.Equal(t, 4, m.calls, "MaxRetries=3 means 4 attempts")
}
