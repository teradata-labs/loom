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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// D-1 acceptance tests (CT-1): a HumanRequest self-describes with a Kind and a
// display Summary set at ORIGIN. These drive the two real origins — the ask
// resolver (hook-held approval) and the contact_human tool (question) — and the
// additive-read contract that a request carrying no Kind round-trips as an empty
// (question) kind without error. The postgres half of the round-trip lives in
// test/e2e; here the origins and the in-memory/sqlite stores are exercised.

// AC1: an ask decision stamps the raised approval request with Kind="approval"
// and a non-empty Summary that names the held tool and digests its args. The
// summary is set by the resolver at origin; the request is read back from the
// store while the resolver blocks awaiting a response.
func TestSelfDescribe_AskResolver_StampsApprovalKindAndSummary(t *testing.T) {
	cases := []struct {
		name       string
		toolName   string
		params     map[string]interface{}
		wantDigest string // fragment the args digest must contain beyond the tool name
	}{
		{
			name:       "stmt param is the digest",
			toolName:   "delete_table",
			params:     map[string]interface{}{"stmt": "DELETE FROM users WHERE id=42"},
			wantDigest: "DELETE FROM users WHERE id=42",
		},
		{
			name:       "non-stmt params digest as key=value",
			toolName:   "drop_index",
			params:     map[string]interface{}{"target": "users"},
			wantDigest: "target=users",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAskFixture(t, tc.toolName, "sess-1", "user-1", 2*time.Second, 10*time.Millisecond)

			// Drive the gated call; it blocks in the resolver until the request resolves.
			done := make(chan *Result, 1)
			go func() {
				res, _ := f.exec.Execute(f.ctx, tc.toolName, tc.params)
				done <- res
			}()

			// While the resolver blocks, the raised request self-describes as an approval.
			hr := waitForPending(t, f.store)
			assert.Equal(t, "approval", hr.Kind, "a hook-held request is stamped Kind=approval at origin")
			require.NotEmpty(t, hr.Summary, "an approval carries a non-empty display summary")
			assert.Contains(t, hr.Summary, tc.toolName, "the summary names the held tool")
			assert.Contains(t, hr.Summary, tc.wantDigest, "the summary carries the args digest")

			// Resolve so the blocked call returns and the driving goroutine exits.
			require.NoError(t, f.store.RespondToRequest(context.Background(), hr.ID, "approved", "", "reviewer@example.com", nil))
			<-done
		})
	}
}

// AC2: contact_human stamps every raised request with Kind="question" and a
// Summary equal to the question text, independent of request_type. The request
// is read back from the store after the tool's wait times out (it remains
// pending), so no responder is needed.
func TestSelfDescribe_ContactHuman_StampsQuestionKindAndSummary(t *testing.T) {
	const question = "Should I proceed with dropping the users table?"

	// The stamp is independent of request_type; assert across every schema value.
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
				"question":     question,
				"request_type": requestType,
			})
			require.NoError(t, err)
			require.NotNil(t, result.Error, "no responder → the wait times out and the request stays pending")
			assert.Equal(t, "TIMEOUT", result.Error.Code)

			// The stored (still-pending) request self-describes as a question.
			pending, err := store.ListPending(ctx)
			require.NoError(t, err)
			require.Len(t, pending, 1)
			hr := pending[0]
			assert.Equal(t, "question", hr.Kind, "contact_human stamps Kind=question regardless of request_type")
			assert.Equal(t, question, hr.Summary, "the question text is the summary")
			assert.Equal(t, requestType, hr.RequestType, "request_type is carried through unchanged")
		})
	}
}

// AC4 (in-memory): the additions are additive. A request built with no Kind
// round-trips through the in-memory store as an empty Kind (treated as a
// question by consumers) with no error, while a request that carries Kind and
// Summary round-trips them intact.
func TestSelfDescribe_InMemoryStore_AdditiveKindSummaryRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryHumanRequestStore()

	t.Run("absent kind reads back empty", func(t *testing.T) {
		require.NoError(t, store.Store(ctx, &HumanRequest{ID: "no-kind", Status: "pending", ExpiresAt: time.Now().Add(time.Hour)}))

		got, err := store.Get(ctx, "no-kind")
		require.NoError(t, err, "a request with no Kind reads back without error")
		assert.Empty(t, got.Kind, "absent Kind stays empty (consumers treat empty as question)")
		assert.Empty(t, got.Summary, "absent Summary stays empty")
	})

	t.Run("kind and summary round-trip", func(t *testing.T) {
		require.NoError(t, store.Store(ctx, &HumanRequest{
			ID:        "with-kind",
			Status:    "pending",
			ExpiresAt: time.Now().Add(time.Hour),
			Kind:      "approval",
			Summary:   "delete_table DELETE FROM users",
		}))

		got, err := store.Get(ctx, "with-kind")
		require.NoError(t, err)
		assert.Equal(t, "approval", got.Kind)
		assert.Equal(t, "delete_table DELETE FROM users", got.Summary)
	})
}

// AC4 (sqlite): the same additive round-trip holds through the SQLite store,
// whose NULL kind/summary columns scan back to empty strings without error.
func TestSelfDescribe_SQLiteStore_AdditiveKindSummaryRoundTrip(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	now := time.Now()

	base := func(id string) *HumanRequest {
		return &HumanRequest{
			ID:          id,
			AgentID:     "agent-1",
			SessionID:   "session-1",
			Question:    "Should I proceed?",
			RequestType: "approval",
			Priority:    "high",
			Timeout:     5 * time.Minute,
			CreatedAt:   now,
			ExpiresAt:   now.Add(5 * time.Minute),
			Status:      "pending",
		}
	}

	t.Run("absent kind scans back empty", func(t *testing.T) {
		req := base("sqlite-no-kind") // Kind/Summary left zero → stored as NULL columns
		require.NoError(t, store.Store(ctx, req))

		got, err := store.Get(ctx, "sqlite-no-kind")
		require.NoError(t, err, "a NULL kind column scans back without error")
		assert.Empty(t, got.Kind, "NULL kind scans to empty (treated as question)")
		assert.Empty(t, got.Summary, "NULL summary scans to empty")
	})

	t.Run("kind and summary round-trip", func(t *testing.T) {
		req := base("sqlite-with-kind")
		req.Kind = "approval"
		req.Summary = "delete_table DELETE FROM users"
		require.NoError(t, store.Store(ctx, req))

		got, err := store.Get(ctx, "sqlite-with-kind")
		require.NoError(t, err)
		assert.Equal(t, "approval", got.Kind)
		assert.Equal(t, "delete_table DELETE FROM users", got.Summary)
	})
}
