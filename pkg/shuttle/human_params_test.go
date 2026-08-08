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
package shuttle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// D-13 acceptance tests (CT-1P): the held call's FULL parameter map travels with
// a stored human request, bounded at 8192 encoded bytes. These drive the two real
// origins — the ask resolver (hook-held approval) and the contact_human tool
// (question) — the bounding helper directly, and the in-memory and SQLite stores'
// round-trip and params-less read. The postgres half of the round-trip lives in
// test/e2e.

// AC1: an ask hold stores Params equal to the held call's whole parameter map,
// including the siblings the Summary digest never shows, with ParamsTruncated
// false for an under-cap call. AC6: Summary is unchanged by the addition — it is
// still the tool name plus the promoted "stmt" digest, and the sibling params
// are carried in Params beside it rather than folded into it.
func TestParams_AskResolver_StoresFullHeldCallParams(t *testing.T) {
	f := newAskFixture(t, "delete_table", "sess-1", "user-1", 2*time.Second, 10*time.Millisecond)

	// "stmt" is promoted into the digest; confirm/dry_run/rows are the siblings
	// the digest omits and only Params can carry.
	params := map[string]interface{}{
		"stmt":    "DELETE FROM users WHERE id=42",
		"confirm": true,
		"dry_run": false,
		"rows":    42,
	}

	done := make(chan *Result, 1)
	go func() {
		res, _ := f.exec.Execute(f.ctx, "delete_table", params)
		done <- res
	}()

	hr := waitForPending(t, f.store)

	assert.Equal(t, params, hr.Params, "the stored request carries every parameter the policy saw")
	assert.False(t, hr.ParamsTruncated, "an under-cap parameter map is stored whole")

	// The Summary digest is the D-1 shape, byte-for-byte: tool name + promoted stmt.
	assert.Equal(t, "delete_table DELETE FROM users WHERE id=42", hr.Summary)
	assert.NotContains(t, hr.Summary, "dry_run", "sibling params live in Params, never folded into Summary")

	// Resolve so the blocked call returns and the driving goroutine exits.
	require.NoError(t, f.store.RespondToRequest(context.Background(), hr.ID, "approved", "", "reviewer@example.com", nil))
	<-done
}

// AC1 (over-cap origin): an ask hold whose parameter map exceeds the bound stores
// the retained subset with ParamsTruncated true, so the flag reaches the store
// from the origin rather than being re-derived downstream.
func TestParams_AskResolver_OverCapCallStoresTruncatedFlag(t *testing.T) {
	f := newAskFixture(t, "bulk_load", "sess-1", "user-1", 2*time.Second, 10*time.Millisecond)

	params := overCapParams()

	done := make(chan *Result, 1)
	go func() {
		res, _ := f.exec.Execute(f.ctx, "bulk_load", params)
		done <- res
	}()

	hr := waitForPending(t, f.store)

	assert.True(t, hr.ParamsTruncated, "the bound cut the payload, and the stored request says so")
	assert.Less(t, len(hr.Params), len(params), "the retained map is a strict subset of the held call's params")
	assertWholePairSubset(t, params, hr.Params)

	require.NoError(t, f.store.RespondToRequest(context.Background(), hr.ID, "approved", "", "reviewer@example.com", nil))
	<-done
}

// AC2: contact_human stores a request with no params and ParamsTruncated false —
// a question has no held call — for every request_type the schema accepts.
func TestParams_ContactHuman_StoresNoParams(t *testing.T) {
	for _, requestType := range []string{"approval", "input", "decision", "review"} {
		t.Run(requestType, func(t *testing.T) {
			store := NewInMemoryHumanRequestStore()
			tool := NewContactHumanTool(ContactHumanConfig{
				Store:        store,
				Timeout:      100 * time.Millisecond,
				PollInterval: 10 * time.Millisecond,
			})

			ctx := context.Background()
			result, err := tool.Execute(ctx, map[string]interface{}{
				"question":     "Should I proceed with dropping the users table?",
				"request_type": requestType,
				"context":      map[string]interface{}{"table": "users"},
			})
			require.NoError(t, err)
			require.NotNil(t, result.Error, "no responder → the wait times out and the request stays pending")

			pending, err := store.ListPending(ctx)
			require.NoError(t, err)
			require.Len(t, pending, 1)
			hr := pending[0]

			assert.Empty(t, hr.Params, "a question carries no held-call params")
			assert.False(t, hr.ParamsTruncated, "nothing was cut because nothing was carried")
			// The model's context payload survives, and the harness stamps the
			// origin discriminator over it: "kind" is the one key chosen to be
			// free of model control (round 3, finding 10).
			assert.Equal(t, map[string]interface{}{"table": "users", "kind": "question"}, hr.Context)
		})
	}
}

