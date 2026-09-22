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

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/observability"
)

// TestDecisionShadowStore_Postgres exercises the Postgres store against a
// real database (TEST_POSTGRES_URL), including tenant isolation through RLS.
func TestDecisionShadowStore_Postgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping PostgreSQL store test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	migrator, err := NewMigrator(pool, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(ctx))

	store := NewDecisionShadowStore(pool, nil)
	site := "it." + uuid.NewString()
	userA := ContextWithUserID(ctx, "user-a-"+uuid.NewString())
	userB := ContextWithUserID(ctx, "user-b-"+uuid.NewString())
	now := time.Now().UTC().Truncate(time.Microsecond)

	rec := func(qid string, at time.Time) *loomv1.DecisionShadowRecord {
		return &loomv1.DecisionShadowRecord{
			RecordedAt: timestamppb.New(at), Site: site, SessionId: "s", QuestionId: qid,
			Kind: decision.KindChoice, CandidateAnswer: "auth", CandidateConfidence: 0.8,
			CandidateProbabilities: map[string]float64{"auth": 0.8, "other": 0.2},
			ReferenceAnswer:        "auth", ReferenceSource: "ref", LatencyMs: 10, InputTokens: 5, CostUsd: 0.0001,
			Model: "m", Provider: "mock", Path: loomv1.DecisionPath_DECISION_PATH_FALLBACK,
		}
	}
	require.NoError(t, store.RecordShadow(userA, []*loomv1.DecisionShadowRecord{rec("q1", now.Add(-time.Hour)), rec("q2", now)}))
	require.NoError(t, store.RecordShadow(userB, []*loomv1.DecisionShadowRecord{rec("q3", now)}))

	rowsA, err := store.QueryShadow(userA, decision.ShadowQuery{Site: site})
	require.NoError(t, err)
	require.Len(t, rowsA, 2, "tenant A sees only its own rows")
	assert.Equal(t, "q2", rowsA[0].QuestionId, "newest first")
	assert.InDelta(t, 0.8, rowsA[0].CandidateProbabilities["auth"], 1e-9)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, rowsA[0].Path)
	assert.Equal(t, now, rowsA[0].RecordedAt.AsTime())

	rowsB, err := store.QueryShadow(userB, decision.ShadowQuery{Site: site})
	require.NoError(t, err)
	assert.Len(t, rowsB, 1, "tenant B sees only its own rows")

	since, err := store.QueryShadow(userA, decision.ShadowQuery{Site: site, Since: now.Add(-time.Minute)})
	require.NoError(t, err)
	assert.Len(t, since, 1)

	limited, err := store.QueryShadow(userA, decision.ShadowQuery{Site: site, Limit: 1})
	require.NoError(t, err)
	assert.Len(t, limited, 1)
}
