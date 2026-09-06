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
	"testing"

	"github.com/teradata-labs/loom/pkg/observability"
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
