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

//go:build fts5

package main

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/server"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// startRegistryServer serves a registry-backed MultiAgentServer, wired the way
// `looms serve` wires it, so CreateAgentFromConfig goes through the registry.
func startRegistryServer(t *testing.T) loomv1.LoomServiceClient {
	t.Helper()
	dir := t.TempDir()
	reg, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   dir,
		DBPath:      filepath.Join(dir, "registry.db"),
		LLMProvider: &outageProvider{},
		Logger:      zap.NewNop(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })

	srv := server.NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	srv.SetAgentRegistry(reg)

	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	loomv1.RegisterLoomServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return loomv1.NewLoomServiceClient(conn)
}

// A runner killed before its deferred cleanup (SIGKILL, OOM, node loss) leaves
// lme-tmp-<qid> registered on the server. The retry of that entry must replace
// the stale agent rather than fail with "already running" — that failure is
// Internal, so it would be charged to the chunk budget and quarantine a
// healthy chunk, and the stale agent would carry the earlier attempt's memory.
func TestCreateTempAgentReplacesStaleAgent(t *testing.T) {
	r := &Runner{
		config:    RunConfig{Mode: ModeContextStuffing, Concurrency: 1, Isolate: true},
		logger:    zap.NewNop(),
		client:    startRegistryServer(t),
		baseAgent: &loomv1.AgentConfig{Name: "lme-base"},
	}
	ctx := context.Background()

	staleID, err := r.createTempAgent(ctx, "q1")
	require.NoError(t, err, "first attempt")

	retryID, err := r.createTempAgent(ctx, "q1")
	require.NoError(t, err, "retry after the first attempt's cleanup never ran")
	assert.NotEmpty(t, retryID)

	list, err := r.client.ListAgents(ctx, &loomv1.ListAgentsRequest{})
	require.NoError(t, err)
	var named int
	for _, a := range list.GetAgents() {
		if a.GetName() == "lme-tmp-q1" {
			named++
			assert.NotEqual(t, staleID, a.GetId(), "the stale agent must be gone")
		}
	}
	assert.Equal(t, 1, named, "exactly one lme-tmp-q1 after the retry")
}
