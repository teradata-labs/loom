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
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/storage/backend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SetStorageBackend sets the storage backend for health checks and migration RPCs.
func (s *MultiAgentServer) SetStorageBackend(sb backend.StorageBackend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storageBackend = sb
}

// SetStorageBackendType sets the storage backend type for health status reporting.
func (s *MultiAgentServer) SetStorageBackendType(backendType loomv1.StorageBackendType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storageBackendType = backendType
}

// GetStorageStatus returns the health status of the storage backend.
func (s *MultiAgentServer) GetStorageStatus(ctx context.Context, _ *loomv1.GetStorageStatusRequest) (*loomv1.GetStorageStatusResponse, error) {
	s.mu.RLock()
	sb := s.storageBackend
	backendType := s.storageBackendType
	s.mu.RUnlock()

	if sb == nil {
		return nil, status.Error(codes.FailedPrecondition, "storage backend not configured")
	}

	// Measure ping latency. Sub-millisecond pings report as 1ms to satisfy
	// callers that expect a positive latency for a successful round-trip.
	start := time.Now()
	pingErr := sb.Ping(ctx)
	latencyMs := time.Since(start).Milliseconds()
	if latencyMs == 0 && pingErr == nil {
		latencyMs = 1
	}

	healthStatus := &loomv1.StorageHealthStatus{
		Backend:   backendType,
		Healthy:   pingErr == nil,
		LatencyMs: latencyMs,
	}

	if pingErr != nil {
		healthStatus.Error = pingErr.Error()
	}

	// Populate migration version and pool stats for backends that support it.
	if provider, ok := sb.(backend.StorageDetailProvider); ok {
		if migVer, poolStats, err := provider.StorageDetails(ctx); err == nil {
			healthStatus.MigrationVersion = migVer
			healthStatus.PoolStats = poolStats
		}
	}

	return &loomv1.GetStorageStatusResponse{
		Status: healthStatus,
	}, nil
}

// RunMigration runs database migrations on the storage backend.
func (s *MultiAgentServer) RunMigration(ctx context.Context, req *loomv1.RunMigrationRequest) (*loomv1.RunMigrationResponse, error) {
	s.mu.RLock()
	sb := s.storageBackend
	s.mu.RUnlock()

	if sb == nil {
		return nil, status.Error(codes.FailedPrecondition, "storage backend not configured")
	}

	if req.GetDryRun() {
		// Dry-run mode: report what would happen without applying changes.
		// If the backend implements MigrationInspector, query the actual
		// pending migrations. Otherwise, return a descriptive fallback.
		if inspector, ok := sb.(backend.MigrationInspector); ok {
			pending, err := inspector.PendingMigrations(ctx)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to inspect pending migrations: %v", err)
			}
			steps := make([]*loomv1.MigrationStep, 0, len(pending))
			for _, m := range pending {
				steps = append(steps, &loomv1.MigrationStep{
					Version:     m.Version,
					Description: m.Description,
					Sql:         m.SQL,
				})
			}
			return &loomv1.RunMigrationResponse{
				Steps: steps,
			}, nil
		}
		// Backend does not support migration introspection.
		return &loomv1.RunMigrationResponse{
			Steps: []*loomv1.MigrationStep{
				{
					Version:     0,
					Description: "dry-run: backend does not support migration introspection; would run all pending migrations to latest",
				},
			},
		}, nil
	}

	// Run migrations
	if err := sb.Migrate(ctx); err != nil {
		return nil, status.Errorf(codes.Internal, "migration failed: %v", err)
	}

	resp := &loomv1.RunMigrationResponse{}
	// Populate current_version so callers can verify the applied migration level.
	if provider, ok := sb.(backend.StorageDetailProvider); ok {
		if migVer, _, err := provider.StorageDetails(ctx); err == nil {
			resp.CurrentVersion = migVer
		}
	}
	return resp, nil
}
