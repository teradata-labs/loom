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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/eventlog"
)

// TestRegistry_BuiltAgentsRecordTheEventLog covers the production path from
// review round 1, F1: `looms serve` builds agents through the registry, so an
// EventLogStore given to the registry must reach those agents and be recorded
// during a normal turn, before any crash.
func TestRegistry_BuiltAgentsRecordTheEventLog(t *testing.T) {
	recorder := &recordingEventLog{}
	tmpDir := t.TempDir()
	registry, err := NewRegistry(RegistryConfig{
		ConfigDir:     tmpDir,
		DBPath:        filepath.Join(tmpDir, "registry.db"),
		LLMProvider:   &mockSimpleLLM{},
		Logger:        zap.NewNop(),
		EventLogStore: recorder,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })

	agentsDir := filepath.Join(tmpDir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	require.NoError(t, SaveAgentConfig(createTestAgentConfig("logged"), filepath.Join(agentsDir, "logged.yaml")))
	ctx := context.Background()
	require.NoError(t, registry.LoadAgents(ctx))
	ag, err := registry.CreateAgent(ctx, "logged")
	require.NoError(t, err)

	_, err = ag.Chat(ctx, "s-registry", "hello")
	require.NoError(t, err)

	var sawLLMIntent bool
	for _, e := range recorder.AllRecordedEvents("s-registry") {
		if e.EventType == eventlog.EventLLMCallIntent {
			sawLLMIntent = true
		}
	}
	require.True(t, sawLLMIntent, "a registry-built agent recorded no execution events during its turn")
}

// Without an EventLogStore the registry builds agents that record nothing,
// which is what keeps the log opt-in.
func TestRegistry_NoEventLogStore_AgentsDoNotRecord(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	agentsDir := filepath.Join(tmpDir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	require.NoError(t, SaveAgentConfig(createTestAgentConfig("unlogged"), filepath.Join(agentsDir, "unlogged.yaml")))
	ctx := context.Background()
	require.NoError(t, registry.LoadAgents(ctx))
	ag, err := registry.CreateAgent(ctx, "unlogged")
	require.NoError(t, err)
	require.Nil(t, ag.eventLog)
}
