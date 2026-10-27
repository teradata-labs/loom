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

package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/backend"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// TestBuildWorkflowStorageBackend_DefaultsToSQLite verifies the zero-config
// path (no LOOM_DATA_DIR Postgres config set) produces a usable SQLite-backed
// StorageBackend, mirroring cmd_serve.go's construction.
func TestBuildWorkflowStorageBackend_DefaultsToSQLite(t *testing.T) {
	// Isolate from the developer's real ~/.loom directory: with zero config,
	// BuildProtoStorageConfig/NewSQLiteBackend fall back to
	// $LOOM_DATA_DIR/loom.db (storage.GetDefaultLoomDBPath), which defaults
	// to the user's actual home directory when LOOM_DATA_DIR is unset. Point
	// it at a throwaway temp dir instead. t.Setenv restores the prior value
	// automatically on cleanup.
	t.Setenv("LOOM_DATA_DIR", t.TempDir())

	// The package-level `config` global is normally populated by cobra's
	// initConfig() before any command runs; it is nil here since the test
	// calls buildWorkflowStorageBackend directly. Set it to a zero-value
	// Config (defaults to the sqlite backend) and restore afterward, mirroring
	// the save/restore pattern used in cmd_hitl_test.go and cmd_upgrade_test.go.
	origConfig := config
	config = &Config{}
	t.Cleanup(func() { config = origConfig })

	sb, err := buildWorkflowStorageBackend(context.Background(), observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("buildWorkflowStorageBackend: %v", err)
	}
	defer func() { _ = sb.Close() }()

	if err := sb.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// setupIsolatedStorageConfig applies the same test isolation
// TestBuildWorkflowStorageBackend_DefaultsToSQLite established: point
// LOOM_DATA_DIR at a throwaway temp dir (never the developer's real ~/.loom)
// and give the package-level `config` global a zero value so
// buildWorkflowStorageBackend can be called directly outside of cobra's
// initConfig().
func setupIsolatedStorageConfig(t *testing.T) {
	t.Helper()
	t.Setenv("LOOM_DATA_DIR", t.TempDir())
	origConfig := config
	config = &Config{
		Storage: StorageBackendConfig{
			Migration: MigrationStorageConfig{
				// buildWorkflowStorageBackend only runs migrations when this
				// is true; a real config gets this from the
				// storage.migration.auto_migrate viper default (true), which
				// a bare zero-value Config{} bypasses.
				AutoMigrate: true,
			},
		},
	}
	t.Cleanup(func() { config = origConfig })
}

func TestPersistCheckpoint_FallsBackToFileWhenNoStore(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.checkpoint.pb"
	ckpt := &loomv1.WorkflowCheckpoint{WorkflowId: "wf-fallback-test"}

	// nil storageBackend simulates buildWorkflowStorageBackend having failed.
	if _, err := persistCheckpoint(context.Background(), nil, zap.NewNop(), path, ckpt); err != nil {
		t.Fatalf("persistCheckpoint: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected fallback file to exist: %v", err)
	}
	got := &loomv1.WorkflowCheckpoint{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("unmarshal fallback file: %v", err)
	}
	if got.WorkflowId != "wf-fallback-test" {
		t.Fatalf("got workflow_id %q, want wf-fallback-test", got.WorkflowId)
	}
}

func TestPersistCheckpoint_WritesBothStoreAndFileWhenStoreAvailable(t *testing.T) {
	setupIsolatedStorageConfig(t)

	sb, err := buildWorkflowStorageBackend(context.Background(), observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("buildWorkflowStorageBackend: %v", err)
	}
	defer func() { _ = sb.Close() }()

	dir := t.TempDir()
	path := dir + "/test.checkpoint.pb"
	ckpt := &loomv1.WorkflowCheckpoint{WorkflowId: "wf-store-test"}

	if _, err := persistCheckpoint(context.Background(), sb, zap.NewNop(), path, ckpt); err != nil {
		t.Fatalf("persistCheckpoint: %v", err)
	}

	// Both the local file (same-machine resume, unchanged from prior
	// behavior) and the store (disaster-recovery resume by workflow ID) must
	// be written — this is belt-and-suspenders, not either/or.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected local file to still be written: %v", err)
	}

	csp := sb.(backend.CheckpointStoreProvider)
	loaded, err := csp.CheckpointStore().LoadCheckpoint(context.Background(), "wf-store-test")
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if loaded == nil || loaded.WorkflowId != "wf-store-test" {
		t.Fatalf("LoadCheckpoint: got %+v, want workflow_id=wf-store-test", loaded)
	}
}

