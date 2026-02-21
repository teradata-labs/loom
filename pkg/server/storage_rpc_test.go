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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/storage/backend"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// mockStorageBackend implements backend.StorageBackend for testing.
// It does NOT implement backend.MigrationInspector, so dry-run falls back
// to the generic "backend does not support migration introspection" message.
type mockStorageBackend struct {
	pingErr    error
	migrateErr error
}

// mockInspectableBackend implements both backend.StorageBackend and
// backend.MigrationInspector for testing the dry-run introspection path.
type mockInspectableBackend struct {
	mockStorageBackend
	pendingMigrations []*backend.PendingMigration
	pendingErr        error
}

func (m *mockInspectableBackend) PendingMigrations(_ context.Context) ([]*backend.PendingMigration, error) {
	return m.pendingMigrations, m.pendingErr
}

func (m *mockStorageBackend) SessionStorage() agent.SessionStorage         { return nil }
func (m *mockStorageBackend) ErrorStore() agent.ErrorStore                 { return nil }
func (m *mockStorageBackend) ArtifactStore() artifacts.ArtifactStore       { return nil }
func (m *mockStorageBackend) ResultStore() storage.ResultStore             { return nil }
func (m *mockStorageBackend) HumanRequestStore() shuttle.HumanRequestStore { return nil }
func (m *mockStorageBackend) Close() error                                 { return nil }

func (m *mockStorageBackend) Ping(_ context.Context) error {
	return m.pingErr
}

func (m *mockStorageBackend) Migrate(_ context.Context) error {
	return m.migrateErr
}

func TestGetStorageStatus_NoBackend(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)

	resp, err := s.GetStorageStatus(context.Background(), &loomv1.GetStorageStatusRequest{})
	require.Error(t, err)
	assert.Nil(t, resp)

	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Contains(t, st.Message(), "storage backend not configured")
}

func TestGetStorageStatus_Healthy(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	s.SetStorageBackend(&mockStorageBackend{})
	s.SetStorageBackendType(loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_SQLITE)

	resp, err := s.GetStorageStatus(context.Background(), &loomv1.GetStorageStatusRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Status)
	assert.True(t, resp.Status.Healthy)
	assert.Equal(t, loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_SQLITE, resp.Status.Backend)
	assert.Empty(t, resp.Status.Error)
}

func TestGetStorageStatus_Unhealthy(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	s.SetStorageBackend(&mockStorageBackend{pingErr: fmt.Errorf("connection refused")})
	s.SetStorageBackendType(loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_POSTGRES)

	resp, err := s.GetStorageStatus(context.Background(), &loomv1.GetStorageStatusRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Status)
	assert.False(t, resp.Status.Healthy)
	assert.Equal(t, loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_POSTGRES, resp.Status.Backend)
	assert.Contains(t, resp.Status.Error, "connection refused")
}

func TestRunMigration_NoBackend(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)

	resp, err := s.RunMigration(context.Background(), &loomv1.RunMigrationRequest{})
	require.Error(t, err)
	assert.Nil(t, resp)

	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

func TestRunMigration_DryRun_NoInspector(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	s.SetStorageBackend(&mockStorageBackend{})

	resp, err := s.RunMigration(context.Background(), &loomv1.RunMigrationRequest{
		DryRun: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Steps, 1)
	assert.Contains(t, resp.Steps[0].Description, "does not support migration introspection")
}

func TestRunMigration_DryRun_WithInspector(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	s.SetStorageBackend(&mockInspectableBackend{
		pendingMigrations: []*backend.PendingMigration{
			{Version: 1, Description: "create sessions table", SQL: "CREATE TABLE sessions (...)"},
			{Version: 2, Description: "add index on created_at", SQL: "CREATE INDEX ..."},
		},
	})

	resp, err := s.RunMigration(context.Background(), &loomv1.RunMigrationRequest{
		DryRun: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Steps, 2)
	assert.Equal(t, int32(1), resp.Steps[0].Version)
	assert.Equal(t, "create sessions table", resp.Steps[0].Description)
	assert.Equal(t, "CREATE TABLE sessions (...)", resp.Steps[0].Sql)
	assert.Equal(t, int32(2), resp.Steps[1].Version)
}

func TestRunMigration_DryRun_InspectorError(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	s.SetStorageBackend(&mockInspectableBackend{
		pendingErr: fmt.Errorf("cannot read migration state"),
	})

	_, err := s.RunMigration(context.Background(), &loomv1.RunMigrationRequest{
		DryRun: true,
	})
	require.Error(t, err)

	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
	assert.Contains(t, st.Message(), "cannot read migration state")
}

func TestRunMigration_DryRun_NoPendingMigrations(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	s.SetStorageBackend(&mockInspectableBackend{
		pendingMigrations: []*backend.PendingMigration{},
	})

	resp, err := s.RunMigration(context.Background(), &loomv1.RunMigrationRequest{
		DryRun: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Empty(t, resp.Steps)
}

func TestRunMigration_Success(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	s.SetStorageBackend(&mockStorageBackend{})

	resp, err := s.RunMigration(context.Background(), &loomv1.RunMigrationRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestRunMigration_Error(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	s.SetStorageBackend(&mockStorageBackend{migrateErr: fmt.Errorf("migration failed")})

	_, err := s.RunMigration(context.Background(), &loomv1.RunMigrationRequest{})
	require.Error(t, err)

	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
	assert.Contains(t, st.Message(), "migration failed")
}
