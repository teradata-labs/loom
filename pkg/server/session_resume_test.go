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

package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/eventlog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage/backend"
)

// resumeLLM plays a script of responses; a nil entry cancels the turn,
// standing in for the process dying during that LLM call.
type resumeLLM struct {
	mu     sync.Mutex
	script []*llmtypes.LLMResponse
	cancel context.CancelFunc
	calls  int
}

func (l *resumeLLM) Chat(ctx context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := l.calls
	l.calls++
	if i < len(l.script) {
		if l.script[i] == nil {
			l.cancel()
			return nil, context.Canceled
		}
		return l.script[i], nil
	}
	return &llmtypes.LLMResponse{Content: "resumed reply"}, nil
}
func (l *resumeLLM) Name() string  { return "resume-test" }
func (l *resumeLLM) Model() string { return "resume-test-v1" }

type resumeProbeTool struct{ calls atomic.Int32 }

func (p *resumeProbeTool) Name() string        { return "probe" }
func (p *resumeProbeTool) Description() string { return "counts calls" }
func (p *resumeProbeTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (p *resumeProbeTool) Backend() string { return "" }
func (p *resumeProbeTool) Execute(context.Context, map[string]interface{}) (*shuttle.Result, error) {
	p.calls.Add(1)
	return &shuttle.Result{Success: true, Data: "probed"}, nil
}

// resumeStores is a migrated SQLite storage backend shared by the "crashed"
// process and the server that resumes it.
type resumeStores struct {
	sessions agent.SessionStorage
	log      eventlog.EventLogStore
}

func newResumeStores(t *testing.T) resumeStores {
	t.Helper()
	sb, err := backend.NewSQLiteBackend(&loomv1.SQLiteStorageConfig{Path: filepath.Join(t.TempDir(), "loom.db")}, observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sb.Close() })
	require.NoError(t, sb.Migrate(context.Background()))
	return resumeStores{sessions: sb.SessionStorage(), log: sb.EventLogStore()}
}

func (r resumeStores) agent(llm agent.LLMProvider, tool shuttle.Tool) *agent.Agent {
	// The LLM pattern classifier would consume a scripted response.
	cfg := agent.DefaultConfig()
	cfg.PatternConfig = agent.DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := agent.NewAgent(nil, llm,
		agent.WithConfig(cfg),
		agent.WithName("resume-agent"),
		agent.WithMemory(agent.NewMemoryWithStore(r.sessions)),
		agent.WithEventLog(r.log),
	)
	if tool != nil {
		ag.RegisterTool(tool)
	}
	return ag
}

func (r resumeStores) server(t *testing.T, ag *agent.Agent) *MultiAgentServer {
	srv := NewMultiAgentServer(map[string]*agent.Agent{"resume-agent": ag}, r.sessions)
	srv.SetLogger(zaptest.NewLogger(t))
	return srv
}

// crashTurn runs one turn that requests the probe tool and then dies in the
// next LLM call.
func (r resumeStores) crashTurn(t *testing.T, sessionID string, probe *resumeProbeTool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	llm := &resumeLLM{cancel: cancel, script: []*llmtypes.LLMResponse{
		{ToolCalls: []llmtypes.ToolCall{{ID: "call-1", Name: "probe", Input: map[string]interface{}{}}}},
		nil,
	}}
	_, err := r.agent(llm, probe).Chat(ctx, sessionID, "probe it")
	require.Error(t, err)
}

func TestResumeSessionRPC_FinishesACrashedTurnWithoutRerunningTools(t *testing.T) {
	stores := newResumeStores(t)
	probe := &resumeProbeTool{}
	stores.crashTurn(t, "s-rpc", probe)
	require.EqualValues(t, 1, probe.calls.Load())

	// A restarted server: a new agent over the same stores.
	srv := stores.server(t, stores.agent(&resumeLLM{}, probe))
	resp, err := srv.ResumeSession(context.Background(), &loomv1.ResumeSessionRequest{SessionId: "s-rpc"})
	require.NoError(t, err)
	require.Equal(t, loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_RESUMED, resp.GetOutcome())
	require.Equal(t, "resumed reply", resp.GetContent())
	require.EqualValues(t, 1, probe.calls.Load(), "resume ran the tool again")

	resp, err = srv.ResumeSession(context.Background(), &loomv1.ResumeSessionRequest{SessionId: "s-rpc"})
	require.NoError(t, err)
	require.Equal(t, loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_NOTHING_TO_RESUME, resp.GetOutcome())
}

