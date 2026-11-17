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

// Review round 2, B2: with the execution log on, a message injected into an
// agent's session while another turn holds the session's lease is refused with
// agent.ErrSessionBusy. That must not be treated like a delivered message.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// injectionLLM records the last message of every turn it is asked to run.
type injectionLLM struct {
	mu      sync.Mutex
	prompts []string
}

func (l *injectionLLM) Chat(_ context.Context, messages []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prompts = append(l.prompts, messages[len(messages)-1].Content)
	return &llmtypes.LLMResponse{Content: "ok"}, nil
}
func (l *injectionLLM) Name() string  { return "injection-test" }
func (l *injectionLLM) Model() string { return "injection-test-v1" }

// saw counts the turns whose last message contains substr.
func (l *injectionLLM) saw(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, p := range l.prompts {
		if strings.Contains(p, substr) {
			n++
		}
	}
	return n
}

// leaseSession saves sessionID (the lease row references it) and takes its
// lease as another holder, standing in for a live turn or a crashed process's
// leftover lease. The returned func frees it.
func (r resumeStores) leaseSession(t *testing.T, sessionID, holder string) (release func()) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, r.sessions.SaveSession(ctx, &agent.Session{
		ID: sessionID, AgentID: "resume-agent", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	won, err := r.log.AcquireSessionLease(ctx, sessionID, holder, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	return func() { require.NoError(t, r.log.ReleaseSessionLease(ctx, sessionID, holder)) }
}

func TestChatInjected_WaitsOutABusySession(t *testing.T) {
	stores := newResumeStores(t)
	llm := &injectionLLM{}
	ag := stores.agent(llm, nil)
	srv := stores.server(t, ag)
	srv.injectBusy = injectBusyPolicy{initial: 10 * time.Millisecond, max: 20 * time.Millisecond, total: 10 * time.Second}

	release := stores.leaseSession(t, "s-inject", "user-turn")
	time.AfterFunc(200*time.Millisecond, release)

	ctx := context.Background()
	err := srv.chatInjected(ctx, ctx, ag, "s-inject", "[MESSAGE FROM worker]: result", time.Second)
	require.NoError(t, err)
	require.Equal(t, 1, llm.saw("result"), "the message must reach the session exactly once")
}

func TestChatInjected_GivesUpWithoutRunningTheTurn(t *testing.T) {
	stores := newResumeStores(t)
	llm := &injectionLLM{}
	ag := stores.agent(llm, nil)
	srv := stores.server(t, ag)
	srv.injectBusy = injectBusyPolicy{initial: 10 * time.Millisecond, max: 20 * time.Millisecond, total: 150 * time.Millisecond}

	release := stores.leaseSession(t, "s-inject-busy", "user-turn")
	defer release()

	ctx := context.Background()
	err := srv.chatInjected(ctx, ctx, ag, "s-inject-busy", "[MESSAGE FROM worker]: result", time.Second)
	require.Error(t, err)
	require.True(t, injectNotDelivered(err), "a message that never reached the session must say so: %v", err)
	require.ErrorIs(t, err, agent.ErrSessionBusy)
	require.Zero(t, llm.saw("result"), "a refused injection ran a turn")
}

// The coordinator's reply path: a worker's message arrives while the user's own
// turn holds the coordinator's session. The message must stay in the queue and
// be delivered once the session is free, not acknowledged and lost.
func TestCoordinatorQueue_BusySessionKeepsTheMessageForRedelivery(t *testing.T) {
	stores := newResumeStores(t)
	llm := &injectionLLM{}
	coordinator := stores.agent(llm, nil)
	// The workflow needs a worker to initialize; it never runs, since the test
	// enqueues the worker's reply itself.
	worker := agent.NewAgent(&mockBackend{}, &injectionLLM{})
	agents := map[string]*agent.Agent{"test-workflow": coordinator, "test-workflow:worker": worker}

	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    ":memory:",
		Logger:    newTestLogger(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "test-workflow",
		Metadata: map[string]string{"role": "coordinator", "workflow": "test-workflow"},
	})
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:     "test-workflow:worker",
		Metadata: map[string]string{"role": "executor", "workflow": "test-workflow"},
	})

	srv := setupBroadcastTestServer(t, agents, registry)
	srv.mu.Lock()
	for name, ag := range agents {
		srv.agents[name] = ag
	}
	srv.mu.Unlock()
	// Short waits keep one delivery attempt well inside the test's window; the
	// queue allows three attempts, so the lease must be freed after the first.
	srv.injectBusy = injectBusyPolicy{initial: 20 * time.Millisecond, max: 50 * time.Millisecond, total: 400 * time.Millisecond}

	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	srv.StartMessageQueueMonitor(monitorCtx)
	t.Cleanup(stopMonitor)

	sessionID := GenerateSessionID()
	release := stores.leaseSession(t, sessionID, "user-turn")
	defer release()
	require.NoError(t, srv.spawnWorkflowSubAgents(context.Background(), coordinator, "test-workflow", sessionID))
	t.Cleanup(func() {
		srv.workflowSubAgentsMu.Lock()
		defer srv.workflowSubAgentsMu.Unlock()
		for _, wctx := range srv.workflowSubAgents {
			if wctx.cancelFunc != nil {
				wctx.cancelFunc()
			}
		}
	})

	require.NoError(t, srv.messageQueue.Enqueue(context.Background(), &communication.QueueMessage{
		ID:          "reply-1",
		FromAgent:   "test-workflow:worker",
		ToAgent:     "test-workflow",
		MessageType: "result",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("worker result: 42")},
		},
	}))

	// While the session is busy the message is not delivered, and (below) not lost.
	require.Never(t, func() bool { return llm.saw("worker result: 42") > 0 },
		1500*time.Millisecond, 50*time.Millisecond, "the message reached a session another turn was driving")

	release()
	require.Eventually(t, func() bool { return llm.saw("worker result: 42") == 1 },
		15*time.Second, 50*time.Millisecond, "the worker's reply was lost while the session was busy")
	require.Eventually(t, func() bool { return srv.messageQueue.GetQueueDepth("test-workflow") == 0 },
		5*time.Second, 50*time.Millisecond, "the delivered message is still queued")
}
