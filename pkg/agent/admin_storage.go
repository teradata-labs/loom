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
package agent

import (
	"context"
)

// AdminSession represents a session with its owner for admin queries.
// Uses a pointer to Session to avoid copying the embedded sync.RWMutex.
type AdminSession struct {
	*Session
	UserID string
}

// UserSessionCount holds the session count for a single user.
type UserSessionCount struct {
	UserID       string
	SessionCount int32
}

// SystemStats holds aggregate system-wide statistics across all users.
type SystemStats struct {
	TotalSessions       int32
	TotalMessages       int64
	TotalToolExecutions int64
	TotalUsers          int32
	TotalCostUSD        float64
	TotalTokens         int64
}

// AdminStorage defines operations that bypass RLS for platform administration.
// Implementations must execute queries without setting app.current_user_id,
// giving cross-tenant visibility to authorized operators.
type AdminStorage interface {
	// ListAllSessions returns sessions across all users (bypasses RLS).
	ListAllSessions(ctx context.Context, limit, offset int) ([]AdminSession, int32, error)

	// CountSessionsByUser returns session counts grouped by user_id.
	CountSessionsByUser(ctx context.Context) ([]UserSessionCount, error)

	// GetSystemStats returns aggregate statistics across all users.
	GetSystemStats(ctx context.Context) (*SystemStats, error)
}
