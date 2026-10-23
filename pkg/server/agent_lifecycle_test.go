// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// newTestMultiAgentServer creates a MultiAgentServer for lifecycle tests.
// Agents are created with a mock LLM and no backend.
func newTestMultiAgentServer(t *testing.T) *MultiAgentServer {
	t.Helper()
	llm := &mockLLMForMultiAgent{}
	ag := agent.NewAgent(nil, llm, agent.WithName("test-agent"))
	agents := map[string]*agent.Agent{
		"default": ag,
	}
	srv := NewMultiAgentServer(agents, nil)
	srv.SetLogger(zaptest.NewLogger(t))
	return srv
}

// --- CreateAgentFromConfig ---

func TestAgentLifecycle_CreateAgentFromConfig(t *testing.T) {
	tests := []struct {
		name       string
		req        *loomv1.CreateAgentRequest
		wantErr    bool
		wantCode   codes.Code
		wantStatus string
	}{
		{
			name:     "missing config and config_path",
			req:      &loomv1.CreateAgentRequest{},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid config - empty name",
			req: &loomv1.CreateAgentRequest{
				Config: &loomv1.AgentConfig{
					Name: "",
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "config_path file not found",
			req: &loomv1.CreateAgentRequest{
				ConfigPath: "/nonexistent/path/agent.yaml",
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "valid config creates agent",
			req: &loomv1.CreateAgentRequest{
				Config: &loomv1.AgentConfig{
					Name:         "new-agent",
					Description:  "A test agent",
					SystemPrompt: "Be helpful.",
				},
			},
			wantErr:    false,
			wantStatus: "running",
		},
		{
			name: "valid config with minimal fields",
			req: &loomv1.CreateAgentRequest{
				Config: &loomv1.AgentConfig{
					Name: "minimal-agent",
				},
			},
			wantErr:    false,
			wantStatus: "running",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestMultiAgentServer(t)
			ctx := context.Background()

			resp, err := srv.CreateAgentFromConfig(ctx, tt.req)
			if tt.wantErr {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok, "expected gRPC status error")
				assert.Equal(t, tt.wantCode, st.Code())
				return
			}

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.NotEmpty(t, resp.Id, "agent ID should be set")
			assert.Equal(t, tt.req.Config.Name, resp.Name)
			assert.Equal(t, tt.wantStatus, resp.Status)
			assert.True(t, resp.CreatedAt > 0, "created_at should be set")
			assert.True(t, resp.UpdatedAt > 0, "updated_at should be set")

			// Verify the agent is actually registered
			agentIDs := srv.GetAgentIDs()
			assert.Contains(t, agentIDs, resp.Id, "new agent should be in server's agents map")
		})
	}
}

type systemPromptRecordingLLM struct {
	mu     sync.Mutex
	system []string
}

func (m *systemPromptRecordingLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range messages {
		if msg.Role == "system" {
			m.system = append(m.system, msg.Content)
		}
	}
	return &llmtypes.LLMResponse{Content: "ok", StopReason: "end_turn"}, nil
}

func (m *systemPromptRecordingLLM) Name() string  { return "system-prompt-recorder" }
func (m *systemPromptRecordingLLM) Model() string { return "recorder-model" }

func (m *systemPromptRecordingLLM) systemMessages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.system...)
}

func TestAgentLifecycle_CreateAgentFromConfig_RuntimeSystemPrompt(t *testing.T) {
	const prompt = "Score each Mastermind guess against the secret code."
	tmpDir := t.TempDir()
	llm := &systemPromptRecordingLLM{}
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "registry.db"),
		LLMProvider: llm,
		Logger:      zaptest.NewLogger(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registry.Close()) })

	srv := newTestMultiAgentServer(t)
	srv.SetAgentRegistry(registry)
	ctx := context.Background()
	resp, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{
			Name:         "mastermind-pilot",
			Description:  "Mastermind pilot agent",
			SystemPrompt: prompt,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "Mastermind pilot agent", resp.Metadata["description"])

	ag, _, err := srv.getAgent(resp.Id)
	require.NoError(t, err)
	_, err = ag.Chat(ctx, "pilot-session", "Guess: red green blue yellow")
	require.NoError(t, err)

	found := false
	for _, msg := range llm.systemMessages() {
		found = found || strings.Contains(msg, prompt)
	}
	assert.True(t, found, "LLM never received the configured system prompt")
}

