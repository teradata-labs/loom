// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
)

const workflowRoutingName = "routing-workflow"

func workflowRoutingConfig(name, workflow, role string) *loomv1.AgentConfig {
	return &loomv1.AgentConfig{
		Name:     name,
		Metadata: map[string]string{"workflow": workflow, "role": role},
	}
}

func workflowRoutingTestServer(t *testing.T, agents map[string]*agent.Agent, configs ...*loomv1.AgentConfig) *MultiAgentServer {
	t.Helper()

	// Exercise registry admission without consulting a user's workflow YAML.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    ":memory:",
		Logger:    newTestLogger(t),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registry.Close()) })

	registry.RegisterConfig(workflowRoutingConfig(workflowRoutingName, workflowRoutingName, "coordinator"))
	registry.RegisterConfig(workflowRoutingConfig(workflowRoutingName+":worker", workflowRoutingName, "executor"))
	for _, cfg := range configs {
		registry.RegisterConfig(cfg)
	}

	return setupBroadcastTestServer(t, agents, registry)
}

func TestWorkflowWorkerResolution(t *testing.T) {
	workerID := workflowRoutingName + ":worker"
	tests := []struct {
		name      string
		liveNames []string
		configs   []*loomv1.AgentConfig
		wantName  string
		wantCode  codes.Code
	}{
		{
			name:      "workflow_prefixed_name_wins",
			liveNames: []string{workerID, "worker", "domain-worker-agent", "domain-worker"},
			configs: []*loomv1.AgentConfig{
				workflowRoutingConfig("domain-worker-agent", workflowRoutingName, "executor"),
				workflowRoutingConfig("domain-worker", workflowRoutingName, "executor"),
			},
			wantName: workerID,
		},
		{
			name:      "base_name",
			liveNames: []string{"worker", "domain-worker-agent", "domain-worker"},
			configs: []*loomv1.AgentConfig{
				workflowRoutingConfig("domain-worker-agent", workflowRoutingName, "executor"),
				workflowRoutingConfig("domain-worker", workflowRoutingName, "executor"),
			},
			wantName: "worker",
		},
		{
			name:      "workflow_metadata_suffix",
			liveNames: []string{"domain-worker"},
			configs: []*loomv1.AgentConfig{
				workflowRoutingConfig("domain-worker", workflowRoutingName, "executor"),
			},
			wantName: "domain-worker",
		},
		{
			name:      "workflow_metadata_substring",
			liveNames: []string{"domain-worker-agent", "other-worker", "coordinator-worker", "homeworker"},
			configs: []*loomv1.AgentConfig{
				workflowRoutingConfig("domain-worker-agent", workflowRoutingName, "executor"),
				workflowRoutingConfig("other-worker", "another-workflow", "executor"),
				workflowRoutingConfig("coordinator-worker", workflowRoutingName, "coordinator"),
				workflowRoutingConfig("homeworker", workflowRoutingName, "executor"),
			},
			wantName: "domain-worker-agent",
		},
		{
			name:      "unloaded_candidates_do_not_hide_live_match",
			liveNames: []string{"domain-worker-agent"},
			configs: []*loomv1.AgentConfig{
				workflowRoutingConfig("unloaded-worker", workflowRoutingName, "executor"),
				workflowRoutingConfig("domain-worker-agent", workflowRoutingName, "executor"),
			},
			wantName: "domain-worker-agent",
		},
		{
			name:     "no_match",
			wantCode: codes.NotFound,
		},
		{
			name:      "different_workflow",
			liveNames: []string{"domain-worker"},
			configs: []*loomv1.AgentConfig{
				workflowRoutingConfig("domain-worker", workflowRoutingName+"-other", "executor"),
			},
			wantCode: codes.NotFound,
		},
		{
			name:      "coordinator_is_not_a_metadata_candidate",
			liveNames: []string{"domain-worker"},
			configs: []*loomv1.AgentConfig{
				workflowRoutingConfig("domain-worker", workflowRoutingName, "coordinator"),
			},
			wantCode: codes.NotFound,
		},
		{
			name:      "missing_workflow_metadata",
			liveNames: []string{"domain-worker"},
			configs: []*loomv1.AgentConfig{
				workflowRoutingConfig("domain-worker", "", "executor"),
			},
			wantCode: codes.NotFound,
		},
		{
			name:      "partial_role_names_do_not_match",
			liveNames: []string{"homeworker", "workerish"},
			configs: []*loomv1.AgentConfig{
				workflowRoutingConfig("homeworker", workflowRoutingName, "executor"),
				workflowRoutingConfig("workerish", workflowRoutingName, "executor"),
			},
			wantCode: codes.NotFound,
		},
		{
			name:      "different_workflow_prefix_does_not_match",
			liveNames: []string{"another-workflow:domain-worker"},
			configs: []*loomv1.AgentConfig{
				workflowRoutingConfig("another-workflow:domain-worker", workflowRoutingName, "executor"),
			},
			wantCode: codes.NotFound,
		},
		{
			name:      "different_workflow_role_does_not_match",
			liveNames: []string{workflowRoutingName + ":worker-helper"},
			configs: []*loomv1.AgentConfig{
				workflowRoutingConfig(workflowRoutingName+":worker-helper", workflowRoutingName, "executor"),
			},
			wantCode: codes.NotFound,
		},
		{
			name:      "ambiguous_metadata_is_not_routed",
			liveNames: []string{"alpha-worker", "beta-worker"},
			configs: []*loomv1.AgentConfig{
				workflowRoutingConfig("alpha-worker", workflowRoutingName, "executor"),
				workflowRoutingConfig("beta-worker", workflowRoutingName, "executor"),
			},
			wantCode: codes.FailedPrecondition,
		},
	}

	for _, mode := range []string{"registry_listed", "auto_spawned"} {
		t.Run(mode, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					agents := map[string]*agent.Agent{
						workflowRoutingName: agent.NewAgent(&mockBackend{}, &mockLLMForBroadcastTest{}, agent.WithName(workflowRoutingName)),
					}
					for _, name := range tt.liveNames {
						agents[name] = agent.NewAgent(&mockBackend{}, &mockLLMForBroadcastTest{}, agent.WithName(name))
					}
					srv := workflowRoutingTestServer(t, agents, tt.configs...)

					var key string
					if mode == "registry_listed" {
						sessionID := GenerateSessionID()
						err := srv.spawnWorkflowSubAgents(context.Background(), agents[workflowRoutingName], workflowRoutingName, sessionID)
						require.NoError(t, err)
						key = sessionID + ":" + workerID
					} else {
						err := srv.autoSpawnWorkflowSubAgent(context.Background(), workerID)
						if tt.wantName == "" {
							require.Error(t, err)
							assert.Equal(t, tt.wantCode, status.Code(err))
							assert.Contains(t, err.Error(), workerID)
							if tt.wantCode == codes.FailedPrecondition {
								assert.Contains(t, err.Error(), "[alpha-worker beta-worker]")
							}
						} else {
							require.NoError(t, err)
						}
						key = "auto:" + workerID
					}

					srv.workflowSubAgentsMu.RLock()
					worker := srv.workflowSubAgents[key]
					srv.workflowSubAgentsMu.RUnlock()
					if tt.wantName == "" {
						assert.Nil(t, worker, "an unresolved or ambiguous address must not start a worker")
					} else {
						require.NotNil(t, worker, "the registry-listed address must resolve before spawning")
						assert.Same(t, agents[tt.wantName], worker.agent)
					}
				})
			}
		})
	}
}

