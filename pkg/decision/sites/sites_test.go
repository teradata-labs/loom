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

	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
)

func TestFailureKindRequestStateIsFiltered(t *testing.T) {
	t.Parallel()
	input := map[string]any{"sql": "SELECT ssn FROM customers", "password": "hunter2"}
	req, err := FailureKindRequest("teradata:connect", "MCP_CALL_FAILED", "session_handle_budget_full", input)
	require.NoError(t, err)
	assert.Equal(t, SiteFailureKind, req.Site)

	state := req.State.GetStructValue().AsMap()
	assert.Equal(t, "teradata:connect", state["tool"])
	assert.Equal(t, "MCP_CALL_FAILED", state["error_code"])
	assert.Equal(t, "session_handle_budget_full", state["error_text"])
	assert.Equal(t, InputDigest(input), state["input_digest"])
	assert.Len(t, state, 4, "state carries exactly tool, error_code, error_text, input_digest")
	raw := req.State.String()
	assert.NotContains(t, raw, "hunter2", "input never reaches state")
	assert.NotContains(t, raw, "ssn", "input never reaches state")

	require.Contains(t, req.Questions, QFailureKind)
	require.Contains(t, req.Questions, QRetryHelps)
	opts := req.Questions[QFailureKind].GetChoice().Options
	for _, k := range []string{KindNotAFailure, KindTransient, KindServerSaturated, KindAuth, KindBadInput, KindNotFound, KindOther} {
		assert.Contains(t, opts, k)
	}
	assert.Len(t, opts, 7)
}

func TestFailureKindRequestTruncatesErrorText(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", maxErrorTextRunes+50)
	req, err := FailureKindRequest("t", "", long, nil)
	require.NoError(t, err)
	got := req.State.GetStructValue().Fields["error_text"].GetStringValue()
	assert.Equal(t, maxErrorTextRunes+1, len([]rune(got)), "bounded to maxErrorTextRunes plus the ellipsis")
	assert.True(t, strings.HasSuffix(got, "…"))
	assert.Equal(t, "", req.State.GetStructValue().Fields["input_digest"].GetStringValue(), "nil input digests to empty")
}

func TestFailureKindReference(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		success   bool
		code      string
		text      string
		wantKind  string
		wantRetry string
	}{
		{name: "success", success: true, wantKind: KindNotAFailure, wantRetry: "false"},
		{name: "syntax", text: "Syntax error: expected something between 'SELECT' and 'FROM'", wantKind: KindBadInput, wantRetry: "false"},
		{name: "permission", text: "The user does not have SELECT access to DBC.Users", wantKind: KindAuth, wantRetry: "false"},
		{name: "table not found", text: "Object 'sales.orders' does not exist", wantKind: KindNotFound, wantRetry: "false"},
		{name: "column not found", text: "Column 'foo' not found in table", wantKind: KindNotFound, wantRetry: "false"},
		{name: "timeout", text: "query timeout exceeded", wantKind: KindTransient, wantRetry: "true"},
		{name: "saturated", text: `MCP_CALL_FAILED: {"code":"session_handle_budget_full"}`, wantKind: KindServerSaturated, wantRetry: "false"},
		{name: "rate limited", code: "429", text: "Too Many Requests", wantKind: KindServerSaturated, wantRetry: "false"},
		// Classes added to InferErrorType after the first shadow run.
		{name: "numeric overflow", text: `MCP_CALL_FAILED: tool error: {"code":"db_error","message":"[Error 2616] Numeric overflow occurred during computation."}`, wantKind: KindBadInput, wantRetry: "false"},
		{name: "fk constraint: referenced entity missing", text: "STORE_ERROR: link entity user: FOREIGN KEY constraint failed", wantKind: KindNotFound, wantRetry: "false"},
		{name: "unique constraint", text: "UNIQUE constraint failed: entities.name", wantKind: KindBadInput, wantRetry: "false"},
		{name: "invalid_input builtin", text: "invalid_input: Data type 'text' requires specific query method", wantKind: KindBadInput, wantRetry: "false"},
		{name: "invalid params", code: "INVALID_PARAMS", text: "config parameter is required for create_agent action", wantKind: KindBadInput, wantRetry: "false"},
		{name: "no rows", text: "STORE_ERROR: get old memory: sql: no rows in result set", wantKind: KindNotFound, wantRetry: "false"},
		{name: "unknown session handle", text: `MCP_CALL_FAILED: tool error: {"code":"unknown_session_handle","message":"handle expired"}`, wantKind: KindNotFound, wantRetry: "false"},
		{name: "other", text: "something odd happened", wantKind: KindOther, wantRetry: "false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			refs := FailureKindReference(tt.success, tt.code, tt.text)
			require.Len(t, refs, 2)
			assert.Equal(t, tt.wantKind, refs[QFailureKind].Answer)
			assert.Equal(t, tt.wantRetry, refs[QRetryHelps].Answer)
			assert.Equal(t, ReferenceSourceInferErrorType, refs[QFailureKind].Source)
		})
	}
}

func TestInputDigest(t *testing.T) {
	t.Parallel()
	a := InputDigest(map[string]any{"x": 1, "y": "z"})
	b := InputDigest(map[string]any{"y": "z", "x": 1})
	assert.Equal(t, a, b, "key order does not matter")
	assert.Len(t, a, 16)
	assert.NotEqual(t, a, InputDigest(map[string]any{"x": 2, "y": "z"}))
	assert.Equal(t, "", InputDigest(nil))
	assert.Equal(t, "", InputDigest(map[string]any{}))
	assert.Equal(t, "", InputDigest(map[string]any{"bad": make(chan int)}), "unmarshalable input digests to empty, never panics")
}