func TestAgentLifecycle_DirectConfigKeepsROMAndMetadata(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()
	created, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{
			Name:         "direct-agent",
			SystemPrompt: "Test direct agent.",
			Rom:          "none",
			Metadata:     map[string]string{"backend_path": "backends/teradata.yaml"},
		},
	})
	require.NoError(t, err)
	before, _, err := srv.getAgent(created.Id)
	require.NoError(t, err)
	assert.Equal(t, "none", before.GetConfig().Rom)
	assert.Equal(t, "backends/teradata.yaml", before.GetConfig().Metadata["backend_path"])

	_, err = srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{
		AgentId: created.Id,
		Config: &loomv1.AgentConfig{
			Name:         "direct-agent",
			SystemPrompt: "Updated direct agent.",
			Rom:          "TD",
			Metadata:     map[string]string{"backend_path": "backends/updated.yaml"},
		},
	})
	require.NoError(t, err)
	after, _, err := srv.getAgent(created.Id)
	require.NoError(t, err)
	assert.Equal(t, "TD", after.GetConfig().Rom)
	assert.Equal(t, "backends/updated.yaml", after.GetConfig().Metadata["backend_path"])
}

func TestAgentLifecycle_ReloadFromFileUpdatesRuntimePrompt(t *testing.T) {
	const (
		oldPrompt = "Use the old experiment rules."
		newPrompt = "Use the updated experiment rules."
	)
	tmpDir := t.TempDir()
	llm := &systemPromptRecordingLLM{}
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "registry.db"),
		LLMProvider: llm,
		Logger:      zaptest.NewLogger(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registry.Close()) })

	srv := newTestMultiAgentServer(t)
	srv.SetAgentRegistry(registry)
	ctx := context.Background()
	created, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{
			Name:         "file-reload-agent",
			Description:  "Old description",
			SystemPrompt: oldPrompt,
		},
	})
	require.NoError(t, err)
	before, _, err := srv.getAgent(created.Id)
	require.NoError(t, err)

	updated := &loomv1.AgentConfig{
		Name:         "file-reload-agent",
		Description:  "New description",
		SystemPrompt: newPrompt,
		Rom:          "none",
		Behavior:     &loomv1.BehaviorConfig{MaxIterations: 2},
	}
	require.NoError(t, agent.SaveAgentConfig(updated, filepath.Join(tmpDir, "agents", updated.Name+".yaml")))
	response, err := srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{AgentId: created.Id, ReloadFromFile: true})
	require.NoError(t, err)
	assert.Equal(t, updated.Description, response.Metadata["description"])
	assert.Equal(t, updated.SystemPrompt, response.Config.SystemPrompt)

	after, _, err := srv.getAgent(created.Id)
	require.NoError(t, err)
	assert.NotSame(t, before, after)
	assert.Equal(t, created.Id, after.GetID())
	assert.Equal(t, updated.Rom, after.GetConfig().Rom)
	assert.Equal(t, 2, after.GetConfig().MaxIterations)
	_, err = after.Chat(ctx, "reloaded-session", "Guess: red green blue yellow")
	require.NoError(t, err)
	system := llm.systemMessages()
	require.NotEmpty(t, system)
	assert.Contains(t, system[len(system)-1], newPrompt)
	assert.NotContains(t, system[len(system)-1], oldPrompt)
}

func TestAgentLifecycle_ReloadFromFileUsesServerCallback(t *testing.T) {
	tmpDir := t.TempDir()
	llm := &systemPromptRecordingLLM{}
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "registry.db"),
		LLMProvider: llm,
		Logger:      zaptest.NewLogger(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registry.Close()) })

	srv := newTestMultiAgentServer(t)
	srv.SetAgentRegistry(registry)
	ctx := context.Background()
	config := &loomv1.AgentConfig{Name: "callback-agent", SystemPrompt: "Old rules."}
	require.NoError(t, agent.SaveAgentConfig(config, filepath.Join(tmpDir, "agents", config.Name+".yaml")))
	require.NoError(t, registry.LoadAgents(ctx))
	info, err := registry.GetAgentInfo(config.Name)
	require.NoError(t, err)
	before := agent.NewAgent(&mockBackend{}, llm, agent.WithName(config.Name), agent.WithSystemPrompt(config.SystemPrompt))
	before.SetID(info.ID)
	srv.AddAgent(info.ID, before)

	var callbackAgent *agent.Agent
	registry.SetReloadCallback(func(name, guid string, updated *loomv1.AgentConfig) error {
		callbackAgent = agent.NewAgent(&mockBackend{}, llm,
			agent.WithName(name),
			agent.WithSystemPrompt(updated.SystemPrompt),
			agent.WithDescription(updated.Description))
		callbackAgent.SetID(guid)
		return srv.UpdateAgent(guid, callbackAgent)
	})

	config.Description = "Updated via callback"
	config.SystemPrompt = "New rules."
	require.NoError(t, agent.SaveAgentConfig(config, filepath.Join(tmpDir, "agents", config.Name+".yaml")))
	response, err := srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{AgentId: info.ID, ReloadFromFile: true})
	require.NoError(t, err)
	require.NotNil(t, callbackAgent)

	after, _, err := srv.getAgent(info.ID)
	require.NoError(t, err)
	assert.NotSame(t, before, after)
	assert.Same(t, callbackAgent, after)
	assert.Equal(t, config.Description, response.Metadata["description"])
	assert.Equal(t, config.SystemPrompt, response.Config.SystemPrompt)
	_, err = after.Chat(ctx, "callback-session", "Guess: red green blue yellow")
	require.NoError(t, err)
	system := llm.systemMessages()
	require.NotEmpty(t, system)
	assert.Contains(t, system[len(system)-1], config.SystemPrompt)

	registry.SetReloadCallback(func(string, string, *loomv1.AgentConfig) error { return nil })
	config.SystemPrompt = "Rules that were not applied."
	require.NoError(t, agent.SaveAgentConfig(config, filepath.Join(tmpDir, "agents", config.Name+".yaml")))
	_, err = srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{AgentId: info.ID, ReloadFromFile: true})
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))
	unchanged, _, getErr := srv.getAgent(info.ID)
	require.NoError(t, getErr)
	assert.Same(t, after, unchanged)

	registry.SetReloadCallback(nil)
	_, err = srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{AgentId: info.ID, ReloadFromFile: true})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	unchanged, _, getErr = srv.getAgent(info.ID)
	require.NoError(t, getErr)
	assert.Same(t, after, unchanged)
}