// AC3: a map whose JSON encoding fits within the 8192-byte bound is returned
// whole and unflagged; an empty or nil map is likewise returned unchanged.
func TestBoundParams_WithinCap_ReturnsWholeMapUnflagged(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]interface{}
	}{
		{name: "nil map", params: nil},
		{name: "empty map", params: map[string]interface{}{}},
		{
			name:   "small map",
			params: map[string]interface{}{"stmt": "SELECT 1", "limit": 10},
		},
		{
			name: "nested values",
			params: map[string]interface{}{
				"filter": map[string]interface{}{"col": "id", "op": ">"},
				"cols":   []interface{}{"a", "b", "c"},
			},
		},
		{
			// Exercises the boundary from below: encodes to just under 8192 bytes.
			name:   "just under the cap",
			params: map[string]interface{}{"blob": strings.Repeat("x", paramsMaxBytes-64)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated := boundParams(tc.params)

			assert.False(t, truncated, "nothing was cut")
			assert.Equal(t, tc.params, got, "an in-bound map is returned whole")
			if len(tc.params) > 0 {
				encoded, err := json.Marshal(got)
				require.NoError(t, err)
				assert.LessOrEqual(t, len(encoded), paramsMaxBytes)
			}
		})
	}
}

// AC3: a map whose encoding exceeds the bound returns a subset of WHOLE key/value
// pairs — every retained value is byte-identical to its input, the result encodes
// within 8192 bytes and re-unmarshals cleanly (never a cut JSON value) — with the
// flag set.
func TestBoundParams_OverCap_RetainsWholePairsWithinBound(t *testing.T) {
	params := overCapParams()

	got, truncated := boundParams(params)

	assert.True(t, truncated, "the bound cut the payload")
	assert.NotEmpty(t, got, "pairs that fit are retained")
	assert.Less(t, len(got), len(params), "the result is a strict subset")
	assertWholePairSubset(t, params, got)

	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(encoded), paramsMaxBytes, "the retained payload respects the bound")

	var reparsed map[string]interface{}
	require.NoError(t, json.Unmarshal(encoded, &reparsed), "the retained payload is well-formed JSON")
	assert.Len(t, reparsed, len(got))
}

// AC3: key-sorted retention makes the cut deterministic — the same over-cap input
// yields the same retained key set on every call.
func TestBoundParams_OverCap_RetentionIsDeterministic(t *testing.T) {
	params := overCapParams()

	first, firstTruncated := boundParams(params)
	second, secondTruncated := boundParams(params)

	assert.True(t, firstTruncated)
	assert.Equal(t, firstTruncated, secondTruncated)
	assert.Equal(t, first, second, "key-sorted retention yields an identical result for identical input")
}

// AC3: a value that alone exceeds the bound is elided entirely — its key is
// absent rather than holding a partial value — while the pairs that fit are kept
// and the flag reports the cut.
func TestBoundParams_SingleOverCapValue_IsElidedWhole(t *testing.T) {
	t.Run("only an over-cap value", func(t *testing.T) {
		params := map[string]interface{}{
			"blob": strings.Repeat("y", paramsMaxBytes*2),
		}

		got, truncated := boundParams(params)

		assert.True(t, truncated)
		assert.NotContains(t, got, "blob", "an unfittable value is dropped, not partially written")
		assert.Empty(t, got)
	})

	t.Run("an over-cap value beside pairs that fit", func(t *testing.T) {
		params := map[string]interface{}{
			"blob":    strings.Repeat("y", paramsMaxBytes*2),
			"confirm": true,
			"stmt":    "DELETE FROM users",
		}

		got, truncated := boundParams(params)

		assert.True(t, truncated)
		assert.NotContains(t, got, "blob", "the unfittable value is elided")
		assert.Equal(t, map[string]interface{}{"confirm": true, "stmt": "DELETE FROM users"}, got,
			"the pairs that fit are retained whole")
	})
}

