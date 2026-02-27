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

package server

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// mockJudgeLLMProvider satisfies types.LLMProvider for judge server tests.
// It returns a valid LLM judge JSON verdict on every call.
type mockJudgeLLMProvider struct {
	model string
}

func (m *mockJudgeLLMProvider) Chat(_ context.Context, _ []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{
		Content: `{"factual_accuracy": 90, "hallucination_score": 10, "query_quality": 85, "completeness": 88, "verdict": "PASS", "reasoning": "Looks good", "issues": []}`,
	}, nil
}

func (m *mockJudgeLLMProvider) Model() string {
	if m.model != "" {
		return m.model
	}
	return "mock-model"
}

func (m *mockJudgeLLMProvider) Name() string { return "mock" }

// newTestJudgeServer creates a JudgeServer wired with a mock LLM provider.
func newTestJudgeServer() *JudgeServer {
	s := NewJudgeServer(observability.NewNoOpTracer(), zap.NewNop())
	provider := &mockJudgeLLMProvider{model: "claude-sonnet-4-6"}
	s.SetProviderPool(map[string]types.LLMProvider{"mock": provider}, provider)
	return s
}

// TestJudgeServer_RegisterJudge verifies judge registration.
func TestJudgeServer_RegisterJudge(t *testing.T) {
	tests := []struct {
		name        string
		config      *loomv1.JudgeConfig
		wantErrCode codes.Code
		wantID      string // if non-empty, assert exact ID
	}{
		{
			name:   "happy path with name",
			config: &loomv1.JudgeConfig{Name: "quality-judge", Criteria: "accuracy"},
			// ID is derived slug: "quality-judge"
			wantID: "quality-judge",
		},
		{
			name:   "happy path with explicit id",
			config: &loomv1.JudgeConfig{Id: "my-judge", Name: "My Judge"},
			wantID: "my-judge",
		},
		{
			name:   "name with spaces gets slugified",
			config: &loomv1.JudgeConfig{Name: "Cost Efficiency Judge"},
			wantID: "cost-efficiency-judge",
		},
		{
			name:        "nil config returns error",
			config:      nil,
			wantErrCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestJudgeServer()
			resp, err := s.RegisterJudge(context.Background(), &loomv1.RegisterJudgeRequest{Config: tt.config})

			if tt.wantErrCode != codes.OK {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok, "expected gRPC status error")
				assert.Equal(t, tt.wantErrCode, st.Code())
				return
			}

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.NotEmpty(t, resp.JudgeId)
			if tt.wantID != "" {
				assert.Equal(t, tt.wantID, resp.JudgeId)
			}
		})
	}
}

// TestJudgeServer_RegisterJudge_DuplicateOverwrites verifies that registering
// the same ID twice overwrites the previous config.
func TestJudgeServer_RegisterJudge_DuplicateOverwrites(t *testing.T) {
	s := newTestJudgeServer()
	ctx := context.Background()

	cfg1 := &loomv1.JudgeConfig{Id: "dup", Name: "first", Criteria: "original"}
	_, err := s.RegisterJudge(ctx, &loomv1.RegisterJudgeRequest{Config: cfg1})
	require.NoError(t, err)

	cfg2 := &loomv1.JudgeConfig{Id: "dup", Name: "first", Criteria: "updated"}
	_, err = s.RegisterJudge(ctx, &loomv1.RegisterJudgeRequest{Config: cfg2})
	require.NoError(t, err)

	got, err := s.GetJudgeConfig("dup")
	require.NoError(t, err)
	assert.Equal(t, "updated", got.Criteria)
}

// TestJudgeServer_GetJudgeConfig exercises found and not-found paths.
func TestJudgeServer_GetJudgeConfig(t *testing.T) {
	s := newTestJudgeServer()
	ctx := context.Background()

	_, _ = s.RegisterJudge(ctx, &loomv1.RegisterJudgeRequest{
		Config: &loomv1.JudgeConfig{Id: "known", Name: "known"},
	})

	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"found", "known", false},
		{"not found", "unknown-id", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := s.GetJudgeConfig(tt.id)
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, cfg)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, cfg)
			}
		})
	}
}