func TestAgentLifecycle_ReloadFromFilePrefersRegistryRuntime(t *testing.T) {
	tmpDir := t.TempDir()
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "registry.db"),
		LLMProvider: &systemPromptRecordingLLM{},
		Logger:      zaptest.NewLogger(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registry.Close()) })

	srv := newTestMultiAgentServer(t)
	srv.SetAgentRegistry(registry)
	ctx := context.Background()
	config := &loomv1.AgentConfig{Name: "registry-agent", SystemPrompt: "Before reload."}
	created, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{Config: config})
	require.NoError(t, err)

	callbackCalled := false
	registry.SetReloadCallback(func(string, string, *loomv1.AgentConfig) error {
		callbackCalled = true
		return nil
	})
	config.SystemPrompt = "After reload."
	require.NoError(t, agent.SaveAgentConfig(config, filepath.Join(tmpDir, "agents", config.Name+".yaml")))
	_, err = srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{AgentId: created.Id, ReloadFromFile: true})
	require.NoError(t, err)
	assert.False(t, callbackCalled)

	runtime, _, err := srv.getAgent(created.Id)
	require.NoError(t, err)
	registered, err := registry.GetAgent(ctx, created.Id)
	require.NoError(t, err)
	assert.Same(t, registered, runtime)
	assert.Equal(t, config.SystemPrompt, runtime.GetConfig().SystemPrompt)
}

func TestAgentLifecycle_CreateAgentFromConfig_DuplicateNames(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	// Create first agent
	resp1, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{Name: "dup-agent"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp1)

	// Create second agent with same name - should succeed (different UUID)
	resp2, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{Name: "dup-agent"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp2)

	// Both should have unique IDs
	assert.NotEqual(t, resp1.Id, resp2.Id, "duplicate-name agents should still get unique IDs")
}

// --- GetAgent ---

func TestAgentLifecycle_GetAgent(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	// Create an agent to retrieve
	createResp, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{Name: "get-me", Description: "Findable agent"},
	})
	require.NoError(t, err)

	tests := []struct {
		name     string
		agentID  string
		wantErr  bool
		wantCode codes.Code
		wantName string
	}{
		{
			name:     "missing agent_id",
			agentID:  "",
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "not found",
			agentID:  "nonexistent-id",
			wantErr:  true,
			wantCode: codes.NotFound,
		},
		{
			name:     "found by ID",
			agentID:  createResp.Id,
			wantErr:  false,
			wantName: "get-me",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := srv.GetAgent(ctx, &loomv1.GetAgentRequest{AgentId: tt.agentID})
			if tt.wantErr {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok, "expected gRPC status error")
				assert.Equal(t, tt.wantCode, st.Code())
				return
			}

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, tt.wantName, resp.Name)
			assert.Equal(t, createResp.Id, resp.Id)
			assert.Equal(t, "running", resp.Status)
		})
	}
}

