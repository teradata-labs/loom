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

//go:build integration

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

const defaultServerAddr = "localhost:60051"

// serverAddr returns the gRPC server address from the environment or default.
func serverAddr() string {
	if addr := os.Getenv("LOOM_E2E_SERVER_ADDR"); addr != "" {
		return addr
	}
	return defaultServerAddr
}

// dialServer returns a gRPC client connection to the server.
// The connection is closed via t.Cleanup.
func dialServer(t *testing.T) *grpc.ClientConn {
	t.Helper()

	conn, err := grpc.NewClient(
		serverAddr(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err, "failed to dial gRPC server at %s", serverAddr())

	t.Cleanup(func() {
		conn.Close()
	})

	return conn
}

// loomClient returns a LoomServiceClient connected to the test server.
func loomClient(t *testing.T) loomv1.LoomServiceClient {
	t.Helper()
	return loomv1.NewLoomServiceClient(dialServer(t))
}

// adminClient returns an AdminServiceClient connected to the test server.
func adminClient(t *testing.T) loomv1.AdminServiceClient {
	t.Helper()
	return loomv1.NewAdminServiceClient(dialServer(t))
}

// withUserID returns a context with the x-user-id gRPC metadata header set.
func withUserID(ctx context.Context, userID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "x-user-id", userID)
}

// expectedBackend reads LOOM_E2E_BACKEND env var and returns the expected
// StorageBackendType. Defaults to SQLITE if not set.
func expectedBackend() loomv1.StorageBackendType {
	switch os.Getenv("LOOM_E2E_BACKEND") {
	case "postgres":
		return loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_POSTGRES
	default:
		return loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_SQLITE
	}
}

// isPostgres returns true if the expected backend is PostgreSQL.
func isPostgres() bool {
	return os.Getenv("LOOM_E2E_BACKEND") == "postgres"
}

// uniqueTestID returns a unique identifier with the given prefix for test isolation.
func uniqueTestID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// waitForHealthy polls GetStorageStatus until the server reports healthy or the
// timeout is reached. Fails the test if the server is not healthy in time.
func waitForHealthy(t *testing.T, client loomv1.LoomServiceClient) {
	t.Helper()

	ctx := withUserID(context.Background(), "health-check-user")
	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		resp, err := client.GetStorageStatus(ctx, &loomv1.GetStorageStatusRequest{})
		if err == nil && resp.GetStatus().GetHealthy() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("server at %s did not become healthy within 30s", serverAddr())
}

// cleanupSession deletes a session as part of test cleanup. Errors are logged
// but do not fail the test (best-effort cleanup).
func cleanupSession(t *testing.T, client loomv1.LoomServiceClient, userID, sessionID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := withUserID(context.Background(), userID)
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, err := client.DeleteSession(ctx, &loomv1.DeleteSessionRequest{
			SessionId: sessionID,
		})
		if err != nil {
			t.Logf("cleanup: failed to delete session %s: %v", sessionID, err)
		}
	})
}
