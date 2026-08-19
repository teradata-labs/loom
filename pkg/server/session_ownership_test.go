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
// Cross-user session isolation tests for the gRPC layer (MCP 2026-07-28
// Immediate brief Part A). These are the first tests covering the ownership
// checks; wrong-owner must be indistinguishable from not-found everywhere.
package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newOwnershipServer builds a server with one agent holding one session
// owned by "user-a".
func newOwnershipServer(t *testing.T) (*MultiAgentServer, *agent.Agent, string) {
	t.Helper()
	ag := createTestAgent()
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent-1": ag}, nil)

	sessionID := GenerateSessionID()
	session := ag.CreateSession(context.Background(), sessionID, "")
	session.UserID = "user-a"
	return srv, ag, sessionID
}

func ctxFor(user string) context.Context {
	if user == "" {
		return context.Background()
	}
	return postgres.ContextWithUserID(context.Background(), user)
}

func TestGetSessionCrossUserIsNotFound(t *testing.T) {
	srv, _, sessionID := newOwnershipServer(t)

	// Owner sees the session.
	_, err := srv.GetSession(ctxFor("user-a"), &loomv1.GetSessionRequest{SessionId: sessionID})
	require.NoError(t, err)

	// Another user gets exactly what a nonexistent session produces.
	_, errCross := srv.GetSession(ctxFor("user-b"), &loomv1.GetSessionRequest{SessionId: sessionID})
	_, errMissing := srv.GetSession(ctxFor("user-b"), &loomv1.GetSessionRequest{SessionId: "no-such-session"})
	require.Error(t, errCross)
	require.Error(t, errMissing)
	assert.Equal(t, status.Code(errMissing), status.Code(errCross))
	assert.Equal(t, errMissing.Error(), errCross.Error(), "wrong-owner must be indistinguishable from not-found")
}

func TestListSessionsFiltersByOwner(t *testing.T) {
	srv, ag, _ := newOwnershipServer(t)

	otherID := GenerateSessionID()
	other := ag.CreateSession(context.Background(), otherID, "")
	other.UserID = "user-b"

	resp, err := srv.ListSessions(ctxFor("user-a"), &loomv1.ListSessionsRequest{})
	require.NoError(t, err)
	for _, s := range resp.Sessions {
		assert.NotEqual(t, otherID, s.Id, "user-a must not see user-b's session")
	}
}

func TestGetConversationHistoryCrossUserIsNotFound(t *testing.T) {
	srv, _, sessionID := newOwnershipServer(t)

	_, err := srv.GetConversationHistory(ctxFor("user-b"), &loomv1.GetConversationHistoryRequest{SessionId: sessionID})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestDeleteSessionCrossUserIsNotFound(t *testing.T) {
	srv, ag, sessionID := newOwnershipServer(t)

	_, err := srv.DeleteSession(ctxFor("user-b"), &loomv1.DeleteSessionRequest{SessionId: sessionID})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))

	// The owner's session survived.
	_, stillThere := ag.GetSession(sessionID)
	assert.True(t, stillThere)
}

func TestFindAgentBySessionEnforcesOwnership(t *testing.T) {
	srv, _, sessionID := newOwnershipServer(t)

	_, _, ok := srv.findAgentBySession(sessionID, "user-a")
	assert.True(t, ok, "owner resolves the session")

	_, _, ok = srv.findAgentBySession(sessionID, "user-b")
	assert.False(t, ok, "foreign session must not resolve (gates Weave/StreamWeave routing)")

	_, _, ok = srv.findAgentBySession(sessionID, "")
	assert.True(t, ok, "identity-less caller (single-tenant) resolves everything")
}

func TestUnstampedSessionRemainsAccessible(t *testing.T) {
	// Compatibility: sessions created before ownership stamping (UserID == "")
	// stay reachable so upgrades do not strand live sessions. Deliberate and
	// documented on sessionAccessibleBy.
	srv, ag, _ := newOwnershipServer(t)
	unstampedID := GenerateSessionID()
	ag.CreateSession(context.Background(), unstampedID, "")

	_, err := srv.GetSession(ctxFor("user-b"), &loomv1.GetSessionRequest{SessionId: unstampedID})
	assert.NoError(t, err)
}

// TestEnforcedOwnershipModeDeniesWildcards (review finding 3, PR #328):
// with enforcement on — any authenticated deployment — a blank caller
// identity sees nothing, and an unowned legacy session is not
// world-readable. The permissive single-tenant behavior survives only as
// the explicit non-enforced mode.
func TestEnforcedOwnershipModeDeniesWildcards(t *testing.T) {
	srv, ag, sessionID := newOwnershipServer(t)

	unownedID := GenerateSessionID()
	ag.CreateSession(context.Background(), unownedID, "") // UserID == ""

	// Permissive mode (default): historical wildcards hold.
	assert.True(t, srv.sessionAccessibleBy("", mustGetSession(t, ag, sessionID)))
	assert.True(t, srv.sessionAccessibleBy("user-b", mustGetSession(t, ag, unownedID)))

	srv.SetEnforceSessionOwnership(true)

	// Anonymous callers see nothing.
	assert.False(t, srv.sessionAccessibleBy("", mustGetSession(t, ag, sessionID)))
	// Unowned sessions are not wildcards.
	assert.False(t, srv.sessionAccessibleBy("user-b", mustGetSession(t, ag, unownedID)))
	assert.False(t, srv.sessionAccessibleBy("user-b", mustGetSession(t, ag, sessionID)))
	// Exact owner still matches.
	assert.True(t, srv.sessionAccessibleBy("user-a", mustGetSession(t, ag, sessionID)))
}

func mustGetSession(t *testing.T, ag *agent.Agent, id string) *agent.Session {
	t.Helper()
	session, ok := ag.GetSession(id)
	require.True(t, ok)
	return session
}

// TestWeaveForeignSessionIsNotFound (review finding 4, PR #328): a foreign
// session_id on Weave must surface as not-found — never fall through to the
// explicit or default agent, which would resume or shadow the session.
func TestWeaveForeignSessionIsNotFound(t *testing.T) {
	srv, _, sessionID := newOwnershipServer(t)

	_, err := srv.Weave(ctxFor("user-b"), &loomv1.WeaveRequest{
		Query:     "hello",
		SessionId: sessionID,
	})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err), "foreign session must be indistinguishable from missing")

	// With an explicit agent_id the outcome must be identical: the bypass
	// the review flagged was exactly agent_id skipping the ownership lookup.
	_, err = srv.Weave(ctxFor("user-b"), &loomv1.WeaveRequest{
		Query:     "hello",
		SessionId: sessionID,
		AgentId:   "agent-1",
	})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// TestSessionsStampedAtCreation (review finding 3, PR #328): every session
// creation path stamps ownership from the authenticated context — not just
// the CreateSession RPC.
func TestSessionsStampedAtCreation(t *testing.T) {
	_, ag, _ := newOwnershipServer(t)

	id := GenerateSessionID()
	session := ag.CreateSession(ctxFor("user-c"), id, "")
	assert.Equal(t, "user-c", session.UserID,
		"sessions created through the agent path must carry the context identity")
}
