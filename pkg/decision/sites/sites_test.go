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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/shuttle"
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

// TestFailureKindReferenceAgreesWithLadder pins the reference to the shipped
// ladder: for every failed result, the reference kind is exactly the kind
// fabric.InferErrorType's answer maps to, with no saturation rule of its own.
// The first three rows hold 429 inside a longer number; the ladder calls them
// unknown, and a reference that called them server_saturated would count a
// disagreement against the decider that the ladder never made.
func TestFailureKindReferenceAgreesWithLadder(t *testing.T) {
	t.Parallel()
	ladderKind := map[string]string{
		fabric.ErrorTypeSyntax:         KindBadInput,
		fabric.ErrorTypeOverflow:       KindBadInput,
		fabric.ErrorTypeConstraint:     KindBadInput,
		fabric.ErrorTypeInvalidInput:   KindBadInput,
		fabric.ErrorTypePermission:     KindAuth,
		fabric.ErrorTypeColumnNotFound: KindNotFound,
		fabric.ErrorTypeTableNotFound:  KindNotFound,
		fabric.ErrorTypeNotFound:       KindNotFound,
		fabric.ErrorTypeTimeout:        KindTransient,
		fabric.ErrorTypeSaturated:      KindServerSaturated,
		fabric.ErrorTypeUnknown:        KindOther,
	}
	tests := []struct {
		name       string
		code       string
		text       string
		wantLadder string
		wantKind   string
	}{
		// 429 inside a longer number is not an HTTP status.
		{name: "session id 4291", text: "[Session 4291] connection reset by peer", wantLadder: fabric.ErrorTypeUnknown, wantKind: KindOther},
		{name: "bare S4291", text: "S4291", wantLadder: fabric.ErrorTypeUnknown, wantKind: KindOther},
		{name: "row count 14290", text: "inserted 14290 rows then lost connection", wantLadder: fabric.ErrorTypeUnknown, wantKind: KindOther},
		// Words the reference used to match that the ladder does not.
		{name: "capacity alone", text: "insufficient capacity in pool", wantLadder: fabric.ErrorTypeUnknown, wantKind: KindOther},
		{name: "quota alone", text: "quota reached for project", wantLadder: fabric.ErrorTypeUnknown, wantKind: KindOther},
		{name: "too many open files", text: "too many open files", wantLadder: fabric.ErrorTypeUnknown, wantKind: KindOther},
		// A whole-number 429, in the code or in the message.
		{name: "429 code", code: "429", text: "Too Many Requests", wantLadder: fabric.ErrorTypeSaturated, wantKind: KindServerSaturated},
		{name: "HTTP 429 in text", text: "upstream returned HTTP 429", wantLadder: fabric.ErrorTypeSaturated, wantKind: KindServerSaturated},
		{name: "429 at start", text: "429: slow down", wantLadder: fabric.ErrorTypeSaturated, wantKind: KindServerSaturated},
		// Each of fabric's saturation markers, in text the original ladder leaves unknown.
		{name: "budget_full", text: `MCP_CALL_FAILED: {"code":"session_handle_budget_full"}`, wantLadder: fabric.ErrorTypeSaturated, wantKind: KindServerSaturated},
		{name: "rate limit", text: "rate limit hit, slow down", wantLadder: fabric.ErrorTypeSaturated, wantKind: KindServerSaturated},
		{name: "rate_limit code", code: "rate_limit", text: "try later", wantLadder: fabric.ErrorTypeSaturated, wantKind: KindServerSaturated},
		{name: "too many requests", text: "Too Many Requests", wantLadder: fabric.ErrorTypeSaturated, wantKind: KindServerSaturated},
		{name: "too many connections", text: "too many connections", wantLadder: fabric.ErrorTypeSaturated, wantKind: KindServerSaturated},
		{name: "overloaded", text: "server overloaded", wantLadder: fabric.ErrorTypeSaturated, wantKind: KindServerSaturated},
		{name: "server_busy", text: "server_busy: retry later", wantLadder: fabric.ErrorTypeSaturated, wantKind: KindServerSaturated},
		{name: "backpressure", text: "backpressure applied", wantLadder: fabric.ErrorTypeSaturated, wantKind: KindServerSaturated},
		{name: "throttled", text: "request throttled", wantLadder: fabric.ErrorTypeSaturated, wantKind: KindServerSaturated},
		// The original ladder's "exceeded" rule runs first and its answer is
		// final, so these saturation messages are timeout to the ladder and
		// transient to the reference. The reference follows the ladder.
		{name: "rate limit exceeded", text: "rate limit exceeded", wantLadder: fabric.ErrorTypeTimeout, wantKind: KindTransient},
		{name: "quota exceeded", text: "quota exceeded", wantLadder: fabric.ErrorTypeTimeout, wantKind: KindTransient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ladder := fabric.InferErrorType(tt.code, tt.text)
			require.Equal(t, tt.wantLadder, ladder, "ladder answer")
			want, ok := ladderKind[ladder]
			require.True(t, ok, "ladder type %q has a kind", ladder)
			require.Equal(t, tt.wantKind, want)
			refs := FailureKindReference(false, tt.code, tt.text)
			assert.Equal(t, want, refs[QFailureKind].Answer, "reference kind must equal the ladder's kind")
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

func TestToolOutcomeFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		execErr     error
		result      *shuttle.Result
		wantSuccess bool
		wantCode    string
		wantText    string
	}{
		{name: "nil result, no error", wantSuccess: true},
		{name: "success", result: &shuttle.Result{Success: true}, wantSuccess: true},
		{name: "exec error wins", execErr: errors.New("boom"), result: &shuttle.Result{Error: &shuttle.Error{Code: "C", Message: "m"}}, wantText: "boom"},
		{name: "result error", result: &shuttle.Result{Error: &shuttle.Error{Code: "C", Message: "m"}}, wantCode: "C", wantText: "m"},
		{name: "failed without details", result: &shuttle.Result{}, wantText: NoErrorDetailsText},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, c, x := ToolOutcomeOf(tt.execErr, tt.result).Fields()
			assert.Equal(t, tt.wantSuccess, s)
			assert.Equal(t, tt.wantCode, c)
			assert.Equal(t, tt.wantText, x)
		})
	}
}