func TestAutoSpawnWorkflowWorkerResolution_ExplicitBaseName(t *testing.T) {
	workerID := workflowRoutingName + ":reviewer"
	for _, exact := range []bool{false, true} {
		t.Run(fmt.Sprintf("exact_workflow_agent_%t", exact), func(t *testing.T) {
			configured := agent.NewAgent(&mockBackend{}, &mockLLMForBroadcastTest{}, agent.WithName("domain-specialist"))
			agents := map[string]*agent.Agent{
				"domain-specialist": configured,
				"reviewer":          agent.NewAgent(&mockBackend{}, &mockLLMForBroadcastTest{}, agent.WithName("reviewer")),
			}
			want := configured
			if exact {
				want = agent.NewAgent(&mockBackend{}, &mockLLMForBroadcastTest{}, agent.WithName(workerID))
				agents[workerID] = want
			}
			srv := workflowRoutingTestServer(t, agents)

			require.NoError(t, srv.autoSpawnWorkflowSubAgent(context.Background(), workerID, "domain-specialist"))
			srv.workflowSubAgentsMu.RLock()
			worker := srv.workflowSubAgents["auto:"+workerID]
			srv.workflowSubAgentsMu.RUnlock()
			require.NotNil(t, worker)
			assert.Same(t, want, worker.agent, "the explicit base name may differ from the workflow role")
		})
	}
}

