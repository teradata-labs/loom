// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk && fts5

package observability

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewEmbeddedHawkTracer_SQLite(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	config := &EmbeddedConfig{
		StorageType:   "sqlite",
		SQLitePath:    dbPath,
		FlushInterval: 0,
	}

	tracer, err := NewEmbeddedHawkTracer(config)
	if err != nil {
		t.Fatalf("Failed to create embedded tracer with SQLite: %v", err)
	}
	defer tracer.Close()

	// Verify database file was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("Expected SQLite database file to be created")
	}
}

func TestNewEmbeddedHawkTracer_SQLiteNoPath(t *testing.T) {
	config := &EmbeddedConfig{
		StorageType: "sqlite",
		// Missing SQLitePath - should fail gracefully
	}

	_, err := NewEmbeddedHawkTracer(config)
	if err == nil {
		t.Fatal("Expected error when SQLitePath is not provided for sqlite storage")
	}
}