// AC5 (in-memory): Params and ParamsTruncated survive Store → Get and
// Store → ListBySession unchanged, for both flag values. AC6: a request built
// with no params reads back with Params empty and the flag false.
func TestParams_InMemoryStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryHumanRequestStore()

	populated := map[string]interface{}{
		"stmt":   "DELETE FROM users WHERE id=42",
		"filter": map[string]interface{}{"col": "id", "op": ">"},
		"cols":   []interface{}{"a", "b"},
		"rows":   42,
	}

	require.NoError(t, store.Store(ctx, &HumanRequest{
		ID:              "mem-truncated",
		SessionID:       "sess-1",
		Status:          "pending",
		ExpiresAt:       time.Now().Add(time.Hour),
		Kind:            "approval",
		Summary:         "delete_table DELETE FROM users WHERE id=42",
		Params:          populated,
		ParamsTruncated: true,
	}))
	require.NoError(t, store.Store(ctx, &HumanRequest{
		ID:              "mem-whole",
		SessionID:       "sess-1",
		Status:          "pending",
		ExpiresAt:       time.Now().Add(time.Hour),
		Kind:            "approval",
		Summary:         "delete_table DELETE FROM users WHERE id=42",
		Params:          populated,
		ParamsTruncated: false,
	}))
	require.NoError(t, store.Store(ctx, &HumanRequest{
		ID:        "mem-no-params",
		SessionID: "sess-1",
		Status:    "pending",
		ExpiresAt: time.Now().Add(time.Hour),
	}))

	t.Run("get round-trips params and flag", func(t *testing.T) {
		truncated, err := store.Get(ctx, "mem-truncated")
		require.NoError(t, err)
		assert.Equal(t, populated, truncated.Params)
		assert.True(t, truncated.ParamsTruncated)

		whole, err := store.Get(ctx, "mem-whole")
		require.NoError(t, err)
		assert.Equal(t, populated, whole.Params)
		assert.False(t, whole.ParamsTruncated)
	})

	t.Run("list by session round-trips params and flag", func(t *testing.T) {
		listed, err := store.ListBySession(ctx, "sess-1")
		require.NoError(t, err)
		byID := indexByID(listed)
		require.Len(t, byID, 3)

		assert.Equal(t, populated, byID["mem-truncated"].Params)
		assert.True(t, byID["mem-truncated"].ParamsTruncated)
		assert.Equal(t, populated, byID["mem-whole"].Params)
		assert.False(t, byID["mem-whole"].ParamsTruncated)
	})

	t.Run("a request with no params reads back empty", func(t *testing.T) {
		got, err := store.Get(ctx, "mem-no-params")
		require.NoError(t, err, "a request carrying no params reads back without error")
		assert.Empty(t, got.Params)
		assert.False(t, got.ParamsTruncated)
	})
}

// AC5 (sqlite): Params and ParamsTruncated survive Store → Get and
// Store → ListBySession through the params_json/params_truncated columns, with
// JSON's value types (objects, arrays, numbers as float64) preserved.
func TestParams_SQLiteStore_RoundTrip(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	now := time.Now()

	// Values as they read back through JSON: numbers are float64, nested objects
	// are map[string]interface{}, arrays are []interface{}.
	populated := map[string]interface{}{
		"stmt":    "DELETE FROM users WHERE id=42",
		"confirm": true,
		"rows":    float64(42),
		"filter":  map[string]interface{}{"col": "id", "op": ">"},
		"cols":    []interface{}{"a", "b"},
	}

	base := func(id string) *HumanRequest {
		return &HumanRequest{
			ID:          id,
			AgentID:     "agent-1",
			SessionID:   "session-1",
			Question:    "Approve tool call \"delete_table\"?",
			RequestType: "approval",
			Priority:    "high",
			Kind:        "approval",
			Summary:     "delete_table DELETE FROM users WHERE id=42",
			Timeout:     5 * time.Minute,
			CreatedAt:   now,
			ExpiresAt:   now.Add(5 * time.Minute),
			Status:      "pending",
		}
	}

	truncatedReq := base("sqlite-truncated")
	truncatedReq.Params = populated
	truncatedReq.ParamsTruncated = true
	require.NoError(t, store.Store(ctx, truncatedReq))

	wholeReq := base("sqlite-whole")
	wholeReq.Params = populated
	wholeReq.ParamsTruncated = false
	require.NoError(t, store.Store(ctx, wholeReq))

	t.Run("get round-trips params and flag", func(t *testing.T) {
		truncated, err := store.Get(ctx, "sqlite-truncated")
		require.NoError(t, err)
		assert.Equal(t, populated, truncated.Params)
		assert.True(t, truncated.ParamsTruncated)

		whole, err := store.Get(ctx, "sqlite-whole")
		require.NoError(t, err)
		assert.Equal(t, populated, whole.Params)
		assert.False(t, whole.ParamsTruncated)
	})

	t.Run("list by session round-trips params and flag", func(t *testing.T) {
		listed, err := store.ListBySession(ctx, "session-1")
		require.NoError(t, err)
		byID := indexByID(listed)
		require.Len(t, byID, 2)

		assert.Equal(t, populated, byID["sqlite-truncated"].Params)
		assert.True(t, byID["sqlite-truncated"].ParamsTruncated)
		assert.Equal(t, populated, byID["sqlite-whole"].Params)
		assert.False(t, byID["sqlite-whole"].ParamsTruncated)
	})

	t.Run("summary and kind are unchanged by the params columns", func(t *testing.T) {
		got, err := store.Get(ctx, "sqlite-truncated")
		require.NoError(t, err)
		assert.Equal(t, "approval", got.Kind)
		assert.Equal(t, "delete_table DELETE FROM users WHERE id=42", got.Summary)
	})
}

