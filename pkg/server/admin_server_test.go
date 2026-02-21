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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// mockAdminStorage implements agent.AdminStorage for testing.
type mockAdminStorage struct {
	sessions []agent.AdminSession
	total    int32
	counts   []agent.UserSessionCount
	stats    *agent.SystemStats
	err      error
}

func (m *mockAdminStorage) ListAllSessions(_ context.Context, _, _ int) ([]agent.AdminSession, int32, error) {
	return m.sessions, m.total, m.err
}

func (m *mockAdminStorage) CountSessionsByUser(_ context.Context) ([]agent.UserSessionCount, error) {
	return m.counts, m.err
}

func (m *mockAdminStorage) GetSystemStats(_ context.Context) (*agent.SystemStats, error) {
	return m.stats, m.err
}

func TestNewAdminServer_NilStore(t *testing.T) {
	srv := NewAdminServer(nil, "some-token")
	assert.Nil(t, srv, "NewAdminServer should return nil when store is nil")
}

func TestNewAdminServer_ValidStore(t *testing.T) {
	store := &mockAdminStorage{}
	srv := NewAdminServer(store, "my-secret")
	require.NotNil(t, srv)
	assert.Equal(t, "my-secret", srv.adminToken)
}

func TestCheckAdminAuth(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		md       metadata.MD
		noMD     bool
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name:    "empty token skips auth",
			token:   "",
			noMD:    true,
			wantErr: false,
		},
		{
			name:    "valid token passes",
			token:   "secret-123",
			md:      metadata.Pairs("x-admin-token", "secret-123"),
			wantErr: false,
		},
		{
			name:     "wrong token rejected",
			token:    "secret-123",
			md:       metadata.Pairs("x-admin-token", "wrong-token"),
			wantErr:  true,
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "missing header rejected",
			token:    "secret-123",
			md:       metadata.MD{},
			wantErr:  true,
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "no metadata rejected",
			token:    "secret-123",
			noMD:     true,
			wantErr:  true,
			wantCode: codes.PermissionDenied,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &AdminServer{adminToken: tt.token}
			ctx := context.Background()
			if !tt.noMD {
				ctx = metadata.NewIncomingContext(ctx, tt.md)
			}

			err := srv.checkAdminAuth(ctx)
			if tt.wantErr {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok)
				assert.Equal(t, tt.wantCode, st.Code())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestListAllSessions_AuthRequired(t *testing.T) {
	store := &mockAdminStorage{
		sessions: nil,
		total:    0,
	}
	srv := NewAdminServer(store, "admin-token")

	// No metadata -- should fail
	resp, err := srv.ListAllSessions(context.Background(), &loomv1.ListAllSessionsRequest{})
	assert.Nil(t, resp)
	require.Error(t, err)
	st, _ := status.FromError(err)
	assert.Equal(t, codes.PermissionDenied, st.Code())

	// With valid token -- should succeed
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-admin-token", "admin-token"))
	resp, err = srv.ListAllSessions(ctx, &loomv1.ListAllSessionsRequest{})
	require.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestCountSessionsByUser_AuthRequired(t *testing.T) {
	store := &mockAdminStorage{
		counts: []agent.UserSessionCount{
			{UserID: "user-1", SessionCount: 5},
		},
	}
	srv := NewAdminServer(store, "token-abc")

	// No metadata -- should fail
	resp, err := srv.CountSessionsByUser(context.Background(), &loomv1.CountSessionsByUserRequest{})
	assert.Nil(t, resp)
	require.Error(t, err)
	st, _ := status.FromError(err)
	assert.Equal(t, codes.PermissionDenied, st.Code())

	// With valid token -- should succeed
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-admin-token", "token-abc"))
	resp, err = srv.CountSessionsByUser(ctx, &loomv1.CountSessionsByUserRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, int32(5), resp.UserCounts["user-1"])
}

func TestGetSystemStats_AuthRequired(t *testing.T) {
	store := &mockAdminStorage{
		stats: &agent.SystemStats{
			TotalSessions: 10,
			TotalMessages: 100,
			TotalUsers:    3,
			TotalTokens:   5000,
		},
	}
	srv := NewAdminServer(store, "stats-token")

	// No metadata -- should fail
	resp, err := srv.GetSystemStats(context.Background(), &loomv1.GetSystemStatsRequest{})
	assert.Nil(t, resp)
	require.Error(t, err)
	st, _ := status.FromError(err)
	assert.Equal(t, codes.PermissionDenied, st.Code())

	// With valid token -- should succeed
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-admin-token", "stats-token"))
	resp, err = srv.GetSystemStats(ctx, &loomv1.GetSystemStatsRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, int32(10), resp.TotalSessions)
	assert.Equal(t, int64(100), resp.TotalMessages)
	assert.Equal(t, int32(3), resp.TotalUsers)
	assert.Equal(t, int64(5000), resp.TotalTokens)
}

func TestAdminServer_NoAuthWhenTokenEmpty(t *testing.T) {
	store := &mockAdminStorage{
		stats: &agent.SystemStats{TotalSessions: 1},
	}
	srv := NewAdminServer(store, "")

	// No metadata at all -- should still work because token is empty
	resp, err := srv.GetSystemStats(context.Background(), &loomv1.GetSystemStatsRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, int32(1), resp.TotalSessions)
}
