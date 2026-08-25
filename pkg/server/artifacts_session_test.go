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