func TestAutoSpawnWorkflowWorkerResolution_EmptyWorkflowOrRole(t *testing.T) {
	for _, id := range []string{":worker", workflowRoutingName + ":", ":"} {
		t.Run(id, func(t *testing.T) {
			fallback := agent.NewAgent(&mockBackend{}, &mockLLMForBroadcastTest{}, agent.WithName("worker"))
			srv := workflowRoutingTestServer(t, map[string]*agent.Agent{"worker": fallback})

			require.Error(t, srv.autoSpawnWorkflowSubAgent(context.Background(), id))
			srv.workflowSubAgentsMu.RLock()
			count := len(srv.workflowSubAgents)
			srv.workflowSubAgentsMu.RUnlock()
			assert.Zero(t, count, "an empty workflow or role must not fall through to the default agent")
		})
	}
}

func TestWorkflowWorkerResolution_RunningInstanceRemainsPinned(t *testing.T) {
	workerID := workflowRoutingName + ":worker"
	for _, mode := range []string{"registry_listed", "auto_spawned"} {
		t.Run(mode, func(t *testing.T) {
			originalLLM := &mockLLMForBroadcastTest{}
			replacementLLM := &mockLLMForBroadcastTest{}
			original := agent.NewAgent(&mockBackend{}, originalLLM, agent.WithName("worker"))
			coordinator := agent.NewAgent(&mockBackend{}, &mockLLMForBroadcastTest{}, agent.WithName(workflowRoutingName))
			srv := workflowRoutingTestServer(t, map[string]*agent.Agent{
				workflowRoutingName: coordinator,
				"worker":            original,
			})

			if mode == "registry_listed" {
				require.NoError(t, srv.spawnWorkflowSubAgents(context.Background(), coordinator, workflowRoutingName, GenerateSessionID()))
			} else {
				require.NoError(t, srv.autoSpawnWorkflowSubAgent(context.Background(), workerID))
			}

			send := func(message string, wantCount int) {
				t.Helper()
				require.NoError(t, srv.messageQueue.Enqueue(context.Background(), &communication.QueueMessage{
					ID:        message,
					FromAgent: workflowRoutingName,
					ToAgent:   workerID,
					Payload: &loomv1.MessagePayload{
						Data: &loomv1.MessagePayload_Value{Value: []byte(message)},
					},
				}))
				require.Eventually(t, func() bool {
					return len(originalLLM.GetInjectedMessages()) == wantCount
				}, 5*time.Second, 10*time.Millisecond)
			}

			send("before-registry-change", 1)

			// A later exact-name registration must not take over an active worker.
			replacement := agent.NewAgent(&mockBackend{}, replacementLLM, agent.WithName(workerID))
			srv.AddAgent(replacement.GetID(), replacement)
			resolved, _, err := srv.getAgent(workerID)
			require.NoError(t, err)
			require.Same(t, replacement, resolved)

			send("after-registry-change", 2)
			assert.Empty(t, replacementLLM.GetInjectedMessages())
		})
	}
}
