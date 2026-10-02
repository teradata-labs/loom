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

package index

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/skills"
)

type memShadowStore struct {
	mu   sync.Mutex
	rows []*loomv1.DecisionShadowRecord
}

func (m *memShadowStore) RecordShadow(_ context.Context, r []*loomv1.DecisionShadowRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, r...)
	return nil
}

func (m *memShadowStore) QueryShadow(context.Context, decision.ShadowQuery) ([]*loomv1.DecisionShadowRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*loomv1.DecisionShadowRecord(nil), m.rows...), nil
}

// twoBranchTree: root -> {sql, ml}; sql holds sql-opt, ml holds ml-train.
func twoBranchTree() (*Tree, *fakeResolver) {
	sql := &skills.SkillIndexNode{ID: NodeID("ent/sql"), Title: "SQL", Summary: "query work", Depth: 1, SkillRefs: []string{"sql-opt"}}
	ml := &skills.SkillIndexNode{ID: NodeID("ent/ml"), Title: "ML", Summary: "model work", Depth: 1, SkillRefs: []string{"ml-train"}}
	root := &skills.SkillIndexNode{ID: NodeID(Root), Title: "root", Children: []string{sql.ID, ml.ID}}
	idx := &skills.SkillIndex{ID: "i", RootID: root.ID, Nodes: []*skills.SkillIndexNode{root, sql, ml}}
	res := &fakeResolver{skills: map[string]*skills.Skill{
		"sql-opt":  {Name: "sql-opt", Title: "SQL optimization", Description: "rewrite slow queries"},
		"ml-train": {Name: "ml-train", Title: "Model training", Description: "train a classifier"},
	}}
	return NewTree(idx), res
}

func liveBand() []*loomv1.DecisionBand {
	return []*loomv1.DecisionBand{{
		Site: sites.SiteSkillRoute, ActMin: 0.5, TrueMin: 0.5,
		Aggregate: loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION,
		Mode:      loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE,
	}}
}

// On a live band the walk never calls the LLM: the whole point is to remove
// one generative call per node visited.
func TestRouteLiveBandSkipsTheLLM(t *testing.T) {
	t.Parallel()
	tree, res := twoBranchTree()
	// sortAndCap orders children by Title, so "ML" is o0 and "SQL" is o1.
	// Descend into SQL only. At the SQL leaf the single attached skill is
	// under maxCandidates, so it surfaces without any call at all.
	dec := decisionmock.New().AnswerNoul("o0", 0.02).AnswerNoul("o1", 0.95)
	llm := newScriptedLLM([]string{`{"descend":[],"skills":[],"reason":"should not be called"}`})
	store := &memShadowStore{}

	r := NewRouter(res,
		WithRouterLLM(llm),
		WithRouterDecision(decision.NewRouter(dec, decision.WithBands(liveBand())), store))
	r.SetTree(tree)

	got, err := r.Route(context.Background(), "sess-1", "make this query faster", nil, "h")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "sql-opt", got[0].Name)
	assert.Equal(t, 0, llm.calls, "no generative call on a live band")
	assert.Equal(t, 1, dec.CallCount(), "one decider call for the one branch node")

	r.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 2, "one row per option")
	for _, row := range rows {
		assert.Equal(t, sites.SiteSkillRoute, row.Site)
		assert.Equal(t, "sess-1", row.SessionId)
		assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_DECIDER, row.Path)
		assert.Empty(t, row.ReferenceSource, "nothing to compare against when the decider acted")
		assert.Contains(t, []string{"subtree:" + NodeID("ent/sql"), "subtree:" + NodeID("ent/ml")}, row.Subject,
			"the subject identifies the option without carrying the message")
	}
}

