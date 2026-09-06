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
	"os"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/backend"
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
	defer sb.Close()

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
	if err := persistCheckpoint(context.Background(), nil, path, ckpt); err != nil {
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
	defer sb.Close()

	dir := t.TempDir()
	path := dir + "/test.checkpoint.pb"
	ckpt := &loomv1.WorkflowCheckpoint{WorkflowId: "wf-store-test"}

	if err := persistCheckpoint(context.Background(), sb, path, ckpt); err != nil {
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
	defer sb.Close()

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
