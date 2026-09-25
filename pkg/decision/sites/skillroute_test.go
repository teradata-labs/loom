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

package sites

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
)

func routeOpts() []RouteOption {
	return []RouteOption{
		{Kind: OptionKindSubtree, ID: "ent/sql", Title: "SQL", Text: "query authoring and tuning"},
		{Kind: OptionKindSubtree, ID: "ent/ml", Title: "ML", Text: "model training"},
		{Kind: OptionKindSkill, ID: "sql-optimization", Title: "SQL optimization", Text: "rewrite slow queries"},
		{Kind: OptionKindSkill, ID: "csv-import", Title: "CSV import", Text: "load delimited files"},
	}
}

func TestSkillRouteRequestShape(t *testing.T) {
	t.Parallel()
	opts := routeOpts()
	req, err := SkillRouteRequest(strings.Repeat("m", maxRouteMessageRunes+50), "Enterprise", "top of the index", opts)
	require.NoError(t, err)
	require.NoError(t, decision.Validate(req))

	assert.Equal(t, SiteSkillRoute, req.Site)
	assert.Equal(t, SkillRouteFanOutKey, req.FanOutKey, "options travel only with their own chunk")
	require.Len(t, req.Questions, len(opts))
	for i := range opts {
		q, ok := req.Questions[RouteOptionQuestionID(i)]
		require.True(t, ok, "question for option %d", i)
		_, isNoul := q.Kind.(*loomv1.DecisionQuestion_Noul)
		assert.True(t, isNoul)
	}

	state := req.GetState().GetStructValue().AsMap()
	msg, _ := state["message"].(string)
	assert.LessOrEqual(t, len([]rune(msg)), maxRouteMessageRunes+1, "message is truncated, not unbounded")
	items, _ := state[SkillRouteFanOutKey].([]any)
	require.Len(t, items, len(opts))
	first, _ := items[0].(map[string]any)
	assert.Equal(t, "o0", first["id"], "question ids are positional, never the skill name")
	assert.Equal(t, OptionKindSubtree, first["kind"])
	assert.Equal(t, "SQL", first["title"])

	// A skill name never leaks into a question id, so a name with odd
	// characters cannot break request validation.
	weird := []RouteOption{{Kind: OptionKindSkill, ID: "weird name/with:chars", Title: "x"}}
	req, err = SkillRouteRequest("m", "n", "", weird)
	require.NoError(t, err)
	require.NoError(t, decision.Validate(req))

	_, err = SkillRouteRequest("m", "n", "", nil)
	require.Error(t, err, "no options is a validation error, not an empty request")
}

// The asymmetry is the design: never prune a subtree on an uncertain answer,
// never select a skill on one.
func TestSkillRouteSelectedAsymmetry(t *testing.T) {
	t.Parallel()
	opts := routeOpts()
	band := decision.Band{ActMin: 0.5, TrueMin: 0.5,
		Aggregate: loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION}

	// o0 subtree confident-no, o1 subtree uncertain, o2 skill confident-yes,
	// o3 skill uncertain.
	m := mock.New().
		AnswerNoul("o0", 0.05).
		AnswerNoul("o1", 0.45).
		AnswerNoul("o2", 0.95).
		AnswerNoul("o3", 0.45)
	req, err := SkillRouteRequest("tune this query", "Enterprise", "", opts)
	require.NoError(t, err)
	out := decision.NewRouter(m).Decide(context.Background(), req)
	require.NoError(t, out.Err)

	sel := SkillRouteSelected(out.Response, opts, band, 3)
	assert.Equal(t, []string{"ent/ml"}, sel.Descend,
		"a confident no prunes; an uncertain subtree is still walked")
	assert.Equal(t, []string{"sql-optimization"}, sel.Skills,
		"only a confident yes selects a skill")
	assert.Equal(t, 2, sel.Uncertain)
	assert.Equal(t, 4, sel.Answered)
	assert.True(t, SkillRouteContributed(sel))
}

func TestSkillRouteSelectedRanksAndCaps(t *testing.T) {
	t.Parallel()
	opts := []RouteOption{
		{Kind: OptionKindSkill, ID: "third", Title: "c"},
		{Kind: OptionKindSkill, ID: "first", Title: "a"},
		{Kind: OptionKindSkill, ID: "second", Title: "b"},
		{Kind: OptionKindSkill, ID: "dropped", Title: "d"},
	}
	m := mock.New().
		AnswerNoul("o0", 0.80).
		AnswerNoul("o1", 0.99).
		AnswerNoul("o2", 0.90).
		AnswerNoul("o3", 0.85)
	req, err := SkillRouteRequest("m", "n", "", opts)
	require.NoError(t, err)
	out := decision.NewRouter(m).Decide(context.Background(), req)
	require.NoError(t, out.Err)

	band := decision.Band{ActMin: 0.5, TrueMin: 0.5,
		Aggregate: loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION}
	sel := SkillRouteSelected(out.Response, opts, band, 3)
	assert.Equal(t, []string{"first", "second", "dropped"}, sel.Skills,
		"most relevant first, capped at the caller's limit")

	sel = SkillRouteSelected(out.Response, opts, band, 0)
	assert.Len(t, sel.Skills, 4, "0 means no cap")
}