// Below the band the existing walk decides, and the answer already in hand
// is recorded against it rather than asking the decider twice.
func TestRouteBelowBandFallsBackAndRecordsOnce(t *testing.T) {
	t.Parallel()
	tree, res := twoBranchTree()
	// o0 is ML and o1 is SQL (children sort by Title). The decider says SQL,
	// the LLM says ML: the disagreement is what the row exists to capture.
	dec := decisionmock.New().AnswerNoul("o0", 0.02).AnswerNoul("o1", 0.95)
	llm := newScriptedLLM([]string{`{"descend":["` + NodeID("ent/ml") + `"],"skills":[],"reason":"ml"}`})
	store := &memShadowStore{}

	// MIN aggregate with a high floor: the weakest answer decides, so the
	// band is not cleared and the walk falls back.
	band := []*loomv1.DecisionBand{{
		Site: sites.SiteSkillRoute, ActMin: 0.99,
		Aggregate: loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_MIN,
	}}
	r := NewRouter(res,
		WithRouterLLM(llm),
		WithRouterDecision(decision.NewRouter(dec, decision.WithBands(band)), store))
	r.SetTree(tree)

	got, err := r.Route(context.Background(), "sess-2", "train a model", nil, "h")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ml-train", got[0].Name, "the LLM's choice stands")
	assert.Equal(t, 1, llm.calls)
	assert.Equal(t, 1, dec.CallCount(), "one decider call per node visit, never two")

	r.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	byQ := map[string]*loomv1.DecisionShadowRecord{}
	for _, row := range rows {
		byQ[row.QuestionId] = row
		assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, row.Path)
		assert.Equal(t, sites.ReferenceSourceRouterLLM, row.ReferenceSource)
	}
	assert.Equal(t, "true", byQ["o0"].ReferenceAnswer, "the LLM picked ML")
	assert.Equal(t, "false", byQ["o1"].ReferenceAnswer, "the LLM did not pick SQL")
	assert.Equal(t, "false", byQ["o0"].CandidateAnswer, "the decider said no to ML")
	assert.Equal(t, "true", byQ["o1"].CandidateAnswer, "and yes to SQL, which is the row's point")
}

// A shadow band changes nothing about the walk and still produces rows.
func TestRouteShadowBandRecordsWithoutChangingTheWalk(t *testing.T) {
	t.Parallel()
	tree, res := twoBranchTree()
	dec := decisionmock.New().AnswerNoul("o0", 0.9).AnswerNoul("o1", 0.9)
	llm := newScriptedLLM([]string{`{"descend":["` + NodeID("ent/sql") + `"],"skills":[],"reason":"sql"}`})
	store := &memShadowStore{}

	r := NewRouter(res, WithRouterLLM(llm),
		WithRouterDecision(decision.NewRouter(dec), store)) // no bands: shadow
	r.SetTree(tree)

	got, err := r.Route(context.Background(), "sess-3", "tune a query", nil, "h")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "sql-opt", got[0].Name)
	assert.Equal(t, 1, llm.calls, "the shadow band never removes the generative call")

	r.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, row.Path)
		assert.Equal(t, sites.ReferenceSourceRouterLLM, row.ReferenceSource)
	}
}

// A fat leaf is the other generative call in the walk; a live band removes
// it too, and the cap still applies.
func TestRouteFatLeafLiveBandPicksAndCaps(t *testing.T) {
	t.Parallel()
	refs := make([]string, 0, 6)
	skillMap := map[string]*skills.Skill{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		refs = append(refs, "sk-"+n)
		skillMap["sk-"+n] = &skills.Skill{Name: "sk-" + n, Description: "does " + n}
	}
	leaf := &skills.SkillIndexNode{ID: NodeID(Root), Title: "root", SkillRefs: refs}
	idx := &skills.SkillIndex{ID: "i", RootID: leaf.ID, Nodes: []*skills.SkillIndexNode{leaf}}

	dec := decisionmock.New().
		AnswerNoul("o0", 0.10).AnswerNoul("o1", 0.99).AnswerNoul("o2", 0.80).
		AnswerNoul("o3", 0.02).AnswerNoul("o4", 0.90).AnswerNoul("o5", 0.01)
	llm := newScriptedLLM([]string{`["sk-a"]`})
	store := &memShadowStore{}

	r := NewRouter(&fakeResolver{skills: skillMap},
		WithRouterLLM(llm),
		WithRouterDecision(decision.NewRouter(dec, decision.WithBands(liveBand())), store))
	r.SetTree(NewTree(idx))

	got, err := r.Route(context.Background(), "sess-4", "which of these", nil, "h")
	require.NoError(t, err)
	assert.Equal(t, 0, llm.calls, "no fat-leaf generative call on a live band")
	names := make([]string, 0, len(got))
	for _, s := range got {
		names = append(names, s.Name)
	}
	assert.ElementsMatch(t, []string{"sk-b", "sk-e", "sk-c"}, names,
		"the three most relevant, capped at maxCandidates")

	r.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 6)
	assert.Equal(t, "skill:sk-a", rows[0].Subject)
}

