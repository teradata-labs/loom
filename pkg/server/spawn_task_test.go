// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"github.com/teradata-labs/loom/pkg/types"
)

// spawnTestServer builds a server whose registry can instantiate the named
// agent, so SpawnSubAgent runs the real path.
func spawnTestServer(t *testing.T, llm *replyingLLM) *MultiAgentServer {
	t.Helper()

	logger := zaptest.NewLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   t.TempDir(),
		DBPath:      ":memory:",
		Logger:      logger,
		LLMProvider: llm,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })

	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:        "helper",
		Description: "spawnable helper",
	})

	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, registry)
	return srv
}

// parentSession creates the parent session a spawn hangs off, which the store
// requires before a child session can reference it.
func parentSession(t *testing.T, srv *MultiAgentServer) string {
	t.Helper()
	id := GenerateSessionID()
	require.NoError(t, srv.sessionStore.SaveSession(context.Background(), &agent.Session{
		ID:      id,
		AgentID: "parent",
	}))
	// A spawn starts background goroutines that outlive the request by design;
	// tear them down with the test so they cannot outlive its logger.
	t.Cleanup(func() { srv.cleanupSpawnedAgentsByParent(id) })
	return id
}

// TestSpawn_WithTaskReturnsTheAnswer proves the spawn contract the response
// struct has always declared: a spawn carrying a task runs it and hands the
// sub-agent's answer back as this call's result, so the parent reads the answer
// where it called and no reply has to find its way home.
func TestSpawn_WithTaskReturnsTheAnswer(t *testing.T) {
	llm := &replyingLLM{}
	srv := spawnTestServer(t, llm)

	resp, err := srv.SpawnSubAgent(context.Background(), &builtin.SpawnSubAgentRequest{
		ParentSessionID: parentSession(t, srv),
		ParentAgentID:   "parent",
		AgentID:         "helper",
		InitialMessage:  "check these queries",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "completed", resp.Status,
		"a spawn carrying a task reports completion, not just creation")
	assert.NotEmpty(t, resp.Output, "the sub-agent's answer is the call's result")
	assert.NotEmpty(t, resp.SubAgentID)
	assert.Greater(t, resp.DurationMs, int64(-1), "the run is timed")
}

// TestSpawn_WithoutTaskKeepsCreateOnlyBehaviour proves the back-compatible
// half: with no task there is nothing to run, so the call creates the agent and
// returns as it always did.
func TestSpawn_WithoutTaskKeepsCreateOnlyBehaviour(t *testing.T) {
	llm := &replyingLLM{}
	srv := spawnTestServer(t, llm)

	resp, err := srv.SpawnSubAgent(context.Background(), &builtin.SpawnSubAgentRequest{
		ParentSessionID: parentSession(t, srv),
		ParentAgentID:   "parent",
		AgentID:         "helper",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "spawned", resp.Status)
	assert.Empty(t, resp.Output, "no task means no answer to report")
}

// TestSpawn_CarriesNoTeamBlock proves a plain spawned sub-agent is told nothing
// about messaging: it receives a task as a call and returns an answer as that
// call's result, so it has no peers to address.
func TestSpawn_CarriesNoTeamBlock(t *testing.T) {
	llm := &replyingLLM{}
	srv := spawnTestServer(t, llm)

	_, err := srv.SpawnSubAgent(context.Background(), &builtin.SpawnSubAgentRequest{
		ParentSessionID: parentSession(t, srv),
		ParentAgentID:   "parent",
		AgentID:         "helper",
		InitialMessage:  "do the thing",
	})
	require.NoError(t, err)

	assert.NotContains(t, llm.prompt(), "WORKFLOW COMMUNICATION",
		"a spawned sub-agent with no subscriptions carries no team block")
}

// failingLLM fails every turn, standing in for a sub-agent whose task blows up.
type failingLLM struct{}

func (failingLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return nil, errors.New("sub-agent exploded")
}
func (failingLLM) Name() string  { return "failing-llm" }
func (failingLLM) Model() string { return "failing-model" }

// TestSpawn_FailedTaskCleansUp proves the failure path: a task that errors
// surfaces as the spawn call's error, and the half-built sub-agent is torn down
// rather than left tracked — the parent gets a failure, not a phantom child.
func TestSpawn_FailedTaskCleansUp(t *testing.T) {
	logger := zaptest.NewLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   t.TempDir(),
		DBPath:      ":memory:",
		Logger:      logger,
		LLMProvider: failingLLM{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })
	registry.RegisterConfig(&loomv1.AgentConfig{Name: "helper", Description: "spawnable helper"})

	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, registry)
	parent := parentSession(t, srv)

	resp, err := srv.SpawnSubAgent(context.Background(), &builtin.SpawnSubAgentRequest{
		ParentSessionID: parent,
		ParentAgentID:   "parent",
		AgentID:         "helper",
		InitialMessage:  "do the thing",
	})

	require.Error(t, err, "a failed task must surface as the spawn call's error")
	assert.Nil(t, resp)
	assert.Zero(t, srv.countSpawnedAgentsByParent(parent),
		"the half-built sub-agent must not stay tracked after a failed task")
}

// spawningLLM is a parent's brain: on its first turn it delegates by calling
// manage_ephemeral_agents(spawn, ...), then reports whatever came back. It
// records every tool result it is shown, which is where a sub-agent's answer
// must appear for the parent to be able to use it.
type spawningLLM struct {
	mu          sync.Mutex
	toolResults []string
	delegated   bool
}