func TestAgentLifecycle_GetAgentIncludesRegistryConfig(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    filepath.Join(t.TempDir(), "registry.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registry.Close()) })

	t.Setenv("AGENT_LIFECYCLE_TEST_PASSWORD", "test-secret")
	config, err := agent.LoadConfigFromString(`
apiVersion: loom/v1
kind: Agent
metadata:
  name: test-agent
  description: Experiment subject
spec:
  system_prompt: Follow the experiment protocol.
  memory:
    type: postgres
    dsn: postgres://u:${AGENT_LIFECYCLE_TEST_PASSWORD}@db/x
    graph_memory:
      enabled: true
`)
	require.NoError(t, err)
	require.Equal(t, "postgres://u:test-secret@db/x", config.GetMemory().GetDsn())
	registry.RegisterConfig(config)
	srv.SetAgentRegistry(registry)
	agentIDs := srv.GetAgentIDs()
	require.Len(t, agentIDs, 1)

	checkResponse := func(t *testing.T, response *loomv1.AgentInfo) {
		t.Helper()
		require.NotNil(t, response.Config)
		assert.Equal(t, config.Name, response.Config.Name)
		assert.Equal(t, config.SystemPrompt, response.Config.SystemPrompt)
		assert.Equal(t, "[REDACTED]", response.Config.GetMemory().GetDsn())
		assert.True(t, response.Config.GetMemory().GetGraphMemory().GetEnabled())
		data, err := protojson.Marshal(response)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "test-secret")
	}

	t.Run("without lifecycle state", func(t *testing.T) {
		response, err := srv.GetAgent(context.Background(), &loomv1.GetAgentRequest{AgentId: agentIDs[0]})
		require.NoError(t, err)
		checkResponse(t, response)
		response.Config.Memory.GraphMemory.Enabled = false
		response.Config.Memory.Dsn = "changed"
		assert.Equal(t, "postgres://u:test-secret@db/x", config.GetMemory().GetDsn())
		assert.True(t, config.GetMemory().GetGraphMemory().GetEnabled())
	})

	t.Run("with lifecycle state but no config", func(t *testing.T) {
		started, err := srv.StartAgent(context.Background(), &loomv1.StartAgentRequest{AgentId: agentIDs[0]})
		require.NoError(t, err)
		checkResponse(t, started)

		response, err := srv.GetAgent(context.Background(), &loomv1.GetAgentRequest{AgentId: agentIDs[0]})
		require.NoError(t, err)
		checkResponse(t, response)
	})
}

func TestAgentLifecycle_StateConfigPrecedesRegistryConfig(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	stateConfig := &loomv1.AgentConfig{
		Name:        "state-agent",
		Description: "Lifecycle state",
		Memory: &loomv1.MemoryConfig{
			Dsn:         "postgres://state:secret@db/x",
			GraphMemory: &loomv1.GraphMemoryConfig{Enabled: true},
		},
	}
	created, err := srv.CreateAgentFromConfig(context.Background(), &loomv1.CreateAgentRequest{Config: stateConfig})
	require.NoError(t, err)
	assert.Equal(t, "[REDACTED]", created.Config.GetMemory().GetDsn())
	assert.Equal(t, "postgres://state:secret@db/x", stateConfig.GetMemory().GetDsn())

	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    filepath.Join(t.TempDir(), "registry.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registry.Close()) })
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:        "state-agent",
		Description: "Registry config",
		Memory:      &loomv1.MemoryConfig{Dsn: "postgres://registry:secret@db/x"},
	})
	srv.SetAgentRegistry(registry)

	response, err := srv.GetAgent(context.Background(), &loomv1.GetAgentRequest{AgentId: created.Id})
	require.NoError(t, err)
	require.NotNil(t, response.Config)
	assert.Equal(t, stateConfig.Description, response.Config.Description)
	assert.True(t, response.Config.GetMemory().GetGraphMemory().GetEnabled())
	assert.Equal(t, "[REDACTED]", response.Config.GetMemory().GetDsn())
	response.Config.Description = "changed"
	response.Config.Memory.GraphMemory.Enabled = false
	assert.Equal(t, "Lifecycle state", stateConfig.Description)
	assert.True(t, stateConfig.GetMemory().GetGraphMemory().GetEnabled())
}

func TestAgentLifecycle_ReloadAgentRedactsConfig(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	created, err := srv.CreateAgentFromConfig(context.Background(), &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{Name: "reload-agent"},
	})
	require.NoError(t, err)

	config := &loomv1.AgentConfig{
		Name:   "reload-agent",
		Memory: &loomv1.MemoryConfig{Dsn: "postgres://reload:secret@db/x"},
	}
	reloaded, err := srv.ReloadAgent(context.Background(), &loomv1.ReloadAgentRequest{
		AgentId: created.Id,
		Config:  config,
	})
	require.NoError(t, err)
	assert.Equal(t, "[REDACTED]", reloaded.Config.GetMemory().GetDsn())
	assert.Equal(t, "postgres://reload:secret@db/x", config.GetMemory().GetDsn())

	response, err := srv.GetAgent(context.Background(), &loomv1.GetAgentRequest{AgentId: created.Id})
	require.NoError(t, err)
	assert.Equal(t, "[REDACTED]", response.Config.GetMemory().GetDsn())
}