// Every answer uncertain is not a routing decision: hand it to the LLM.
func TestRouteAllUncertainFallsBackToTheLLM(t *testing.T) {
	t.Parallel()
	tree, res := twoBranchTree()
	dec := decisionmock.New().AnswerNoul("o0", 0.5).AnswerNoul("o1", 0.5)
	llm := newScriptedLLM([]string{`{"descend":["` + NodeID("ent/ml") + `"],"skills":[],"reason":"ml"}`})
	store := &memShadowStore{}

	r := NewRouter(res, WithRouterLLM(llm),
		WithRouterDecision(decision.NewRouter(dec, decision.WithBands(liveBand())), store))
	r.SetTree(tree)

	got, err := r.Route(context.Background(), "sess-5", "ambiguous", nil, "h")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ml-train", got[0].Name)
	assert.Equal(t, 1, llm.calls, "the walk asked the LLM rather than descending into everything")

	r.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, rows[0].Path)
}

// A decider that errors must never break the walk.
func TestRouteDeciderErrorFallsBack(t *testing.T) {
	t.Parallel()
	tree, res := twoBranchTree()
	dec := decisionmock.New().SetError(errors.New("gateway 503"))
	llm := newScriptedLLM([]string{`{"descend":["` + NodeID("ent/sql") + `"],"skills":[],"reason":"sql"}`})
	store := &memShadowStore{}

	r := NewRouter(res, WithRouterLLM(llm),
		WithRouterDecision(decision.NewRouter(dec, decision.WithBands(liveBand())), store))
	r.SetTree(tree)

	got, err := r.Route(context.Background(), "sess-6", "tune a query", nil, "h")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "sql-opt", got[0].Name)
	assert.Equal(t, 1, llm.calls)

	r.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_ERROR, row.Path)
		assert.Contains(t, row.Error, "503", "the reason is on the row, not just lost")
	}
}

// With no decision layer at all the walk is byte-for-byte what it was.
func TestRouteWithoutDecisionLayerIsUnchanged(t *testing.T) {
	t.Parallel()
	tree, res := twoBranchTree()
	llm := newScriptedLLM([]string{`{"descend":["` + NodeID("ent/sql") + `"],"skills":[],"reason":"sql"}`})

	r := NewRouter(res, WithRouterLLM(llm))
	r.SetTree(tree)

	got, err := r.Route(context.Background(), "sess-7", "tune a query", nil, "h")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "sql-opt", got[0].Name)
	assert.Equal(t, 1, llm.calls)
	r.WaitDecisionShadows() // must not panic with no store
}

// Eligibility filtering is the agent's binding boundary and the decider must
// not widen it.
func TestRouteLiveBandStillHonoursEligibility(t *testing.T) {
	t.Parallel()
	tree, res := twoBranchTree()
	dec := decisionmock.New().AnswerNoul("o0", 0.95).AnswerNoul("o1", 0.95)
	llm := newScriptedLLM([]string{`{"descend":[],"skills":[],"reason":""}`})

	r := NewRouter(res, WithRouterLLM(llm),
		WithRouterDecision(decision.NewRouter(dec, decision.WithBands(liveBand())), &memShadowStore{}))
	r.SetTree(tree)

	got, err := r.Route(context.Background(), "sess-8", "anything", map[string]bool{"ml-train": true}, "h")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ml-train", got[0].Name, "a skill the agent never bound must not surface")
	r.WaitDecisionShadows()
}
