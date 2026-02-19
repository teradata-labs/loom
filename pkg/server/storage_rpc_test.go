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

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// mockStorageBackend implements backend.StorageBackend for testing.
type mockStorageBackend struct {
	pingErr    error
	migrateErr error
}

func (m *mockStorageBackend) SessionStorage() agent.SessionStorage       { return nil }
func (m *mockStorageBackend) ErrorStore() agent.ErrorStore               { return nil }
func (m *mockStorageBackend) ArtifactStore() artifacts.ArtifactStore     { return nil }
func (m *mockStorageBackend) ResultStore() storage.ResultStore           { return nil }
func (m *mockStorageBackend) HumanRequestStore() shuttle.HumanRequestStore { return nil }
func (m *mockStorageBackend) Close() error                               { return nil }

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
	s.storageBackend = &mockStorageBackend{}
	s.storageBackendType = loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_SQLITE

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
	s.storageBackend = &mockStorageBackend{pingErr: fmt.Errorf("connection refused")}
	s.storageBackendType = loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_POSTGRES

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

func TestRunMigration_DryRun(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	s.storageBackend = &mockStorageBackend{}

	resp, err := s.RunMigration(context.Background(), &loomv1.RunMigrationRequest{
		DryRun: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.NotEmpty(t, resp.Steps)
}

func TestRunMigration_Success(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	s.storageBackend = &mockStorageBackend{}

	resp, err := s.RunMigration(context.Background(), &loomv1.RunMigrationRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.Empty(t, resp.Error)
}

func TestRunMigration_Error(t *testing.T) {
	s := NewMultiAgentServer(map[string]*agent.Agent{}, nil)
	s.storageBackend = &mockStorageBackend{migrateErr: fmt.Errorf("migration failed")}

	resp, err := s.RunMigration(context.Background(), &loomv1.RunMigrationRequest{})
	require.NoError(t, err) // gRPC returns success, error in response body
	require.NotNil(t, resp)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "migration failed")
}