func TestLoadCheckpoint_PrefersLocalFileWhenPresent(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.checkpoint.pb"
	ckpt := &loomv1.WorkflowCheckpoint{WorkflowId: "wf-file-present-test"}
	data, err := proto.Marshal(ckpt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	got, err := loadCheckpoint(context.Background(), nil, path)
	if err != nil {
		t.Fatalf("loadCheckpoint: %v", err)
	}
	if got == nil || got.WorkflowId != "wf-file-present-test" {
		t.Fatalf("loadCheckpoint: got %+v, want workflow_id=wf-file-present-test", got)
	}
}

func TestLoadCheckpoint_FallsBackToStoreByWorkflowIDWhenFileMissing(t *testing.T) {
	setupIsolatedStorageConfig(t)

	sb, err := buildWorkflowStorageBackend(context.Background(), observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("buildWorkflowStorageBackend: %v", err)
	}
	defer func() { _ = sb.Close() }()

	ckpt := &loomv1.WorkflowCheckpoint{WorkflowId: "wf-recovered-by-id"}
	csp := sb.(backend.CheckpointStoreProvider)
	if err := csp.CheckpointStore().SaveCheckpoint(context.Background(), ckpt); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	// No file exists at this path — the argument is instead treated as the
	// workflow ID to look up in the store, simulating resume on a machine
	// that never had the original local checkpoint file.
	got, err := loadCheckpoint(context.Background(), sb, "wf-recovered-by-id")
	if err != nil {
		t.Fatalf("loadCheckpoint: %v", err)
	}
	if got == nil || got.WorkflowId != "wf-recovered-by-id" {
		t.Fatalf("loadCheckpoint: got %+v, want workflow_id=wf-recovered-by-id", got)
	}
}

func TestLoadCheckpoint_NoFileNoStore_ReturnsError(t *testing.T) {
	_, err := loadCheckpoint(context.Background(), nil, "/nonexistent/path.checkpoint.pb")
	if err == nil {
		t.Fatal("loadCheckpoint: expected an error when neither a file nor a store can resolve the argument")
	}
}

// TestWorkflowCheckpointUserID covers the tenant the workflow commands use for
// checkpoints in the storage backend: --user wins, postgres falls back to its
// configured default_user_id and otherwise refuses, and SQLite uses
// types.DefaultUserID like SQLite sessions do.
func TestWorkflowCheckpointUserID(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		pgUser  string
		flag    string
		want    string
		wantErr bool
	}{
		{name: "sqlite default", backend: "sqlite", want: types.DefaultUserID},
		{name: "unset backend means sqlite", backend: "", want: types.DefaultUserID},
		{name: "sqlite with flag", backend: "sqlite", flag: "alice", want: "alice"},
		{name: "postgres with flag", backend: "postgres", pgUser: "svc", flag: "alice", want: "alice"},
		{name: "postgres default_user_id", backend: "postgres", pgUser: "svc", want: "svc"},
		{name: "postgres with no tenant", backend: "postgres", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origConfig, origUser := config, workflowUser
			t.Cleanup(func() { config, workflowUser = origConfig, origUser })
			config = &Config{}
			config.Storage.Backend = tt.backend
			config.Storage.Postgres.DefaultUserID = tt.pgUser
			workflowUser = tt.flag

			got, err := workflowCheckpointUserID()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("workflowCheckpointUserID() = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("workflowCheckpointUserID(): %v", err)
			}
			if got != tt.want {
				t.Fatalf("workflowCheckpointUserID() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRunResume_RejectNeedsNoLLM checks that `looms workflow resume --reject`
// ends the workflow without building the LLM provider or any agent: a
// rejection runs no stage, so it must not depend on LLM credentials.
func TestRunResume_RejectNeedsNoLLM(t *testing.T) {
	t.Setenv("LOOM_DATA_DIR", t.TempDir())
	origConfig, origReject := config, resumeReject
	t.Cleanup(func() { config, resumeReject = origConfig, origReject })
	config = &Config{}
	resumeReject = true

	dir := t.TempDir()
	workflowPath := dir + "/gated.yaml"
	if err := os.WriteFile(workflowPath, []byte(`apiVersion: loom/v1
kind: Workflow
metadata:
  name: gated
spec:
  type: pipeline
  initial_prompt: "go"
  stages:
    - agent_id: designer
      prompt_template: "Design: {{previous}}"
      hitl_gate:
        prompt_template: "Review:\n{{output}}"
        request_type: approval
    - agent_id: executor
      prompt_template: "Execute: {{previous}}"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	ckptPath := dir + "/gated.checkpoint.pb"
	data, err := proto.Marshal(&loomv1.WorkflowCheckpoint{
		CheckpointVersion: 1,
		WorkflowId:        "wf-reject",
		PendingGate:       &loomv1.HITLGateRequest{StageAgentId: "designer", StageNumber: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ckptPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { runResume(nil, []string{workflowPath, ckptPath}) })

	if !strings.Contains(out, "Workflow rejected") {
		t.Fatalf("output does not report the rejection:\n%s", out)
	}
	if strings.Contains(out, "LLM Provider") {
		t.Fatalf("resume --reject built an LLM provider; output:\n%s", out)
	}
}

// captureStdout returns everything fn writes to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = orig }()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}