// AC6 (sqlite): the addition is additive on the read path. A request stored with
// no params writes params_json NULL, and a row written as it was before the
// columns existed — the params columns omitted from the INSERT, or written
// explicitly NULL — is read by Get and ListBySession without error, with Params
// empty and ParamsTruncated false.
func TestParams_SQLiteStore_ParamsLessRowReadsEmpty(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	now := time.Now()

	// Stored through the store's own writer with no params.
	require.NoError(t, store.Store(ctx, &HumanRequest{
		ID:          "sqlite-store-no-params",
		AgentID:     "agent-1",
		SessionID:   "session-legacy",
		Question:    "Should I proceed?",
		RequestType: "input",
		Priority:    "normal",
		Kind:        "question",
		Summary:     "Should I proceed?",
		Timeout:     5 * time.Minute,
		CreatedAt:   now,
		ExpiresAt:   now.Add(5 * time.Minute),
		Status:      "pending",
	}))

	// A row in the pre-params shape: the INSERT names neither params column, so
	// params_json is NULL and params_truncated takes its false default.
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO human_requests (
			id, agent_id, session_id, question, context_json,
			request_type, priority, timeout_ms, created_at, expires_at,
			status, response, response_data_json, responded_by, kind, summary
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sqlite-legacy-omitted", "agent-1", "session-legacy", "Should I proceed?", "{}",
		"input", "normal", int64(300000), now.UnixMilli(), now.Add(5*time.Minute).UnixMilli(),
		"pending", "", "", "", "question", "Should I proceed?",
	)
	require.NoError(t, err)

	// A row whose params columns are both explicitly NULL — the scan path
	// tolerates a NULL flag rather than erroring on it.
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO human_requests (
			id, agent_id, session_id, question, context_json,
			request_type, priority, timeout_ms, created_at, expires_at,
			status, response, response_data_json, responded_by, kind, summary,
			params_json, params_truncated
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`,
		"sqlite-legacy-null", "agent-1", "session-legacy", "Should I proceed?", "{}",
		"input", "normal", int64(300000), now.UnixMilli(), now.Add(5*time.Minute).UnixMilli(),
		"pending", "", "", "", "question", "Should I proceed?",
	)
	require.NoError(t, err)

	for _, id := range []string{"sqlite-store-no-params", "sqlite-legacy-omitted", "sqlite-legacy-null"} {
		t.Run("get "+id, func(t *testing.T) {
			got, err := store.Get(ctx, id)
			require.NoError(t, err, "a row with no params scans back without error")
			assert.Empty(t, got.Params, "absent params scan to an empty map")
			assert.False(t, got.ParamsTruncated, "absent params_truncated scans to false")
			assert.Equal(t, "Should I proceed?", got.Summary, "Summary is unchanged by the additive columns")
			assert.Equal(t, "question", got.Kind)
		})
	}

	t.Run("list by session", func(t *testing.T) {
		listed, err := store.ListBySession(ctx, "session-legacy")
		require.NoError(t, err)
		byID := indexByID(listed)
		require.Len(t, byID, 3)
		for id, got := range byID {
			assert.Empty(t, got.Params, "%s: absent params scan to an empty map", id)
			assert.False(t, got.ParamsTruncated, "%s: absent params_truncated scans to false", id)
		}
	})
}

// overCapParams builds a parameter map whose JSON encoding comfortably exceeds
// paramsMaxBytes, spread over many equally sized pairs so the bound cuts between
// pairs rather than inside one.
func overCapParams() map[string]interface{} {
	params := make(map[string]interface{}, 64)
	for i := 0; i < 64; i++ {
		params[fmt.Sprintf("k%03d", i)] = strings.Repeat("v", 256)
	}
	return params
}

// assertWholePairSubset asserts every pair in got appears in want with an
// identical value — the retained map holds whole pairs, never a cut value.
func assertWholePairSubset(t *testing.T, want, got map[string]interface{}) {
	t.Helper()
	for k, v := range got {
		original, ok := want[k]
		assert.True(t, ok, "retained key %q comes from the input", k)
		assert.Equal(t, original, v, "retained value for %q is whole", k)
	}
}

// indexByID keys a request list by request ID for order-independent assertions.
func indexByID(requests []*HumanRequest) map[string]*HumanRequest {
	byID := make(map[string]*HumanRequest, len(requests))
	for _, r := range requests {
		byID[r.ID] = r
	}
	return byID
}
