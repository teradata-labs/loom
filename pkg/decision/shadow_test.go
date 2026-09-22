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

package decision_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/observability"
)

// memShadowStore is an in-memory decision.ShadowStore for tests.
type memShadowStore struct {
	mu   sync.Mutex
	rows []*loomv1.DecisionShadowRecord
	err  error
}

func (m *memShadowStore) RecordShadow(_ context.Context, records []*loomv1.DecisionShadowRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.rows = append(m.rows, records...)
	return nil
}

func (m *memShadowStore) QueryShadow(_ context.Context, q decision.ShadowQuery) ([]*loomv1.DecisionShadowRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*loomv1.DecisionShadowRecord
	for _, r := range m.rows {
		if q.Site == "" || r.Site == q.Site {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memShadowStore) Rows() []*loomv1.DecisionShadowRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*loomv1.DecisionShadowRecord(nil), m.rows...)
}

func TestCandidateAnswer(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", decision.CandidateAnswer(nil))
	assert.Equal(t, "", decision.CandidateAnswer(&loomv1.DecisionAnswer{}))
	assert.Equal(t, "true", decision.CandidateAnswer(&loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Noul{Noul: &loomv1.NoulAnswer{Probability: 0.5}}}))
	assert.Equal(t, "false", decision.CandidateAnswer(&loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Noul{Noul: &loomv1.NoulAnswer{Probability: 0.49}}}))
	c, err := decision.ChoiceFromProbabilities(map[string]float64{"a": 0.2, "b": 0.8})
	require.NoError(t, err)
	assert.Equal(t, "b", decision.CandidateAnswer(&loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Choice{Choice: c}}))
	s, err := decision.ScoreFromProbabilities(map[string]float64{"0": 0.1, "1": 0.2, "2": 0.7}, nil)
	require.NoError(t, err)
	assert.Equal(t, "2", decision.CandidateAnswer(&loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Score{Score: s}}))
}

func TestBuildShadowRecords(t *testing.T) {
	t.Parallel()
	choice, err := decision.Choice("k", map[string]any{"a": "A", "b": "B"})
	require.NoError(t, err)
	req, err := decision.NewRequest("site.x", "s", map[string]*loomv1.DecisionQuestion{
		"n": decision.Noul("?"),
		"c": choice,
	})
	require.NoError(t, err)

	t.Run("with response", func(t *testing.T) {
		t.Parallel()
		m := mock.New().AnswerNoul("n", 0.9).AnswerChoice("c", map[string]float64{"a": 0.3, "b": 0.7}).
			SetUsage(50, 0, 0.001).SetModel("m-1")
		out := decision.NewRouter(m).Decide(context.Background(), req)
		require.NotNil(t, out.Response)
		out.Latency = 42 * time.Millisecond

		refs := map[string]decision.Reference{
			"n": {Answer: "true", Source: "ref"},
			"c": {Answer: "a", Source: "ref"},
		}
		records := decision.BuildShadowRecords(req, out, "mock", "sess", refs)
		require.Len(t, records, 2)
		byID := map[string]*loomv1.DecisionShadowRecord{}
		for _, r := range records {
			byID[r.QuestionId] = r
		}
		assert.Equal(t, []string{"c", "n"}, []string{records[0].QuestionId, records[1].QuestionId}, "question-id order")

		n := byID["n"]
		assert.Equal(t, "site.x", n.Site)
		assert.Equal(t, "sess", n.SessionId)
		assert.Equal(t, decision.KindNoul, n.Kind)
		assert.Equal(t, "true", n.CandidateAnswer)
		assert.InDelta(t, 0.8, n.CandidateConfidence, 1e-9)
		assert.InDelta(t, 0.9, n.CandidateProbabilities["true"], 1e-9)
		assert.Equal(t, "true", n.ReferenceAnswer)
		assert.Equal(t, "ref", n.ReferenceSource)
		assert.Equal(t, int64(42), n.LatencyMs)
		assert.Equal(t, int64(50), n.InputTokens)
		assert.InDelta(t, 0.001, n.CostUsd, 1e-12)
		assert.Equal(t, "m-1", n.Model)
		assert.Equal(t, "mock", n.Provider)
		assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, n.Path, "no band: shadow")
		assert.NotNil(t, n.RecordedAt)

		c := byID["c"]
		assert.Equal(t, decision.KindChoice, c.Kind)
		assert.Equal(t, "b", c.CandidateAnswer)
		assert.Equal(t, "a", c.ReferenceAnswer)
		assert.InDelta(t, 0.7, c.CandidateProbabilities["b"], 1e-9)
	})

	t.Run("without response still emits rows", func(t *testing.T) {
		t.Parallel()
		out := decision.NewRouter(mock.New().SetError(decision.ErrOverloaded)).Decide(context.Background(), req)
		require.Nil(t, out.Response)
		records := decision.BuildShadowRecords(req, out, "mock", "", map[string]decision.Reference{"n": {Answer: "false", Source: "ref"}})
		require.Len(t, records, 2)
		for _, r := range records {
			assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_ERROR, r.Path)
			assert.Equal(t, "", r.CandidateAnswer)
			assert.Equal(t, 0.0, r.CandidateConfidence)
			assert.NotEqual(t, "", r.Kind, "kind falls back to the question's kind")
		}
	})

	t.Run("nil request", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, decision.BuildShadowRecords(nil, decision.Outcome{}, "", "", nil))
	})
}

func TestShadowRecorder(t *testing.T) {
	t.Parallel()
	rows := []*loomv1.DecisionShadowRecord{{Site: "s", QuestionId: "q"}}

	t.Run("nil store records nothing and is disabled", func(t *testing.T) {
		t.Parallel()
		r := decision.NewShadowRecorder(nil, nil)
		assert.False(t, r.Enabled())
		assert.NoError(t, r.Record(context.Background(), rows))
		var nilRecorder *decision.ShadowRecorder
		assert.False(t, nilRecorder.Enabled())
		assert.NoError(t, nilRecorder.Record(context.Background(), rows))
	})

	t.Run("writes and counts rows", func(t *testing.T) {
		t.Parallel()
		store := &memShadowStore{}
		tracer := newRecordingTracer()
		r := decision.NewShadowRecorder(store, tracer)
		require.True(t, r.Enabled())
		require.NoError(t, r.Record(context.Background(), rows))
		assert.Len(t, store.Rows(), 1)
		ms := tracer.Metrics(observability.MetricDecisionShadowRows)
		require.Len(t, ms, 1)
		assert.InDelta(t, 1, ms[0].Value, 1e-9)
		assert.Equal(t, "s", ms[0].Labels[decision.AttrDecisionSite])
		assert.NoError(t, r.Record(context.Background(), nil), "empty batch is a no-op")
	})

	t.Run("store failure is counted, returned, never panics", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("disk full")
		store := &memShadowStore{err: boom}
		tracer := newRecordingTracer()
		r := decision.NewShadowRecorder(store, tracer)
		err := r.Record(context.Background(), rows)
		assert.True(t, errors.Is(err, boom))
		assert.Len(t, tracer.Metrics(observability.MetricDecisionShadowErrors), 1)
		assert.Empty(t, tracer.Metrics(observability.MetricDecisionShadowRows))
	})
}
