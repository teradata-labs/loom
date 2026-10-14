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

// Tests that an LLM provider capacity failure (throttle, temporary 5xx)
// surfaces from Weave/StreamWeave as a retryable gRPC status rather than
// Internal, and that the idempotency dedupe releases it instead of caching it.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// anthropicSDKError is the *anthropic.Error the Bedrock client's Anthropic
// SDK returns for an HTTP error response.
func anthropicSDKError(t *testing.T, statusCode int, body string) *anthropic.Error {
	t.Helper()
	u, err := url.Parse("https://bedrock-runtime.us-west-2.amazonaws.com/model/m/invoke")
	require.NoError(t, err)
	req := &http.Request{Method: http.MethodPost, URL: u}
	e := &anthropic.Error{
		StatusCode: statusCode,
		Request:    req,
		Response:   &http.Response{StatusCode: statusCode, Request: req},
	}
	require.NoError(t, e.UnmarshalJSON([]byte(body)))
	return e
}

// bedrockRuntimeError is the chain the AWS SDK returns for a failed
// bedrockruntime call.
func bedrockRuntimeError(statusCode int, exception error) error {
	return &smithy.OperationError{
		ServiceID:     "Bedrock Runtime",
		OperationName: "Converse",
		Err: &awshttp.ResponseError{
			ResponseError: &smithyhttp.ResponseError{
				Response: &smithyhttp.Response{Response: &http.Response{StatusCode: statusCode}},
				Err:      exception,
			},
			RequestID: "req-1",
		},
	}
}

// agentChain wraps a provider error the way a failed turn reaches
// wrapAgentError: Bedrock client → agent retry loop → turn → chat.
func agentChain(providerErr error) error {
	return fmt.Errorf("conversation loop failed: %w",
		fmt.Errorf("LLM call failed: %w",
			fmt.Errorf("llm call failed after 4 attempts: %w",
				fmt.Errorf("bedrock SDK invocation failed: %w", providerErr))))
}

func TestWrapAgentErrorCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		// Unchanged: cancellation, deadline, stream timeout, deterministic.
		{"context canceled", fmt.Errorf("chat: %w", context.Canceled), codes.Canceled},
		{"deadline exceeded", fmt.Errorf("chat: %w", context.DeadlineExceeded), codes.DeadlineExceeded},
		{"provider stream timeout", fmt.Errorf("chat: %w", llm.ErrStreamTimeout), codes.Unavailable},
		{"deterministic agent error", errors.New("llm rejected the request"), codes.Internal},

		// Throttling → ResourceExhausted.
		{"typed ThrottleError", llm.NewThrottleError(errors.New("API error (status 429)"), time.Second), codes.ResourceExhausted},
		{"throttle wrapped through LLM call failed",
			fmt.Errorf("LLM call failed: %w", llm.NewThrottleError(errors.New("slow down"), 0)),
			codes.ResourceExhausted},
		{"rate limiter exhaustion of an SDK 429",
			agentChain(fmt.Errorf("LLM request failed after 6 retries due to throttling: %w",
				anthropicSDKError(t, 429, `{"message":"Too many requests"}`))),
			codes.ResourceExhausted},
		{"Bedrock ThrottlingException", agentChain(bedrockRuntimeError(429, &brtypes.ThrottlingException{Message: aws.String("Too many tokens")})), codes.ResourceExhausted},
		{"untyped SDK throttling message", agentChain(errors.New("ThrottlingException: Too many requests")), codes.ResourceExhausted},

		// Temporary provider server fault → Unavailable.
		{"AWS smithy ResponseError 503", agentChain(bedrockRuntimeError(503, errors.New("upstream connect error"))), codes.Unavailable},
		{"Bedrock ServiceUnavailableException", agentChain(bedrockRuntimeError(503, &brtypes.ServiceUnavailableException{Message: aws.String("unavailable")})), codes.Unavailable},
		{"Bedrock InternalServerException", agentChain(bedrockRuntimeError(500, &brtypes.InternalServerException{Message: aws.String("internal")})), codes.Unavailable},
		{"Bedrock ModelNotReadyException", agentChain(bedrockRuntimeError(429, &brtypes.ModelNotReadyException{Message: aws.String("not ready")})), codes.Unavailable},
		{"Anthropic SDK 503 (Bedrock Claude path)", agentChain(anthropicSDKError(t, 503, `{"message":"Bedrock is unable to process your request."}`)), codes.Unavailable},
		{"Anthropic SDK 529 overloaded", agentChain(anthropicSDKError(t, 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)), codes.Unavailable},
		{"Anthropic SDK mid-stream overloaded_error", agentChain(anthropicSDKError(t, 200, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)), codes.Unavailable},

		// Deterministic provider refusals stay Internal.
		{"Anthropic SDK 400 validation", agentChain(anthropicSDKError(t, 400, `{"message":"messages: field required"}`)), codes.Internal},
		{"Anthropic SDK 400 whose body mentions 429", agentChain(anthropicSDKError(t, 400, `{"message":"exceed context limit: 180429 + 21000 > 200000"}`)), codes.Internal},
		{"Bedrock ValidationException", agentChain(bedrockRuntimeError(400, &brtypes.ValidationException{Message: aws.String("malformed input")})), codes.Internal},
		{"Bedrock AccessDeniedException", agentChain(bedrockRuntimeError(403, &brtypes.AccessDeniedException{Message: aws.String("denied")})), codes.Internal},
		{"untyped 5xx message", agentChain(errors.New("API error (status 503): upstream down")), codes.Internal},

		// Order: a request that was canceled or ran out of time is reported
		// as such even when the provider call it interrupted was throttled.
		{"canceled while throttled", fmt.Errorf("%w: %w", context.Canceled, llm.NewThrottleError(errors.New("429"), 0)), codes.Canceled},
		{"deadline while unavailable", fmt.Errorf("%w: %w", context.DeadlineExceeded, anthropicSDKError(t, 503, `{}`)), codes.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapAgentError(tt.err)
			assert.Equal(t, tt.want, status.Code(got), "%v", got)
			// Every retryable code here must be released by the dedupe.
			assert.Equal(t, tt.want != codes.Internal, isTransientOutcome(got),
				"isTransientOutcome(%s)", status.Code(got))
		})
	}
}

// failingProvider is an LLM provider whose every call fails with err after
// passing through a real llm.RateLimiter, so the test covers the limiter's
// exhaustion wrap as well as the agent and server layers.
type failingProvider struct {
	rl    *llm.RateLimiter
	err   error
	calls atomic.Int32
}

func (p *failingProvider) Chat(ctx context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	p.calls.Add(1)
	if _, err := p.rl.Do(ctx, func(context.Context) (interface{}, error) { return nil, p.err }); err != nil {
		return nil, fmt.Errorf("bedrock SDK invocation failed: %w", err)
	}
	return &llmtypes.LLMResponse{Content: "unreachable"}, nil
}

func (p *failingProvider) Name() string  { return "failing" }
func (p *failingProvider) Model() string { return "failing-model" }

func newFailingProvider(t *testing.T, err error) *failingProvider {
	t.Helper()
	rl := llm.NewRateLimiter(llm.RateLimiterConfig{
		Enabled:           true,
		Logger:            zap.NewNop(),
		RequestsPerSecond: 1000,
		BurstCapacity:     100,
		MinDelay:          time.Microsecond,
		MaxRetries:        1,
		RetryBackoff:      time.Millisecond,
		QueueTimeout:      10 * time.Second,
	})
	t.Cleanup(func() { _ = rl.Close() })
	return &failingProvider{rl: rl, err: err}
}

// newFailingAgent keeps the agent's own LLM retry loop enabled — it is one
// of the wrap layers under test — with millisecond backoff so the test is
// fast.
func newFailingAgent(p *failingProvider) *agent.Agent {
	cfg := agent.DefaultConfig()
	cfg.Retry.InitialDelay = time.Millisecond
	cfg.Retry.MaxDelay = 2 * time.Millisecond
	return agent.NewAgent(&mockBackend{}, p, agent.WithConfig(cfg))
}