// --- StartAgent / StopAgent state transitions ---

func TestAgentLifecycle_StartStopTransitions(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	// Create agent
	createResp, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{Name: "lifecycle-agent"},
	})
	require.NoError(t, err)
	agentID := createResp.Id

	// Agent should start as running
	getResp, err := srv.GetAgent(ctx, &loomv1.GetAgentRequest{AgentId: agentID})
	require.NoError(t, err)
	assert.Equal(t, "running", getResp.Status)

	// Start an already running agent should be idempotent
	startResp, err := srv.StartAgent(ctx, &loomv1.StartAgentRequest{AgentId: agentID})
	require.NoError(t, err)
	assert.Equal(t, "running", startResp.Status)

	// Stop the agent
	stopResp, err := srv.StopAgent(ctx, &loomv1.StopAgentRequest{AgentId: agentID})
	require.NoError(t, err)
	assert.Equal(t, "stopped", stopResp.Status)

	// Stop an already stopped agent should be idempotent
	stopResp2, err := srv.StopAgent(ctx, &loomv1.StopAgentRequest{AgentId: agentID})
	require.NoError(t, err)
	assert.Equal(t, "stopped", stopResp2.Status)

	// Verify GetAgent reflects stopped status
	getResp2, err := srv.GetAgent(ctx, &loomv1.GetAgentRequest{AgentId: agentID})
	require.NoError(t, err)
	assert.Equal(t, "stopped", getResp2.Status)

	// Start the stopped agent
	startResp2, err := srv.StartAgent(ctx, &loomv1.StartAgentRequest{AgentId: agentID})
	require.NoError(t, err)
	assert.Equal(t, "running", startResp2.Status)
}

func TestAgentLifecycle_StartAgent_Validation(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	tests := []struct {
		name     string
		agentID  string
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name:     "empty agent_id",
			agentID:  "",
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "not found",
			agentID:  "nonexistent-agent",
			wantErr:  true,
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := srv.StartAgent(ctx, &loomv1.StartAgentRequest{AgentId: tt.agentID})
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok, "expected gRPC status error")
			assert.Equal(t, tt.wantCode, st.Code())
		})
	}
}

func TestAgentLifecycle_StopAgent_Validation(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	tests := []struct {
		name     string
		agentID  string
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name:     "empty agent_id",
			agentID:  "",
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "not found",
			agentID:  "nonexistent-agent",
			wantErr:  true,
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := srv.StopAgent(ctx, &loomv1.StopAgentRequest{AgentId: tt.agentID})
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok, "expected gRPC status error")
			assert.Equal(t, tt.wantCode, st.Code())
		})
	}
}

// --- DeleteAgent ---

func TestAgentLifecycle_DeleteAgent(t *testing.T) {
	tests := []struct {
		name      string
		force     bool
		stopFirst bool
		wantErr   bool
		wantCode  codes.Code
	}{
		{
			name:     "delete running agent without force fails",
			force:    false,
			wantErr:  true,
			wantCode: codes.FailedPrecondition,
		},
		{
			name:    "delete running agent with force succeeds",
			force:   true,
			wantErr: false,
		},
		{
			name:      "delete stopped agent without force succeeds",
			force:     false,
			stopFirst: true,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestMultiAgentServer(t)
			ctx := context.Background()

			// Create agent
			createResp, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
				Config: &loomv1.AgentConfig{Name: "delete-me"},
			})
			require.NoError(t, err)
			agentID := createResp.Id

			// Optionally stop first
			if tt.stopFirst {
				_, err := srv.StopAgent(ctx, &loomv1.StopAgentRequest{AgentId: agentID})
				require.NoError(t, err)
			}

			// Attempt deletion
			resp, err := srv.DeleteAgent(ctx, &loomv1.DeleteAgentRequest{
				AgentId: agentID,
				Force:   tt.force,
			})

			if tt.wantErr {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok, "expected gRPC status error")
				assert.Equal(t, tt.wantCode, st.Code())
				return
			}

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.True(t, resp.Success)
			assert.Equal(t, agentID, resp.AgentId)

			// Verify agent is actually removed
			agentIDs := srv.GetAgentIDs()
			assert.NotContains(t, agentIDs, agentID, "deleted agent should not be in agents map")

			// Verify GetAgent returns NotFound
			_, err = srv.GetAgent(ctx, &loomv1.GetAgentRequest{AgentId: agentID})
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, codes.NotFound, st.Code())
		})
	}
}

