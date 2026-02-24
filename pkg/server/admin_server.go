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

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// AdminServer implements the AdminService gRPC service.
// It wraps an AdminStorage backend to expose admin operations via gRPC.
// Access is optionally gated by a token passed in the "x-admin-token" gRPC metadata header.
type AdminServer struct {
	loomv1.UnimplementedAdminServiceServer
	store      agent.AdminStorage
	adminToken string
}

// NewAdminServer creates a new admin gRPC server.
// If adminToken is non-empty, every RPC will require the caller to supply a
// matching "x-admin-token" metadata header. An empty adminToken disables the check.
// Returns nil if no admin storage is configured (graceful degradation).
func NewAdminServer(store agent.AdminStorage, adminToken string) *AdminServer {
	if store == nil {
		return nil
	}
	return &AdminServer{store: store, adminToken: adminToken}
}

// checkAdminAuth validates the admin token from gRPC metadata.
// If the server was created with an empty adminToken, all requests are allowed.
func (s *AdminServer) checkAdminAuth(ctx context.Context) error {
	if s.adminToken == "" {
		return nil
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.PermissionDenied, "missing metadata; admin token required")
	}

	tokens := md.Get("x-admin-token")
	if len(tokens) == 0 {
		return status.Error(codes.PermissionDenied, "missing x-admin-token header")
	}

	if tokens[0] != s.adminToken {
		return status.Error(codes.PermissionDenied, "invalid admin token")
	}

	return nil
}

// ListAllSessions lists sessions across all users (bypasses RLS).
func (s *AdminServer) ListAllSessions(ctx context.Context, req *loomv1.ListAllSessionsRequest) (*loomv1.ListAllSessionsResponse, error) {
	if err := s.checkAdminAuth(ctx); err != nil {
		return nil, err
	}
	if s.store == nil {
		return nil, status.Error(codes.Unavailable, "admin storage not configured")
	}

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 50
	}
	offset := int(req.GetOffset())

	sessions, totalCount, err := s.store.ListAllSessions(ctx, limit, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list sessions: %v", err)
	}

	// Convert to proto sessions
	protoSessions := make([]*loomv1.Session, 0, len(sessions))
	for _, sess := range sessions {
		protoSessions = append(protoSessions, ConvertSession(sess.Session))
	}

	return &loomv1.ListAllSessionsResponse{
		Sessions:   protoSessions,
		TotalCount: totalCount,
	}, nil
}

// CountSessionsByUser returns session counts grouped by user.
func (s *AdminServer) CountSessionsByUser(ctx context.Context, req *loomv1.CountSessionsByUserRequest) (*loomv1.CountSessionsByUserResponse, error) {
	if err := s.checkAdminAuth(ctx); err != nil {
		return nil, err
	}
	if s.store == nil {
		return nil, status.Error(codes.Unavailable, "admin storage not configured")
	}

	counts, err := s.store.CountSessionsByUser(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to count sessions by user: %v", err)
	}

	userCounts := make(map[string]int32, len(counts))
	for _, c := range counts {
		userCounts[c.UserID] = c.SessionCount
	}

	return &loomv1.CountSessionsByUserResponse{
		UserCounts: userCounts,
	}, nil
}

// GetSystemStats returns aggregate system statistics across all users.
func (s *AdminServer) GetSystemStats(ctx context.Context, req *loomv1.GetSystemStatsRequest) (*loomv1.GetSystemStatsResponse, error) {
	if err := s.checkAdminAuth(ctx); err != nil {
		return nil, err
	}
	if s.store == nil {
		return nil, status.Error(codes.Unavailable, "admin storage not configured")
	}

	stats, err := s.store.GetSystemStats(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get system stats: %v", err)
	}

	return &loomv1.GetSystemStatsResponse{
		TotalSessions:       stats.TotalSessions,
		TotalMessages:       stats.TotalMessages,
		TotalToolExecutions: stats.TotalToolExecutions,
		TotalUsers:          stats.TotalUsers,
		TotalCostUsd:        stats.TotalCostUSD,
		TotalTokens:         stats.TotalTokens,
	}, nil
}