func newFailingServer(p *failingProvider) *MultiAgentServer {
	return NewMultiAgentServer(map[string]*agent.Agent{"agent-1": newFailingAgent(p)}, nil)
}

// End to end: provider → rate limiter → agent retry → turn → Weave →
// wrapAgentError → dedupe. A provider capacity failure must come back with a
// retryable code and must not be cached for a same-key retry.
func TestWeaveProviderCapacityFailureIsRetryableAndReleased(t *testing.T) {
	tests := []struct {
		name string
		err  func(t *testing.T) error
		want codes.Code
	}{
		{"Bedrock throttle", func(*testing.T) error {
			return bedrockRuntimeError(429, &brtypes.ThrottlingException{Message: aws.String("Too many tokens")})
		}, codes.ResourceExhausted},
		{"Anthropic SDK 429", func(t *testing.T) error {
			return anthropicSDKError(t, 429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`)
		}, codes.ResourceExhausted},
		{"Bedrock ServiceUnavailableException", func(*testing.T) error {
			return bedrockRuntimeError(503, &brtypes.ServiceUnavailableException{Message: aws.String("unavailable")})
		}, codes.Unavailable},
		{"Anthropic SDK 503", func(t *testing.T) error {
			return anthropicSDKError(t, 503, `{"message":"Bedrock is unable to process your request."}`)
		}, codes.Unavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newFailingProvider(t, tt.err(t))
			srv := newFailingServer(p)

			_, err := srv.Weave(keyedCtx("user-a", "key-1"), &loomv1.WeaveRequest{Query: "q"})
			require.Error(t, err)
			assert.Equal(t, tt.want, status.Code(err), "%v", err)
			first := p.calls.Load()
			require.Positive(t, first)

			// Same key again: released, so the turn re-executes (the
			// provider is called again) instead of joining a cached failure.
			_, err = srv.Weave(keyedCtx("user-a", "key-1"), &loomv1.WeaveRequest{Query: "q"})
			assert.Equal(t, tt.want, status.Code(err), "%v", err)
			assert.Greater(t, p.calls.Load(), first, "a capacity failure must be released by the dedupe, not cached")
		})
	}

	t.Run("a deterministic provider refusal stays Internal and cached", func(t *testing.T) {
		p := newFailingProvider(t, bedrockRuntimeError(400, &brtypes.ValidationException{Message: aws.String("malformed input")}))
		srv := newFailingServer(p)

		_, err := srv.Weave(keyedCtx("user-a", "key-1"), &loomv1.WeaveRequest{Query: "q"})
		assert.Equal(t, codes.Internal, status.Code(err), "%v", err)
		first := p.calls.Load()

		_, err = srv.Weave(keyedCtx("user-a", "key-1"), &loomv1.WeaveRequest{Query: "q"})
		assert.Equal(t, codes.Internal, status.Code(err), "%v", err)
		assert.Equal(t, first, p.calls.Load(), "a deterministic failure is durable: the retry joins it")
	})
}

// StreamWeave on both servers reports a provider throttle with the same
// retryable code as Weave.
func TestStreamWeaveProviderThrottleIsResourceExhausted(t *testing.T) {
	throttle := func(*testing.T) error {
		return bedrockRuntimeError(429, &brtypes.ThrottlingException{Message: aws.String("Too many tokens")})
	}

	t.Run("multi-agent server", func(t *testing.T) {
		srv := newFailingServer(newFailingProvider(t, throttle(t)))
		stream := &mockStreamWeaveServer{ctx: context.Background()}
		err := srv.StreamWeave(&loomv1.WeaveRequest{Query: "q"}, stream)
		assert.Equal(t, codes.ResourceExhausted, status.Code(err), "%v", err)
	})

	t.Run("single-agent server", func(t *testing.T) {
		srv := NewServer(newFailingAgent(newFailingProvider(t, throttle(t))), nil)
		stream := &mockStreamWeaveServer{ctx: context.Background()}
		err := srv.StreamWeave(&loomv1.WeaveRequest{Query: "q"}, stream)
		assert.Equal(t, codes.ResourceExhausted, status.Code(err), "%v", err)
	})
}