func TestAgentLifecycle_DeleteAgent_Validation(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	tests := []struct {
		name     string
		agentID  string
		wantCode codes.Code
	}{
		{
			name:     "empty agent_id",
			agentID:  "",
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "not found",
			agentID:  "nonexistent",
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := srv.DeleteAgent(ctx, &loomv1.DeleteAgentRequest{
				AgentId: tt.agentID,
				Force:   true,
			})
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tt.wantCode, st.Code())
		})
	}
}

// --- ReloadAgent ---

func TestAgentLifecycle_ReloadAgent(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	// Create initial agent
	createResp, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{
			Name:        "reload-me",
			Description: "Original description",
		},
	})
	require.NoError(t, err)
	agentID := createResp.Id

	tests := []struct {
		name     string
		req      *loomv1.ReloadAgentRequest
		wantErr  bool
		wantCode codes.Code
		wantName string
	}{
		{
			name:     "empty agent_id",
			req:      &loomv1.ReloadAgentRequest{AgentId: ""},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "not found",
			req:      &loomv1.ReloadAgentRequest{AgentId: "nonexistent"},
			wantErr:  true,
			wantCode: codes.NotFound,
		},
		{
			name: "no config or reload_from_file",
			req: &loomv1.ReloadAgentRequest{
				AgentId: agentID,
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "reload_from_file without registry",
			req: &loomv1.ReloadAgentRequest{
				AgentId:        agentID,
				ReloadFromFile: true,
			},
			wantErr:  true,
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "reload with new config - invalid empty name",
			req: &loomv1.ReloadAgentRequest{
				AgentId: agentID,
				Config:  &loomv1.AgentConfig{Name: ""},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "reload with valid config",
			req: &loomv1.ReloadAgentRequest{
				AgentId: agentID,
				Config: &loomv1.AgentConfig{
					Name:        "reloaded-agent",
					Description: "Updated description",
				},
			},
			wantErr:  false,
			wantName: "reloaded-agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := srv.ReloadAgent(ctx, tt.req)
			if tt.wantErr {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok, "expected gRPC status error")
				assert.Equal(t, tt.wantCode, st.Code())
				return
			}

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, tt.wantName, resp.Name)
			assert.True(t, resp.UpdatedAt > 0, "updated_at should be set after reload")
		})
	}
}

// --- AgentInfo builder ---

func TestAgentLifecycle_BuildAgentInfo(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	// Create agent with specific fields
	resp, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{
			Name:        "info-agent",
			Description: "Testing info builder",
		},
	})
	require.NoError(t, err)

	info, err := srv.GetAgent(ctx, &loomv1.GetAgentRequest{AgentId: resp.Id})
	require.NoError(t, err)

	assert.Equal(t, resp.Id, info.Id)
	assert.Equal(t, "info-agent", info.Name)
	assert.Equal(t, "running", info.Status)
	assert.NotNil(t, info.Metadata)
	assert.Equal(t, "Testing info builder", info.Metadata["description"])
	assert.True(t, info.CreatedAt > 0)
	assert.True(t, info.UpdatedAt > 0)
}

// --- Concurrent access ---

func TestAgentLifecycle_ConcurrentCreateAndGet(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()
	const numAgents = 20

	// Create multiple agents concurrently
	type result struct {
		info *loomv1.AgentInfo
		err  error
	}
	results := make(chan result, numAgents)

	for i := 0; i < numAgents; i++ {
		go func(idx int) {
			name := "concurrent-agent-" + string(rune('A'+idx))
			resp, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
				Config: &loomv1.AgentConfig{Name: name},
			})
			results <- result{info: resp, err: err}
		}(i)
	}

	// Collect results
	var created []*loomv1.AgentInfo
	for i := 0; i < numAgents; i++ {
		r := <-results
		require.NoError(t, r.err)
		created = append(created, r.info)
	}

	// Verify all agents are retrievable
	for _, info := range created {
		getResp, err := srv.GetAgent(ctx, &loomv1.GetAgentRequest{AgentId: info.Id})
		require.NoError(t, err)
		assert.Equal(t, info.Name, getResp.Name)
	}

	// Total agents should be initial (1 default) + numAgents
	agentIDs := srv.GetAgentIDs()
	assert.Len(t, agentIDs, 1+numAgents)
}

func TestAgentLifecycle_ConcurrentStartStop(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	// Create an agent
	createResp, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{Name: "race-agent"},
	})
	require.NoError(t, err)
	agentID := createResp.Id

	// Concurrently start and stop
	done := make(chan struct{}, 40)
	for i := 0; i < 20; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = srv.StartAgent(ctx, &loomv1.StartAgentRequest{AgentId: agentID})
		}()
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = srv.StopAgent(ctx, &loomv1.StopAgentRequest{AgentId: agentID})
		}()
	}

	for i := 0; i < 40; i++ {
		<-done
	}

	// Agent should still be in a valid state
	resp, err := srv.GetAgent(ctx, &loomv1.GetAgentRequest{AgentId: agentID})
	require.NoError(t, err)
	assert.Contains(t, []string{"running", "stopped"}, resp.Status)
}

