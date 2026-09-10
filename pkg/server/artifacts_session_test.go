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
	"path/filepath"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/backend"
	"github.com/teradata-labs/loom/pkg/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// setupArtifactServer builds a server over a real SQLite artifact store seeded
// with artifacts across two sessions plus one session-less artifact — the three
// populations session scoping has to keep apart.
func setupArtifactServer(t *testing.T) *MultiAgentServer {
	t.Helper()

	// The artifacts schema is created by the storage backend's migrations, not
	// by NewSQLiteStore itself, so build the store the way production does:
	// through the backend.
	sb, err := backend.NewSQLiteBackend(&loomv1.SQLiteStorageConfig{
		Path: filepath.Join(t.TempDir(), "loom.db"),
	}, observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	store := sb.ArtifactStore()

	ctx := context.Background()

	// The artifacts table has a foreign key onto sessions, so the sessions must
	// exist first — the same invariant production maintains, since an artifact
	// is always written from inside a session.
	for _, id := range []string{"sess-1", "sess-2"} {
		if err := sb.SessionStorage().SaveSession(ctx, &agent.Session{ID: id}); err != nil {
			t.Fatalf("create session %s: %v", id, err)
		}
	}
	seed := []*artifacts.Artifact{
		{ID: "a1", Name: "report.md", Path: "/tmp/a1", Source: artifacts.SourceAgent, SessionID: "sess-1"},
		{ID: "a2", Name: "data.csv", Path: "/tmp/a2", Source: artifacts.SourceAgent, SessionID: "sess-1"},
		{ID: "b1", Name: "report.md", Path: "/tmp/b1", Source: artifacts.SourceAgent, SessionID: "sess-2"},
		{ID: "u1", Name: "upload.txt", Path: "/tmp/u1", Source: artifacts.SourceUser},
	}
	for _, a := range seed {
		if err := store.Index(ctx, a); err != nil {
			t.Fatalf("seed %s: %v", a.ID, err)
		}
	}

	srv := NewMultiAgentServer(nil, nil)
	srv.SetArtifactStore(store)
	return srv
}

// The point of the field: a session filter returns that session's artifacts and
// nothing else. This is what lets a remote surface render "the files this
// session produced" — the failure mode without it is every surface showing
// every session's files mixed together.
func TestListArtifactsFiltersBySession(t *testing.T) {
	srv := setupArtifactServer(t)
	ctx := context.Background()

	resp, err := srv.ListArtifacts(ctx, &loomv1.ListArtifactsRequest{SessionId: "sess-1"})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(resp.Artifacts) != 2 {
		t.Fatalf("got %d artifacts for sess-1, want 2", len(resp.Artifacts))
	}
	for _, a := range resp.Artifacts {
		// The response must also SAY which session each artifact belongs to —
		// the message carried no session_id before, so clients could not even
		// filter for themselves.
		if a.SessionId != "sess-1" {
			t.Errorf("artifact %s reports session %q, want sess-1", a.Id, a.SessionId)
		}
	}
}

// No session filter must behave exactly as before: everything comes back.
func TestListArtifactsUnfilteredIsUnchanged(t *testing.T) {
	srv := setupArtifactServer(t)

	resp, err := srv.ListArtifacts(context.Background(), &loomv1.ListArtifactsRequest{})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(resp.Artifacts) != 4 {
		t.Errorf("got %d artifacts unfiltered, want all 4", len(resp.Artifacts))
	}
}

// Names are only unique within a session: both sessions have a report.md, and
// the explicit session_id is what disambiguates them for a remote caller that
// has no session in its call context.
func TestGetArtifactByNameScopedToSession(t *testing.T) {
	srv := setupArtifactServer(t)
	ctx := context.Background()

	for _, tt := range []struct {
		session string
		wantID  string
	}{
		{"sess-1", "a1"},
		{"sess-2", "b1"},
	} {
		resp, err := srv.GetArtifact(ctx, &loomv1.GetArtifactRequest{Name: "report.md", SessionId: tt.session})
		if err != nil {
			t.Fatalf("GetArtifact(report.md, %s): %v", tt.session, err)
		}
		if resp.Artifact.Id != tt.wantID {
			t.Errorf("report.md in %s resolved to %s, want %s", tt.session, resp.Artifact.Id, tt.wantID)
		}
	}
}

// An ID lookup ignores session entirely — IDs are globally unique, and scoping
// them would only manufacture spurious not-founds.
func TestGetArtifactByIDIgnoresSession(t *testing.T) {
	srv := setupArtifactServer(t)

	resp, err := srv.GetArtifact(context.Background(), &loomv1.GetArtifactRequest{Id: "b1", SessionId: "sess-1"})
	if err != nil {
		t.Fatalf("GetArtifact by id: %v", err)
	}
	if resp.Artifact.Id != "b1" {
		t.Errorf("got %s, want b1", resp.Artifact.Id)
	}
}

// setupOwnedArtifactServer builds a server whose sessions have real owners and
// whose session store is wired, so the ownership predicate has something to
// resolve. alice owns sess-a, bob owns sess-b, and each has a report.md — the
// name collision that makes an unauthorized scope worth reaching for.
//
// Sessions are saved under the owner's own context because both backends take
// the owner from the context, not from Session.UserID (session_store.go:
// "cannot claim another owner by presetting session.UserID").
func setupOwnedArtifactServer(t *testing.T, enforce bool) *MultiAgentServer {
	t.Helper()

	sb, err := backend.NewSQLiteBackend(&loomv1.SQLiteStorageConfig{
		Path: filepath.Join(t.TempDir(), "loom.db"),
	}, observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	sessions := sb.SessionStorage()
	for _, o := range []struct{ user, session string }{
		{"alice", "sess-a"},
		{"bob", "sess-b"},
	} {
		ownerCtx := types.ContextWithUserID(context.Background(), o.user)
		if err := sessions.SaveSession(ownerCtx, &agent.Session{ID: o.session}); err != nil {
			t.Fatalf("create session %s: %v", o.session, err)
		}
	}

	store := sb.ArtifactStore()
	seed := []*artifacts.Artifact{
		{ID: "a1", Name: "report.md", Path: "/tmp/a1", Source: artifacts.SourceAgent, SessionID: "sess-a"},
		{ID: "b1", Name: "report.md", Path: "/tmp/b1", Source: artifacts.SourceAgent, SessionID: "sess-b"},
	}
	for _, a := range seed {
		if err := store.Index(context.Background(), a); err != nil {
			t.Fatalf("seed %s: %v", a.ID, err)
		}
	}

	srv := NewMultiAgentServer(nil, sessions)
	srv.SetArtifactStore(store)
	srv.SetEnforceSessionOwnership(enforce)
	return srv
}

// requireNotFound asserts a denial is reported as NotFound rather than
// PermissionDenied: a caller must not be able to tell "exists but not yours"
// from "no such session" by probing.
func requireNotFound(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: got no error, want NotFound — a caller reached another user's session", what)
	}
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("%s: got code %v, want NotFound", what, got)
	}
}