// true_min is the lever that widens what counts as relevant, exactly as at
// the rerank sites.
func TestSkillRouteSelectedHonoursTrueMin(t *testing.T) {
	t.Parallel()
	opts := []RouteOption{{Kind: OptionKindSkill, ID: "marginal", Title: "m"}}
	m := mock.New().AnswerNoul("o0", 0.2) // confident (decisiveness 0.6) but low
	req, err := SkillRouteRequest("m", "n", "", opts)
	require.NoError(t, err)
	out := decision.NewRouter(m).Decide(context.Background(), req)
	require.NoError(t, out.Err)

	perQ := loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION
	strict := SkillRouteSelected(out.Response, opts, decision.Band{ActMin: 0.5, Aggregate: perQ}, 3)
	assert.Empty(t, strict.Skills, "at the default 0.5 cut a 0.2 answer is a no")

	loose := SkillRouteSelected(out.Response, opts, decision.Band{ActMin: 0.5, TrueMin: 0.1, Aggregate: perQ}, 3)
	assert.Equal(t, []string{"marginal"}, loose.Skills, "a lower cut takes it")
}

// "Descend into everything and select nothing" is not a routing decision, so
// the caller must be told to run the LLM instead.
func TestSkillRouteContributed(t *testing.T) {
	t.Parallel()
	opts := routeOpts()
	m := mock.New().
		AnswerNoul("o0", 0.5).AnswerNoul("o1", 0.5).
		AnswerNoul("o2", 0.5).AnswerNoul("o3", 0.5)
	req, err := SkillRouteRequest("m", "n", "", opts)
	require.NoError(t, err)
	out := decision.NewRouter(m).Decide(context.Background(), req)
	require.NoError(t, out.Err)

	band := decision.Band{ActMin: 0.5, Aggregate: loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION}
	sel := SkillRouteSelected(out.Response, opts, band, 3)
	assert.Equal(t, 4, sel.Uncertain)
	assert.False(t, SkillRouteContributed(sel), "every answer uncertain: hand it to the LLM")
	assert.Len(t, sel.Descend, 2, "but the subtrees were still not pruned")

	assert.False(t, SkillRouteContributed(RouteSelection{}), "no answers at all is not a decision")
}

// A missing answer must behave like an uncertain one, not like a no.
func TestSkillRouteSelectedMissingAnswerIsUncertain(t *testing.T) {
	t.Parallel()
	opts := routeOpts()
	resp := &loomv1.DecisionResponse{Answers: map[string]*loomv1.DecisionAnswer{}}
	band := decision.Band{ActMin: 0.5, Aggregate: loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION}
	sel := SkillRouteSelected(resp, opts, band, 3)
	assert.Equal(t, []string{"ent/sql", "ent/ml"}, sel.Descend, "unanswered subtrees are still walked")
	assert.Empty(t, sel.Skills, "unanswered skills are not selected")
	assert.Equal(t, 0, sel.Answered)
	assert.Equal(t, 4, sel.Uncertain)
	assert.Nil(t, SkillRouteSelected(nil, opts, band, 3).Descend)
}

func TestSkillRouteReferenceAndSubjects(t *testing.T) {
	t.Parallel()
	opts := routeOpts()
	subjects := SkillRouteSubjects(opts)
	assert.Equal(t, []string{"subtree:ent/sql", "subtree:ent/ml", "skill:sql-optimization", "skill:csv-import"}, subjects)

	refs := SkillRouteReference(opts, []string{"ent/sql", "sql-optimization"}, ReferenceSourceRouterLLM, subjects)
	require.Len(t, refs, 4)
	assert.Equal(t, "true", refs["o0"].Answer)
	assert.Equal(t, "false", refs["o1"].Answer)
	assert.Equal(t, "true", refs["o2"].Answer)
	assert.Equal(t, "false", refs["o3"].Answer)
	assert.Equal(t, ReferenceSourceRouterLLM, refs["o0"].Source)
	assert.Equal(t, "skill:sql-optimization", refs["o2"].Subject, "a row can be graded without the message")
}