// --- Agent registered externally (without CreateAgentFromConfig) ---

func TestAgentLifecycle_StartStopExternalAgent(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	// The default agent was added via constructor, not CreateAgentFromConfig.
	// It should still be startable/stoppable via the lifecycle RPCs.
	agentIDs := srv.GetAgentIDs()
	require.Len(t, agentIDs, 1)
	defaultID := agentIDs[0]

	// Start (should work even without tracked state)
	startResp, err := srv.StartAgent(ctx, &loomv1.StartAgentRequest{AgentId: defaultID})
	require.NoError(t, err)
	assert.Equal(t, "running", startResp.Status)

	// Stop
	stopResp, err := srv.StopAgent(ctx, &loomv1.StopAgentRequest{AgentId: defaultID})
	require.NoError(t, err)
	assert.Equal(t, "stopped", stopResp.Status)

	// Start again
	startResp2, err := srv.StartAgent(ctx, &loomv1.StartAgentRequest{AgentId: defaultID})
	require.NoError(t, err)
	assert.Equal(t, "running", startResp2.Status)
}

// --- Bug regression tests ---

// TestAgentLifecycle_B1_InlineConfigNilNestedFields verifies that CreateAgentFromConfig
// does not crash when the inline AgentConfig has nil nested fields (Llm, Tools, Memory,
// Behavior). Previously this caused a nil pointer dereference when the registry's
// buildAgent tried to access config.Llm.Provider on a nil Llm field.
func TestAgentLifecycle_B1_InlineConfigNilNestedFields(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	tests := []struct {
		name   string
		config *loomv1.AgentConfig
	}{
		{
			name: "only name set",
			config: &loomv1.AgentConfig{
				Name: "bare-minimum-agent",
			},
		},
		{
			name: "name and description only",
			config: &loomv1.AgentConfig{
				Name:        "described-agent",
				Description: "I have no nested fields",
			},
		},
		{
			name: "name, description, and system_prompt only",
			config: &loomv1.AgentConfig{
				Name:         "prompted-agent",
				Description:  "I have a prompt",
				SystemPrompt: "Be helpful.",
			},
		},
		{
			name: "partial nested fields - only Llm set",
			config: &loomv1.AgentConfig{
				Name: "partial-llm-agent",
				Llm:  &loomv1.LLMConfig{}, // empty but non-nil
			},
		},
		{
			name: "partial nested fields - only Behavior set",
			config: &loomv1.AgentConfig{
				Name:     "partial-behavior-agent",
				Behavior: &loomv1.BehaviorConfig{MaxIterations: 5},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
				Config: tt.config,
			})
			require.NoError(t, err, "CreateAgentFromConfig must not crash with nil nested fields")
			require.NotNil(t, resp)
			assert.NotEmpty(t, resp.Id)
			assert.Equal(t, tt.config.Name, resp.Name)
			assert.Equal(t, "running", resp.Status)
		})
	}
}

// TestAgentLifecycle_B1_EnsureConfigDefaults verifies that ensureConfigDefaults
// fills in nil nested fields with sensible non-nil defaults.
func TestAgentLifecycle_B1_EnsureConfigDefaults(t *testing.T) {
	config := &loomv1.AgentConfig{Name: "test"}
	assert.Nil(t, config.Llm)
	assert.Nil(t, config.Tools)
	assert.Nil(t, config.Memory)
	assert.Nil(t, config.Behavior)

	ensureConfigDefaults(config)

	assert.NotNil(t, config.Llm, "Llm should be non-nil after ensureConfigDefaults")
	assert.NotNil(t, config.Tools, "Tools should be non-nil after ensureConfigDefaults")
	assert.NotNil(t, config.Memory, "Memory should be non-nil after ensureConfigDefaults")
	assert.Equal(t, "memory", config.Memory.Type, "Memory.Type should default to 'memory'")
	assert.Equal(t, int32(50), config.Memory.MaxHistory, "Memory.MaxHistory should default to 50")
	assert.NotNil(t, config.Behavior, "Behavior should be non-nil after ensureConfigDefaults")
	assert.Equal(t, int32(10), config.Behavior.MaxIterations, "Behavior.MaxIterations should default to 10")
	assert.Equal(t, int32(300), config.Behavior.TimeoutSeconds, "Behavior.TimeoutSeconds should default to 300")
	assert.Equal(t, int32(25), config.Behavior.MaxTurns, "Behavior.MaxTurns should default to 25")
	assert.Equal(t, int32(50), config.Behavior.MaxToolExecutions, "Behavior.MaxToolExecutions should default to 50")
}