func (m *spawningLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	for _, msg := range messages {
		if msg.Role == "tool" {
			m.toolResults = append(m.toolResults, msg.Content)
		}
	}
	first := !m.delegated
	m.delegated = true
	m.mu.Unlock()

	if first {
		return &llmtypes.LLMResponse{
			ToolCalls: []types.ToolCall{{
				ID:   "spawn-1",
				Name: "manage_ephemeral_agents",
				Input: map[string]interface{}{
					"command":         "spawn",
					"agent_id":        "helper",
					"initial_message": "check these queries",
				},
			}},
			Usage: llmtypes.Usage{InputTokens: 10, OutputTokens: 5},
		}, nil
	}
	return &llmtypes.LLMResponse{
		Content: "delegated and read the answer",
		Usage:   llmtypes.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

func (m *spawningLLM) Name() string  { return "spawning-llm" }
func (m *spawningLLM) Model() string { return "spawning-model" }

func (m *spawningLLM) results() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.toolResults...)
}

// answeringLLM is the spawned helper: it answers with a distinctive string so
// the test can prove that exact text travelled back to the parent.
type answeringLLM struct{}

func (answeringLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return &llmtypes.LLMResponse{
		Content: "SUBAGENT-VERDICT: query 2 does a full scan",
		Usage:   llmtypes.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}
func (answeringLLM) Name() string  { return "answering-llm" }
func (answeringLLM) Model() string { return "answering-model" }

// TestSpawn_ParentReadsAnswerAsToolResult is the path-1+5 gate: an ordinary
// agent holding manage_ephemeral_agents delegates to a sub-agent and reads the
// sub-agent's answer in the tool result, in the same place every other tool
// result appears. This exercises the whole chain — tool call, handler, child
// run, response mapping — not just the server call.
func TestSpawn_ParentReadsAnswerAsToolResult(t *testing.T) {
	logger := zaptest.NewLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   t.TempDir(),
		DBPath:      ":memory:",
		Logger:      logger,
		LLMProvider: answeringLLM{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })
	registry.RegisterConfig(&loomv1.AgentConfig{Name: "helper", Description: "spawnable helper"})

	parentLLM := &spawningLLM{}
	parentAgent := agent.NewAgent(&mockBackend{}, parentLLM)

	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{"parent": parentAgent}, registry)
	parentSessionID := parentSession(t, srv)

	// Attach the spawn tool exactly as the Weave path does, stamped with this
	// parent's session and id.
	parentAgent.RegisterTool(builtin.NewManageEphemeralAgentsTool(srv, parentSessionID, "parent"))

	_, err = parentAgent.Chat(context.Background(), parentSessionID, "review my queries")
	require.NoError(t, err)

	joined := strings.Join(parentLLM.results(), "\n")
	require.NotEmpty(t, joined, "the parent must be shown a tool result")
	assert.Contains(t, joined, "SUBAGENT-VERDICT: query 2 does a full scan",
		"the sub-agent's answer must reach the parent as the tool's result")
	assert.Contains(t, joined, "completed",
		"the result reports the task ran, not merely that an agent was created")
}

// TestSpawn_OneShotChildIsDespawnedAfterAnswering proves the lifecycle: a child
// that answered and subscribes to nothing is finished — its answer is already
// the call's result and no command can give it another task — so it is torn
// down at once. Left alive it would hold one of the parent's ten spawn slots
// until the idle reaper ran, and a parent delegating repeatedly would hit the
// spawn limit for children that had all already answered.
func TestSpawn_OneShotChildIsDespawnedAfterAnswering(t *testing.T) {
	logger := zaptest.NewLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   t.TempDir(),
		DBPath:      ":memory:",
		Logger:      logger,
		LLMProvider: answeringLLM{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })
	registry.RegisterConfig(&loomv1.AgentConfig{Name: "helper", Description: "spawnable helper"})

	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, registry)
	parent := parentSession(t, srv)

	// Delegate more times than the spawn limit allows if nothing is reclaimed.
	for i := 0; i < 12; i++ {
		resp, spawnErr := srv.SpawnSubAgent(context.Background(), &builtin.SpawnSubAgentRequest{
			ParentSessionID: parent,
			ParentAgentID:   "parent",
			AgentID:         "helper",
			InitialMessage:  "task",
		})
		require.NoError(t, spawnErr, "delegation %d must not hit the spawn limit", i+1)
		require.Equal(t, "completed", resp.Status)
		require.NotEmpty(t, resp.Output, "the answer still comes back")
	}

	assert.Zero(t, srv.countSpawnedAgentsByParent(parent),
		"a one-shot child holds no slot once it has answered")
}

// TestSpawn_SubscribedChildSurvives proves the exception: a child that
// auto-subscribed is a live topic participant with a real lifetime, so it stays
// up after answering and remains the parent's to despawn.
func TestSpawn_SubscribedChildSurvives(t *testing.T) {
	logger := zaptest.NewLogger(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   t.TempDir(),
		DBPath:      ":memory:",
		Logger:      logger,
		LLMProvider: answeringLLM{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })
	registry.RegisterConfig(&loomv1.AgentConfig{Name: "helper", Description: "spawnable helper"})

	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, registry)
	parent := parentSession(t, srv)

	resp, err := srv.SpawnSubAgent(context.Background(), &builtin.SpawnSubAgentRequest{
		ParentSessionID: parent,
		ParentAgentID:   "parent",
		AgentID:         "helper",
		InitialMessage:  "task",
		AutoSubscribe:   []string{"audit-events"},
	})
	require.NoError(t, err)
	require.Equal(t, "completed", resp.Status)
	require.NotEmpty(t, resp.SubscribedTopics)

	assert.Equal(t, 1, srv.countSpawnedAgentsByParent(parent),
		"a subscribed child stays up as a topic participant")
}