// interruptedBatch saves a session whose last turn asked for the probe tool
// and stopped before any tool row: the state a crash during the call leaves.
// With recorded set, the batch's LLM call and the call's intent are in the
// log (no result), which makes the call an execution gap.
func (r resumeStores) interruptedBatch(t *testing.T, sessionID string, recorded bool) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, r.sessions.SaveSession(ctx, &agent.Session{
		ID: sessionID, AgentID: "resume-agent", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	require.NoError(t, r.sessions.SaveMessage(ctx, sessionID,
		&agent.Message{Role: "user", Content: "probe it", Timestamp: time.Now()}, true))
	calls := []llmtypes.ToolCall{{ID: "call-1", Name: "probe", Input: map[string]interface{}{}}}
	require.NoError(t, r.sessions.SaveMessage(ctx, sessionID,
		&agent.Message{Role: "assistant", ToolCalls: calls, Timestamp: time.Now()}, false))
	if !recorded {
		return
	}
	llmResult, err := json.Marshal(agent.LLMResponse{ToolCalls: calls})
	require.NoError(t, err)
	require.NoError(t, r.log.RecordResult(ctx, sessionID, 0, 0, eventlog.EventLLMCallResult, "", llmResult))
	require.NoError(t, r.log.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventToolCallIntent, "call-1",
		[]byte(`{"tool_name":"probe","input":{}}`)))
}

func TestResumeSessionRPC_GapThenResolveThenResume(t *testing.T) {
	stores := newResumeStores(t)
	ctx := context.Background()
	stores.interruptedBatch(t, "s-gap", true)
	probe := &resumeProbeTool{}
	srv := stores.server(t, stores.agent(&resumeLLM{}, probe))

	resp, err := srv.ResumeSession(ctx, &loomv1.ResumeSessionRequest{SessionId: "s-gap"})
	require.NoError(t, err)
	require.Equal(t, loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_EXECUTION_GAP, resp.GetOutcome())
	require.Equal(t, "probe", resp.GetGap().GetToolName())
	require.Zero(t, probe.calls.Load(), "a call with an unknown outcome was run")

	resolved, err := srv.ResolveExecutionGap(ctx, &loomv1.ResolveExecutionGapRequest{
		SessionId:  "s-gap",
		Outcome:    loomv1.ExecutionGapOutcome_EXECUTION_GAP_OUTCOME_SUCCEEDED,
		ResultJson: `{"rows": 3}`,
	})
	require.NoError(t, err)
	require.Equal(t, resp.GetGap().GetBatchPosition(), resolved.GetGap().GetBatchPosition())

	resp, err = srv.ResumeSession(ctx, &loomv1.ResumeSessionRequest{SessionId: "s-gap"})
	require.NoError(t, err)
	require.Equal(t, loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_RESUMED, resp.GetOutcome())
	require.Zero(t, probe.calls.Load(), "the operator-resolved call was run")

	_, err = srv.ResolveExecutionGap(ctx, &loomv1.ResolveExecutionGapRequest{
		SessionId: "s-gap", Outcome: loomv1.ExecutionGapOutcome_EXECUTION_GAP_OUTCOME_FAILED,
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "no gap is left to resolve")
}

func TestResumeSessionRPC_Refusals(t *testing.T) {
	stores := newResumeStores(t)
	ctx := context.Background()
	probe := &resumeProbeTool{}
	srv := stores.server(t, stores.agent(&resumeLLM{}, probe))

	_, err := srv.ResumeSession(ctx, &loomv1.ResumeSessionRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = srv.ResumeSession(ctx, &loomv1.ResumeSessionRequest{SessionId: "no-such-session"})
	require.Equal(t, codes.NotFound, status.Code(err))

	_, err = srv.ResolveExecutionGap(ctx, &loomv1.ResolveExecutionGapRequest{
		SessionId: "s", Outcome: loomv1.ExecutionGapOutcome_EXECUTION_GAP_OUTCOME_SUCCEEDED,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "SUCCEEDED without result_json")

	// A turn that ran with the log off cannot be resumed: nothing says
	// whether its call already ran.
	stores.interruptedBatch(t, "s-unlogged", false)
	_, err = srv.ResumeSession(ctx, &loomv1.ResumeSessionRequest{SessionId: "s-unlogged"})
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "got %v", err)
	require.Zero(t, probe.calls.Load())
}

// A session leased by another process (a live turn or another replica's
// resume) is refused with Aborted, so the caller can retry later.
func TestResumeSessionRPC_BusySessionIsAborted(t *testing.T) {
	stores := newResumeStores(t)
	ctx := context.Background()
	stores.interruptedBatch(t, "s-busy", true)
	won, err := stores.log.AcquireSessionLease(ctx, "s-busy", "other-replica", time.Minute)
	require.NoError(t, err)
	require.True(t, won)

	srv := stores.server(t, stores.agent(&resumeLLM{}, &resumeProbeTool{}))
	_, err = srv.ResumeSession(ctx, &loomv1.ResumeSessionRequest{SessionId: "s-busy"})
	require.Equal(t, codes.Aborted, status.Code(err), "got %v", err)
}