// TestAgentLifecycle_B1_EnsureConfigDefaults_NoOverwrite verifies that ensureConfigDefaults
// does not overwrite already-set nested fields.
func TestAgentLifecycle_B1_EnsureConfigDefaults_NoOverwrite(t *testing.T) {
	config := &loomv1.AgentConfig{
		Name: "test",
		Llm: &loomv1.LLMConfig{
			Provider: "anthropic",
			Model:    "claude-3-5-sonnet-20241022",
		},
		Memory: &loomv1.MemoryConfig{
			Type:       "sqlite",
			MaxHistory: 100,
		},
		Behavior: &loomv1.BehaviorConfig{
			MaxIterations: 20,
		},
		Tools: &loomv1.ToolsConfig{
			Builtin: []string{"execute_sql"},
		},
	}

	ensureConfigDefaults(config)

	// Existing values must not be overwritten
	assert.Equal(t, "anthropic", config.Llm.Provider)
	assert.Equal(t, "claude-3-5-sonnet-20241022", config.Llm.Model)
	assert.Equal(t, "sqlite", config.Memory.Type)
	assert.Equal(t, int32(100), config.Memory.MaxHistory)
	assert.Equal(t, int32(20), config.Behavior.MaxIterations)
	assert.Equal(t, []string{"execute_sql"}, config.Tools.Builtin)
}

// TestAgentLifecycle_B4_ReloadStoppedAgentWithInlineConfig verifies that ReloadAgent
// with an inline config succeeds even when the agent has been stopped. Previously,
// the inline config reload path called registry.CreateAgent which returned
// "agent is already running" regardless of actual state.
func TestAgentLifecycle_B4_ReloadStoppedAgentWithInlineConfig(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	// Create agent
	createResp, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{
			Name:        "b4-reload-target",
			Description: "Original config",
		},
	})
	require.NoError(t, err)
	agentID := createResp.Id

	// Stop the agent
	_, err = srv.StopAgent(ctx, &loomv1.StopAgentRequest{AgentId: agentID})
	require.NoError(t, err)

	// Verify stopped
	getResp, err := srv.GetAgent(ctx, &loomv1.GetAgentRequest{AgentId: agentID})
	require.NoError(t, err)
	assert.Equal(t, "stopped", getResp.Status)

	// Reload with inline config while stopped - this was the B4 bug
	reloadResp, err := srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{
		AgentId: agentID,
		Config: &loomv1.AgentConfig{
			Name:        "b4-reload-target-v2",
			Description: "Updated config",
		},
	})
	require.NoError(t, err, "ReloadAgent with inline config must succeed for stopped agents")
	require.NotNil(t, reloadResp)
	assert.Equal(t, "b4-reload-target-v2", reloadResp.Name)
}

// TestAgentLifecycle_B4_ReloadRunningAgentWithInlineConfig verifies that ReloadAgent
// with an inline config also succeeds for running agents.
func TestAgentLifecycle_B4_ReloadRunningAgentWithInlineConfig(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	// Create agent (starts as running)
	createResp, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{
			Name:        "b4-running-reload",
			Description: "Original",
		},
	})
	require.NoError(t, err)
	agentID := createResp.Id

	// Reload with inline config while running
	reloadResp, err := srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{
		AgentId: agentID,
		Config: &loomv1.AgentConfig{
			Name:        "b4-running-reload-v2",
			Description: "Updated while running",
		},
	})
	require.NoError(t, err, "ReloadAgent with inline config must succeed for running agents")
	require.NotNil(t, reloadResp)
	assert.Equal(t, "b4-running-reload-v2", reloadResp.Name)
}

// TestAgentLifecycle_B4_ReloadWithNilNestedConfig verifies that ReloadAgent
// does not crash when the inline config has nil nested fields.
func TestAgentLifecycle_B4_ReloadWithNilNestedConfig(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	// Create agent
	createResp, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: &loomv1.AgentConfig{
			Name: "b4-nil-nested-reload",
		},
	})
	require.NoError(t, err)

	// Reload with config that only has Name (all nested fields nil)
	reloadResp, err := srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{
		AgentId: createResp.Id,
		Config: &loomv1.AgentConfig{
			Name:         "b4-nil-nested-reload-v2",
			SystemPrompt: "New prompt",
		},
	})
	require.NoError(t, err, "ReloadAgent must not crash with nil nested fields in inline config")
	require.NotNil(t, reloadResp)
	assert.Equal(t, "b4-nil-nested-reload-v2", reloadResp.Name)
}