// TestJudgeServer_EvaluateWithJudges_EmptyJudgeIds verifies error on empty IDs.
func TestJudgeServer_EvaluateWithJudges_EmptyJudgeIds(t *testing.T) {
	s := newTestJudgeServer()
	_, err := s.EvaluateWithJudges(context.Background(), &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{Prompt: "hello", Response: "world"},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// TestJudgeServer_EvaluateWithJudges_UnknownJudge verifies NotFound for unknown ID.
func TestJudgeServer_EvaluateWithJudges_UnknownJudge(t *testing.T) {
	s := newTestJudgeServer()
	_, err := s.EvaluateWithJudges(context.Background(), &loomv1.EvaluateRequest{
		JudgeIds: []string{"no-such-judge"},
		Context:  &loomv1.EvaluationContext{Prompt: "hello", Response: "world"},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestJudgeServer_EvaluateWithJudges_NoProvider verifies FailedPrecondition when no LLM.
func TestJudgeServer_EvaluateWithJudges_NoProvider(t *testing.T) {
	// Create server without wiring a provider.
	s := NewJudgeServer(observability.NewNoOpTracer(), zap.NewNop())
	ctx := context.Background()

	_, _ = s.RegisterJudge(ctx, &loomv1.RegisterJudgeRequest{
		Config: &loomv1.JudgeConfig{Id: "j1", Name: "j1"},
	})

	_, err := s.EvaluateWithJudges(ctx, &loomv1.EvaluateRequest{
		JudgeIds: []string{"j1"},
		Context:  &loomv1.EvaluationContext{Prompt: "hello", Response: "world"},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

// TestJudgeServer_EvaluateWithJudges_HappyPath verifies a successful evaluation.
func TestJudgeServer_EvaluateWithJudges_HappyPath(t *testing.T) {
	s := newTestJudgeServer()
	ctx := context.Background()

	_, err := s.RegisterJudge(ctx, &loomv1.RegisterJudgeRequest{
		Config: &loomv1.JudgeConfig{Id: "quality", Name: "quality", Criteria: "accuracy"},
	})
	require.NoError(t, err)

	resp, err := s.EvaluateWithJudges(ctx, &loomv1.EvaluateRequest{
		JudgeIds:    []string{"quality"},
		Aggregation: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		Context: &loomv1.EvaluationContext{
			Prompt:   "find duplicate integers",
			Response: "use a map to count occurrences",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, resp.Verdicts, 1)
	assert.Greater(t, resp.FinalScore, 0.0)
}

// TestJudgeServer_GetJudgeHistory_Empty verifies GetJudgeHistory returns empty without error.
func TestJudgeServer_GetJudgeHistory_Empty(t *testing.T) {
	s := newTestJudgeServer()
	resp, err := s.GetJudgeHistory(context.Background(), &loomv1.GetJudgeHistoryRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Empty(t, resp.Evaluations)
	assert.Equal(t, int32(0), resp.TotalCount)
}

// TestJudgeServer_Race exercises RegisterJudge and GetJudgeConfig concurrently
// to confirm there are no data races (requires -race flag).
func TestJudgeServer_Race(t *testing.T) {
	s := newTestJudgeServer()
	ctx := context.Background()

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			id := slugify("judge-" + itoa(n))
			_, _ = s.RegisterJudge(ctx, &loomv1.RegisterJudgeRequest{
				Config: &loomv1.JudgeConfig{Id: id, Name: id},
			})
		}(i)
		go func(n int) {
			defer wg.Done()
			id := slugify("judge-" + itoa(n))
			_, _ = s.GetJudgeConfig(id)
		}(i)
	}

	wg.Wait()
}

// itoa converts int to string without importing strconv in the loop.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// ---------------------------------------------------------------------------
// ABTest + JudgeServer wiring tests
// ---------------------------------------------------------------------------

// mockABTestStream implements grpc.ServerStreamingServer[loomv1.ABTestEvent].
type mockABTestStream struct {
	ctx    context.Context
	events []*loomv1.ABTestEvent
	mu     sync.Mutex
}

func (m *mockABTestStream) Send(evt *loomv1.ABTestEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, evt)
	return nil
}
func (m *mockABTestStream) Context() context.Context        { return m.ctx }
func (m *mockABTestStream) SendMsg(msg interface{}) error   { return nil }
func (m *mockABTestStream) RecvMsg(msg interface{}) error   { return nil }
func (m *mockABTestStream) SetHeader(md metadata.MD) error  { return nil }
func (m *mockABTestStream) SendHeader(md metadata.MD) error { return nil }
func (m *mockABTestStream) SetTrailer(md metadata.MD)       {}

// mockJudgingBackend satisfies fabric.ExecutionBackend for ABTest agent creation.
type mockJudgingBackend struct{}

func (m *mockJudgingBackend) Name() string { return "mock-judge-backend" }
func (m *mockJudgingBackend) ExecuteQuery(_ context.Context, _ string) (*fabric.QueryResult, error) {
	return &fabric.QueryResult{Type: "rows", RowCount: 0}, nil
}
func (m *mockJudgingBackend) GetSchema(_ context.Context, r string) (*fabric.Schema, error) {
	return &fabric.Schema{Name: r}, nil
}
func (m *mockJudgingBackend) GetMetadata(_ context.Context, _ string) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}
func (m *mockJudgingBackend) ListResources(_ context.Context, _ map[string]string) ([]fabric.Resource, error) {
	return nil, nil
}
func (m *mockJudgingBackend) Capabilities() *fabric.Capabilities { return fabric.NewCapabilities() }
func (m *mockJudgingBackend) Close() error                       { return nil }
func (m *mockJudgingBackend) Ping(_ context.Context) error       { return nil }
func (m *mockJudgingBackend) ExecuteCustomOperation(_ context.Context, _ string, _ map[string]interface{}) (interface{}, error) {
	return nil, nil
}

// newABTestServer builds a Server wired with a judge-capable mock LLM.
// The mock LLM returns valid judge JSON so scoring succeeds.
func newABTestServer(t *testing.T, js *JudgeServer) *Server {
	t.Helper()
	// The mock LLM must return valid judge JSON because runABTestSequentialScored
	// uses the agent's LLM as the judge LLM (JUDGE role falls back to AGENT role).
	mockLLM := &mockJudgeLLMProvider{model: "mock-judge-model"}
	ag := agent.NewAgent(&mockJudgingBackend{}, mockLLM)

	srv := NewServer(ag, nil)
	pool := map[string]agent.LLMProvider{
		"providerA": mockLLM,
		"providerB": mockLLM,
	}
	srv.SetProviderPool(pool, "providerA")
	if js != nil {
		srv.SetJudgeServer(js)
	}
	return srv
}

// TestABTest_SequentialScored_WithJudgeId verifies that a registered judge is
// used instead of the hardcoded "ab-test-scorer" when req.JudgeId is set.
func TestABTest_SequentialScored_WithJudgeId(t *testing.T) {
	js := newTestJudgeServer()
	ctx := context.Background()

	_, err := js.RegisterJudge(ctx, &loomv1.RegisterJudgeRequest{
		Config: &loomv1.JudgeConfig{
			Id:       "custom-scorer",
			Name:     "custom-scorer",
			Criteria: "domain-specific accuracy",
		},
	})
	require.NoError(t, err)

	srv := newABTestServer(t, js)
	stream := &mockABTestStream{ctx: ctx}

	err = srv.ABTest(&loomv1.ABTestRequest{
		Prompt:        "find duplicate integers in a list",
		JudgeId:       "custom-scorer",
		Mode:          loomv1.ABTestMode_AB_TEST_MODE_SEQUENTIAL_SCORED,
		ProviderNames: []string{"providerA", "providerB"},
	}, stream)

	require.NoError(t, err)
	// At least one event with a score should have been sent.
	stream.mu.Lock()
	events := stream.events
	stream.mu.Unlock()
	assert.NotEmpty(t, events, "expected streaming events from ABTest")
}

// TestABTest_SequentialScored_UnknownJudgeId verifies NotFound when judge_id is unknown.
func TestABTest_SequentialScored_UnknownJudgeId(t *testing.T) {
	js := newTestJudgeServer()
	srv := newABTestServer(t, js)
	stream := &mockABTestStream{ctx: context.Background()}

	err := srv.ABTest(&loomv1.ABTestRequest{
		Prompt:        "hello",
		JudgeId:       "does-not-exist",
		Mode:          loomv1.ABTestMode_AB_TEST_MODE_SEQUENTIAL_SCORED,
		ProviderNames: []string{"providerA"},
	}, stream)

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestABTest_SequentialScored_NoJudgeServer verifies FailedPrecondition when
// judge_id is set but no JudgeServer is wired.
func TestABTest_SequentialScored_NoJudgeServer(t *testing.T) {
	// Pass nil judgeServer intentionally.
	srv := newABTestServer(t, nil)
	stream := &mockABTestStream{ctx: context.Background()}

	err := srv.ABTest(&loomv1.ABTestRequest{
		Prompt:        "hello",
		JudgeId:       "some-judge",
		Mode:          loomv1.ABTestMode_AB_TEST_MODE_SEQUENTIAL_SCORED,
		ProviderNames: []string{"providerA"},
	}, stream)

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

// TestABTest_SequentialScored_NoJudgeId verifies the default path (no judge_id)
// still works after the wiring changes — hardcoded scorer is used.
func TestABTest_SequentialScored_NoJudgeId(t *testing.T) {
	srv := newABTestServer(t, nil) // no judge server needed for default path
	stream := &mockABTestStream{ctx: context.Background()}

	err := srv.ABTest(&loomv1.ABTestRequest{
		Prompt:        "write a Go function that reverses a string",
		Mode:          loomv1.ABTestMode_AB_TEST_MODE_SEQUENTIAL_SCORED,
		ProviderNames: []string{"providerA", "providerB"},
	}, stream)

	require.NoError(t, err)
	stream.mu.Lock()
	events := stream.events
	stream.mu.Unlock()
	assert.NotEmpty(t, events)
}
