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

package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/observability"
)

func newShadowTestStore(t *testing.T) (*DecisionShadowStore, context.Context) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "shadow.db")
	db, err := sql.Open("sqlite3", sqlitedriver.DSN(dbPath, sqlitedriver.Options{BusyTimeoutMS: 5000, WAL: true, ForeignKeys: true}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	migrator, err := NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(ctx))
	return NewDecisionShadowStore(db, nil), ctx
}

func shadowRow(site, qid string, at time.Time) *loomv1.DecisionShadowRecord {
	return &loomv1.DecisionShadowRecord{
		RecordedAt:             timestamppb.New(at),
		Site:                   site,
		SessionId:              "sess-1",
		QuestionId:             qid,
		Kind:                   decision.KindChoice,
		CandidateAnswer:        "auth",
		CandidateConfidence:    0.87,
		CandidateProbabilities: map[string]float64{"auth": 0.9, "other": 0.1},
		ReferenceAnswer:        "auth",
		ReferenceSource:        "fabric.InferErrorType",
		LatencyMs:              321,
		InputTokens:            412,
		CostUsd:                0.0004,
		Model:                  "mock-1",
		Provider:               "mock",
		Path:                   loomv1.DecisionPath_DECISION_PATH_FALLBACK,
	}
}

func TestDecisionShadowStoreRoundTrip(t *testing.T) {
	store, ctx := newShadowTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	require.NoError(t, store.RecordShadow(ctx, nil), "empty batch is a no-op")
	require.NoError(t, store.RecordShadow(ctx, []*loomv1.DecisionShadowRecord{
		shadowRow("a", "q1", now.Add(-2*time.Hour)),
		shadowRow("a", "q2", now.Add(-time.Hour)),
		shadowRow("b", "q1", now),
		nil,
	}))

	all, err := store.QueryShadow(ctx, decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, "b", all[0].Site, "newest first")
	assert.Equal(t, "q2", all[1].QuestionId)
	assert.Equal(t, "q1", all[2].QuestionId)

	got := all[0]
	assert.NotEmpty(t, got.Id)
	assert.Equal(t, now, got.RecordedAt.AsTime())
	assert.Equal(t, "sess-1", got.SessionId)
	assert.Equal(t, decision.KindChoice, got.Kind)
	assert.Equal(t, "auth", got.CandidateAnswer)
	assert.InDelta(t, 0.87, got.CandidateConfidence, 1e-9)
	assert.InDelta(t, 0.9, got.CandidateProbabilities["auth"], 1e-9)
	assert.Equal(t, "auth", got.ReferenceAnswer)
	assert.Equal(t, "fabric.InferErrorType", got.ReferenceSource)
	assert.Equal(t, int64(321), got.LatencyMs)
	assert.Equal(t, int64(412), got.InputTokens)
	assert.InDelta(t, 0.0004, got.CostUsd, 1e-12)
	assert.Equal(t, "mock-1", got.Model)
	assert.Equal(t, "mock", got.Provider)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, got.Path)

	bySite, err := store.QueryShadow(ctx, decision.ShadowQuery{Site: "a"})
	require.NoError(t, err)
	assert.Len(t, bySite, 2)

	since, err := store.QueryShadow(ctx, decision.ShadowQuery{Since: now.Add(-90 * time.Minute)})
	require.NoError(t, err)
	assert.Len(t, since, 2)

	limited, err := store.QueryShadow(ctx, decision.ShadowQuery{Limit: 1})
	require.NoError(t, err)
	require.Len(t, limited, 1)
	assert.Equal(t, "b", limited[0].Site)

	none, err := store.QueryShadow(ctx, decision.ShadowQuery{Site: "zzz"})
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestDecisionShadowStoreDefaultsAndEmptyProbabilities(t *testing.T) {
	store, ctx := newShadowTestStore(t)
	before := time.Now().UTC().Add(-time.Second)
	rec := &loomv1.DecisionShadowRecord{Site: "s", QuestionId: "q", Kind: decision.KindNoul, Path: loomv1.DecisionPath_DECISION_PATH_ERROR, Error: "jev: HTTP 503: service unavailable"}
	require.NoError(t, store.RecordShadow(ctx, []*loomv1.DecisionShadowRecord{rec}))
	rows, err := store.QueryShadow(ctx, decision.ShadowQuery{Site: "s"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].RecordedAt.AsTime().After(before), "missing recorded_at defaults to now")
	assert.Nil(t, rows[0].CandidateProbabilities)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_ERROR, rows[0].Path)
	assert.Equal(t, "jev: HTTP 503: service unavailable", rows[0].Error, "ERROR rows keep their reason")
}

func TestDecisionShadowStoreConcurrentWrites(t *testing.T) {
	store, ctx := newShadowTestStore(t)
	var wg sync.WaitGroup
	const writers, perWriter = 8, 10
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				err := store.RecordShadow(ctx, []*loomv1.DecisionShadowRecord{shadowRow("c", "q", time.Now())})
				assert.NoError(t, err)
			}
		}(w)
	}
	wg.Wait()
	rows, err := store.QueryShadow(ctx, decision.ShadowQuery{Site: "c"})
	require.NoError(t, err)
	assert.Len(t, rows, writers*perWriter)
}
