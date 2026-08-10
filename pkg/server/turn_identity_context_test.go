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
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/storage/postgres"
)

// turnIdentityContext is the seam every background-driven turn's context is
// rooted in: the tenant identity MUST survive (the postgres HITL store refuses
// any operation without it — a held call would be denied in microseconds with
// no card) and the caller's cancellation and deadline must NOT (the worker
// outlives the request).
func TestTurnIdentityContext(t *testing.T) {
	t.Run("carries the tenant identity", func(t *testing.T) {
		ctx := postgres.ContextWithUserID(context.Background(), "tenant-42")
		got := turnIdentityContext(ctx)
		require.Equal(t, "tenant-42", postgres.UserIDFromContext(got),
			"rewriting the body to context.Background() must fail here")
	})

	t.Run("drops the caller's cancellation and deadline", func(t *testing.T) {
		parent, cancel := context.WithCancel(postgres.ContextWithUserID(context.Background(), "tenant-42"))
		cancel()
		got := turnIdentityContext(parent)
		require.NoError(t, got.Err(), "the worker context must outlive the request that spawned it")
		_, hasDeadline := got.Deadline()
		require.False(t, hasDeadline)
		require.Equal(t, "tenant-42", postgres.UserIDFromContext(got))
	})

	t.Run("empty identity stays empty", func(t *testing.T) {
		got := turnIdentityContext(context.Background())
		require.Empty(t, postgres.UserIDFromContext(got),
			"a SQLite deployment's identity-less context behaves like context.Background()")
	})
}

// The call-site half: a spawn's background context genuinely carries the
// spawning request's tenant identity — rooting the worker contexts in a bare
// context.Background() again must fail here, not only in the seam's own
// table test.
func TestSpawnSubAgent_BackgroundContextCarriesTenantIdentity(t *testing.T) {
	llm := &replyingLLM{}
	srv := spawnTestServer(t, llm)
	parentID := parentSession(t, srv)

	ctx := postgres.ContextWithUserID(context.Background(), "tenant-42")
	resp, err := srv.SpawnSubAgent(ctx, &builtin.SpawnSubAgentRequest{
		ParentSessionID: parentID,
		ParentAgentID:   "parent",
		AgentID:         "helper",
	})
	require.NoError(t, err)

	srv.spawnedAgentsMu.RLock()
	spawned := srv.spawnedAgents[resp.SessionID]
	srv.spawnedAgentsMu.RUnlock()
	require.NotNil(t, spawned)
	require.NotNil(t, spawned.runCtx)
	require.Equal(t, "tenant-42", postgres.UserIDFromContext(spawned.runCtx),
		"the spawn's background turns reach tenant-scoped stores — losing the id denies every held call in microseconds with no card")
	require.NoError(t, spawned.runCtx.Err(),
		"the background context must not inherit the request's cancellation")
}

// The reload half: MultiAgentServer.UpdateAgent hands the outgoing agent's
// approved set to its replacement — reverting the carry at the swap site (not
// just the Agent-level adoption primitive) must fail here.
func TestUpdateAgent_CarriesApprovedSetAcrossSwap(t *testing.T) {
	old := agent.NewAgent(nil, nil, agent.WithName("outgoing"))
	old.AdoptApprovedSet(shuttle.NewApprovedSet())
	sctx := session.WithSessionID(context.Background(), "s1")
	require.NoError(t, old.ApprovedSet().Record(sctx, "approved_stmts", []shuttle.CallIdentity{"stmt-a"}))

	srv := NewMultiAgentServer(map[string]*agent.Agent{"gov": old}, nil)

	replacement := agent.NewAgent(nil, nil, agent.WithName("replacement"))
	replacement.SetID(old.GetID())
	require.NoError(t, srv.UpdateAgent("gov", replacement))

	ok, err := replacement.ApprovedSet().Contains(sctx, "approved_stmts", "stmt-a")
	require.NoError(t, err)
	require.True(t, ok,
		"a hot swap must hand the outgoing agent's approvals to its replacement — a human-visible approval cannot be falsified by an agent reload")
}