// The denial case the happy-path tests cannot cover: session_id arrives in the
// REQUEST, so it is caller-declared. On an ownership-enforcing deployment,
// naming another user's session must not read it — before the authorization
// check, this returned bob's listing to alice.
func TestListArtifactsDeniesForeignSession(t *testing.T) {
	srv := setupOwnedArtifactServer(t, true)
	aliceCtx := types.ContextWithUserID(context.Background(), "alice")

	_, err := srv.ListArtifacts(aliceCtx, &loomv1.ListArtifactsRequest{SessionId: "sess-b"})
	requireNotFound(t, err, "alice listing bob's session")
}

// The same boundary on the name path, which is the one an explicit session_id
// exists for: both sessions hold a report.md, so without the check alice could
// resolve bob's copy by naming his session.
func TestGetArtifactByNameDeniesForeignSession(t *testing.T) {
	srv := setupOwnedArtifactServer(t, true)
	aliceCtx := types.ContextWithUserID(context.Background(), "alice")

	_, err := srv.GetArtifact(aliceCtx, &loomv1.GetArtifactRequest{Name: "report.md", SessionId: "sess-b"})
	requireNotFound(t, err, "alice resolving report.md in bob's session")
}

// Enforcement must not be a blanket deny — the owner still reads her own
// session, which is what makes the check an isolation boundary rather than a
// feature switch.
func TestOwnerStillReadsOwnSessionUnderEnforcement(t *testing.T) {
	srv := setupOwnedArtifactServer(t, true)
	aliceCtx := types.ContextWithUserID(context.Background(), "alice")

	resp, err := srv.ListArtifacts(aliceCtx, &loomv1.ListArtifactsRequest{SessionId: "sess-a"})
	if err != nil {
		t.Fatalf("alice listing her own session: %v", err)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Id != "a1" {
		t.Fatalf("got %d artifacts, want just a1", len(resp.Artifacts))
	}

	got, err := srv.GetArtifact(aliceCtx, &loomv1.GetArtifactRequest{Name: "report.md", SessionId: "sess-a"})
	if err != nil {
		t.Fatalf("alice resolving her own report.md: %v", err)
	}
	if got.Artifact.Id != "a1" {
		t.Errorf("report.md in sess-a resolved to %s, want a1", got.Artifact.Id)
	}
}

// With ownership enforcement on, a blank identity is never a wildcard — the
// rule sessionAccessibleBy already applies to every other session-scoped RPC.
func TestAnonymousCallerDeniedUnderEnforcement(t *testing.T) {
	srv := setupOwnedArtifactServer(t, true)

	_, err := srv.ListArtifacts(context.Background(), &loomv1.ListArtifactsRequest{SessionId: "sess-a"})
	requireNotFound(t, err, "anonymous caller scoping to a session")
}

// Single-tenant deployments keep the permissive behaviour their trust model
// documents (cmd/looms: "All data is accessible to all callers"). This is the
// regression guard for the default mode: adding the authorization check must
// not break local session filtering.
func TestSingleTenantSessionScopeStillPermitted(t *testing.T) {
	srv := setupOwnedArtifactServer(t, false)

	resp, err := srv.ListArtifacts(context.Background(), &loomv1.ListArtifactsRequest{SessionId: "sess-b"})
	if err != nil {
		t.Fatalf("single-tenant session filter: %v", err)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Id != "b1" {
		t.Fatalf("got %d artifacts, want just b1", len(resp.Artifacts))
	}
}

// An ID lookup stays unscoped by design, so it is NOT an isolation boundary:
// this records that explicitly rather than leaving it implied by the happy-path
// test, because "session scoping" could otherwise be read as protecting ids too.
func TestGetArtifactByIDRemainsUnscopedUnderEnforcement(t *testing.T) {
	srv := setupOwnedArtifactServer(t, true)
	aliceCtx := types.ContextWithUserID(context.Background(), "alice")

	resp, err := srv.GetArtifact(aliceCtx, &loomv1.GetArtifactRequest{Id: "b1"})
	if err != nil {
		t.Fatalf("id lookup under enforcement: %v", err)
	}
	if resp.Artifact.Id != "b1" {
		t.Errorf("got %s, want b1", resp.Artifact.Id)
	}
}