func TestFailureKindEndToEndWithMock(t *testing.T) {
	t.Parallel()
	req, err := FailureKindRequest("t", "", "timeout exceeded", map[string]any{"q": 1})
	require.NoError(t, err)
	m := mock.New().
		AnswerChoice(QFailureKind, map[string]float64{KindTransient: 0.9, KindOther: 0.1}).
		AnswerNoul(QRetryHelps, 0.8)
	out := decision.NewRouter(m).Decide(context.Background(), req)
	require.NoError(t, out.Err)
	records := decision.BuildShadowRecords(req, out, "mock", "s", FailureKindReference(false, "", "timeout exceeded"))
	require.Len(t, records, 2)
	for _, r := range records {
		assert.Equal(t, r.CandidateAnswer, r.ReferenceAnswer, "mock agrees with the reference on %s", r.QuestionId)
	}
}

func TestRerankRequestAndReference(t *testing.T) {
	t.Parallel()
	cands := []string{"alpha memory", "beta memory", strings.Repeat("x", maxRerankCandidateRunes+10)}
	req, err := RerankRequest(SiteRecallRerank, strings.Repeat("q", maxRerankQueryRunes+10), cands)
	require.NoError(t, err)
	assert.Equal(t, SiteRecallRerank, req.Site)
	assert.Len(t, req.Questions, 3)
	for i := range cands {
		assert.Contains(t, req.Questions, CandidateQuestionID(i))
	}
	state := req.State.GetStructValue().AsMap()
	assert.Len(t, state["query"].(string), 0+len([]rune(strings.Repeat("q", maxRerankQueryRunes)))+len("…"))
	items := state["candidates"].([]any)
	require.Len(t, items, 3)
	assert.Equal(t, "c0", items[0].(map[string]any)["id"])
	assert.True(t, strings.HasSuffix(items[2].(map[string]any)["text"].(string), "…"), "long candidate truncated")

	_, err = RerankRequest(SiteRecallRerank, "q", nil)
	assert.ErrorIs(t, err, decision.ErrValidation)

	refs := RerankReference(3, []int{0, 2}, ReferenceSourceLLMRerank)
	assert.Equal(t, "true", refs["c0"].Answer)
	assert.Equal(t, "false", refs["c1"].Answer)
	assert.Equal(t, "true", refs["c2"].Answer)
	assert.Equal(t, ReferenceSourceLLMRerank, refs["c1"].Source)
}

func TestRerankCapsCandidates(t *testing.T) {
	t.Parallel()
	many := make([]string, MaxRerankCandidates+5)
	for i := range many {
		many[i] = "c"
	}
	req, err := RerankRequest(SiteToolSearchRerank, "q", many)
	require.NoError(t, err)
	assert.Len(t, req.Questions, MaxRerankCandidates)
	refs := RerankReference(len(many), []int{MaxRerankCandidates + 1}, ReferenceSourceBM25)
	assert.Len(t, refs, MaxRerankCandidates, "references are capped with the request")
}

func TestRerankKeptWithBand(t *testing.T) {
	t.Parallel()
	req, err := RerankRequest(SiteRecallRerank, "q", []string{"a", "b", "c", "d"})
	require.NoError(t, err)
	// c0 relevant+confident, c1 irrelevant+confident, c2 uncertain (0.55 → decisiveness 0.1), c3 relevant but below band.
	m := mock.New().AnswerNoul("c0", 0.95).AnswerNoul("c1", 0.05).AnswerNoul("c2", 0.55).AnswerNoul("c3", 0.7)
	out := decision.NewRouter(m).Decide(context.Background(), req)
	require.NotNil(t, out.Response)

	kept, uncertain := RerankKeptWithBand(out.Response, 4, decision.Band{ActMin: 0.8})
	assert.Equal(t, []int{0, 2, 3}, kept, "confident-irrelevant dropped; uncertain kept")
	assert.Equal(t, 2, uncertain)

	kept, uncertain = RerankKeptWithBand(out.Response, 4, decision.Band{ActMin: 0.0})
	assert.Equal(t, []int{0, 2, 3}, kept, "with no threshold, p≥0.5 decides")
	assert.Equal(t, 0, uncertain)

	kept, uncertain = RerankKeptWithBand(out.Response, 6, decision.Band{ActMin: 0.8})
	assert.Equal(t, []int{0, 2, 3, 4, 5}, kept, "candidates without an answer are kept")
	assert.Equal(t, 4, uncertain)

	kept, uncertain = RerankKeptWithBand(nil, 4, decision.Band{})
	assert.Nil(t, kept)
	assert.Equal(t, 0, uncertain)

	assert.True(t, RerankContributed(4, 2))
	assert.False(t, RerankContributed(4, 4), "all uncertain: nothing to act on")
	assert.False(t, RerankContributed(0, 0))
}

func TestRerankKept(t *testing.T) {
	t.Parallel()
	req, err := RerankRequest(SiteRecallRerank, "q", []string{"a", "b", "c"})
	require.NoError(t, err)
	m := mock.New().AnswerNoul("c0", 0.9).AnswerNoul("c1", 0.2).AnswerNoul("c2", 0.6)
	out := decision.NewRouter(m).Decide(context.Background(), req)
	require.NotNil(t, out.Response)
	assert.Equal(t, []int{0, 2}, RerankKept(out.Response, 3, 0.5))
	assert.Equal(t, []int{0}, RerankKept(out.Response, 3, 0.8))
	assert.Nil(t, RerankKept(nil, 3, 0.5))
}
