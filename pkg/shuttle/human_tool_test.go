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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AC1: RespondToRequest is a member of the HumanRequestStore interface, and the
// in-memory store satisfies that interface at build time. This assignment fails
// to compile if the method is dropped from the interface or from the store.
var _ HumanRequestStore = (*InMemoryHumanRequestStore)(nil)

func TestContactHumanTool_Metadata(t *testing.T) {
	tool := NewContactHumanTool(ContactHumanConfig{})

	t.Run("Name", func(t *testing.T) {
		assert.Equal(t, "contact_human", tool.Name())
	})

	t.Run("Description", func(t *testing.T) {
		desc := tool.Description()
		assert.NotEmpty(t, desc)
		assert.Contains(t, desc, "human")
		assert.Contains(t, desc, "approval")
	})

	t.Run("Backend", func(t *testing.T) {
		assert.Equal(t, "", tool.Backend())
	})

	t.Run("InputSchema", func(t *testing.T) {
		schema := tool.InputSchema()
		require.NotNil(t, schema)
		assert.Equal(t, "object", schema.Type)

		// Check required fields
		assert.Contains(t, schema.Required, "question")

		// Check properties
		assert.NotNil(t, schema.Properties["question"])
		assert.NotNil(t, schema.Properties["request_type"])
		assert.NotNil(t, schema.Properties["priority"])
		assert.NotNil(t, schema.Properties["context"])
		assert.NotNil(t, schema.Properties["timeout_seconds"])

		// Check enums
		assert.NotEmpty(t, schema.Properties["request_type"].Enum)
		assert.NotEmpty(t, schema.Properties["priority"].Enum)
	})
}

func TestContactHumanTool_Execute_RequiredParams(t *testing.T) {
	tool := NewContactHumanTool(ContactHumanConfig{})
	ctx := context.Background()

	t.Run("MissingQuestion", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]interface{}{})
		require.NoError(t, err)
		assert.False(t, result.Success)
		require.NotNil(t, result.Error)
		assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
		assert.Contains(t, result.Error.Message, "question")
	})

	t.Run("EmptyQuestion", func(t *testing.T) {
		result, err := tool.Execute(ctx, map[string]interface{}{
			"question": "",
		})
		require.NoError(t, err)
		assert.False(t, result.Success)
		require.NotNil(t, result.Error)
		assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
	})
}

func TestContactHumanTool_Execute_Timeout(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	tool := NewContactHumanTool(ContactHumanConfig{
		Store:        store,
		Timeout:      100 * time.Millisecond,
		PollInterval: 10 * time.Millisecond,
	})

	ctx := context.Background()
	params := map[string]interface{}{
		"question":        "Should I proceed?",
		"timeout_seconds": float64(0.1), // 100ms
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.False(t, result.Success)
	require.NotNil(t, result.Error)
	assert.Equal(t, "TIMEOUT", result.Error.Code)
	assert.Contains(t, result.Error.Message, "did not respond")
	assert.True(t, result.Error.Retryable)

	// The request was stored, and the give-up exit closed it terminally: an
	// unanswered question must never sit answerable for a call that already
	// returned.
	rows, err := store.ListBySession(ctx, "")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "timeout", rows[0].Status)
	assert.Equal(t, "system:expiry", rows[0].RespondedBy)
}

func TestContactHumanTool_Execute_WithResponse(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	tool := NewContactHumanTool(ContactHumanConfig{
		Store:        store,
		Timeout:      2 * time.Second,
		PollInterval: 50 * time.Millisecond,
	})

	ctx := context.Background()
	params := map[string]interface{}{
		"question":     "Should I delete this table?",
		"request_type": "approval",
		"priority":     "high",
		"context": map[string]interface{}{
			"table_name": "users",
			"row_count":  1000000,
		},
	}

	// Simulate human response in background
	go func() {
		time.Sleep(200 * time.Millisecond)

		// Get the pending request
		pending, err := store.ListPending(ctx)
		if err != nil || len(pending) == 0 {
			return
		}

		// Respond to it
		err = store.RespondToRequest(ctx, pending[0].ID, "approved", "Yes, proceed with deletion", "alice@example.com", map[string]interface{}{
			"confirmed": true,
		})
		if err != nil {
			t.Logf("Failed to respond: %v", err)
		}
	}()

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Nil(t, result.Error)

	// Check response data
	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "approved", data["status"])
	assert.Equal(t, "Yes, proceed with deletion", data["response"])
	assert.Equal(t, "alice@example.com", data["responded_by"])

	responseData, ok := data["response_data"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, responseData["confirmed"])

	// Check metadata
	assert.Equal(t, "approval", result.Metadata["request_type"])
	assert.Equal(t, "high", result.Metadata["priority"])
}

func TestContactHumanTool_Execute_AllRequestTypes(t *testing.T) {
	requestTypes := []string{"approval", "decision", "input", "review"}

	for _, reqType := range requestTypes {
		t.Run(reqType, func(t *testing.T) {
			store := NewInMemoryHumanRequestStore()
			tool := NewContactHumanTool(ContactHumanConfig{
				Store:        store,
				Timeout:      2 * time.Second,
				PollInterval: 50 * time.Millisecond,
			})

			ctx := context.Background()
			params := map[string]interface{}{
				"question":     "Test question for " + reqType,
				"request_type": reqType,
			}

			// Respond in background
			go func() {
				time.Sleep(100 * time.Millisecond)
				pending, _ := store.ListPending(ctx)
				if len(pending) > 0 {
					_ = store.RespondToRequest(ctx, pending[0].ID, "responded", "Test response", "test@example.com", nil)
				}
			}()

			result, err := tool.Execute(ctx, params)
			require.NoError(t, err)
			assert.True(t, result.Success)
		})
	}
}

func TestContactHumanTool_Execute_AllPriorities(t *testing.T) {
	priorities := []string{"low", "normal", "high", "critical"}

	for _, priority := range priorities {
		t.Run(priority, func(t *testing.T) {
			store := NewInMemoryHumanRequestStore()
			tool := NewContactHumanTool(ContactHumanConfig{
				Store:        store,
				Timeout:      2 * time.Second,
				PollInterval: 50 * time.Millisecond,
			})

			ctx := context.Background()
			params := map[string]interface{}{
				"question": "Test question with " + priority + " priority",
				"priority": priority,
			}

			// Respond in background
			go func() {
				time.Sleep(100 * time.Millisecond)
				pending, _ := store.ListPending(ctx)
				if len(pending) > 0 {
					_ = store.RespondToRequest(ctx, pending[0].ID, "responded", "Test response", "test@example.com", nil)
				}
			}()

			result, err := tool.Execute(ctx, params)
			require.NoError(t, err)
			assert.True(t, result.Success)
			assert.Equal(t, priority, result.Metadata["priority"])
		})
	}
}

func TestInMemoryHumanRequestStore(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryHumanRequestStore()

	t.Run("Store_and_Get", func(t *testing.T) {
		now := time.Now()
		req := &HumanRequest{
			ID:          "test-123",
			AgentID:     "agent-1",
			SessionID:   "session-1",
			Question:    "Test question",
			RequestType: "approval",
			Priority:    "high",
			Timeout:     5 * time.Minute,
			CreatedAt:   now,
			ExpiresAt:   now.Add(5 * time.Minute),
			Status:      "pending",
			Context: map[string]interface{}{
				"key": "value",
			},
		}

		err := store.Store(ctx, req)
		require.NoError(t, err)

		retrieved, err := store.Get(ctx, "test-123")
		require.NoError(t, err)
		assert.Equal(t, req.ID, retrieved.ID)
		assert.Equal(t, req.Question, retrieved.Question)
		assert.Equal(t, req.RequestType, retrieved.RequestType)
		assert.Equal(t, req.Priority, retrieved.Priority)
		assert.Equal(t, "value", retrieved.Context["key"])
	})

	t.Run("Get_NotFound", func(t *testing.T) {
		_, err := store.Get(ctx, "nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("Update", func(t *testing.T) {
		req := &HumanRequest{
			ID:        "test-update",
			Status:    "pending",
			ExpiresAt: time.Now().Add(time.Hour),
			Question:  "Original question",
		}

		err := store.Store(ctx, req)
		require.NoError(t, err)

		// Update the request
		req.Status = "approved"
		req.Response = "Yes"
		err = store.Update(ctx, req)
		require.NoError(t, err)

		// Verify update
		retrieved, err := store.Get(ctx, "test-update")
		require.NoError(t, err)
		assert.Equal(t, "approved", retrieved.Status)
		assert.Equal(t, "Yes", retrieved.Response)
	})

	t.Run("ListPending", func(t *testing.T) {
		// Clear store for this test
		store = NewInMemoryHumanRequestStore()

		// Add pending and non-pending requests
		require.NoError(t, store.Store(ctx, &HumanRequest{ID: "pending-1", Status: "pending", ExpiresAt: time.Now().Add(time.Hour)}))
		require.NoError(t, store.Store(ctx, &HumanRequest{ID: "pending-2", Status: "pending", ExpiresAt: time.Now().Add(time.Hour)}))
		require.NoError(t, store.Store(ctx, &HumanRequest{ID: "approved-1", Status: "approved"}))

		pending, err := store.ListPending(ctx)
		require.NoError(t, err)
		assert.Len(t, pending, 2)

		// Verify all are pending
		for _, req := range pending {
			assert.Equal(t, "pending", req.Status)
		}
	})

	t.Run("ListBySession", func(t *testing.T) {
		// Clear store
		store = NewInMemoryHumanRequestStore()

		// Add requests for different sessions
		require.NoError(t, store.Store(ctx, &HumanRequest{ID: "req-1", SessionID: "session-1"}))
		require.NoError(t, store.Store(ctx, &HumanRequest{ID: "req-2", SessionID: "session-1"}))
		require.NoError(t, store.Store(ctx, &HumanRequest{ID: "req-3", SessionID: "session-2"}))

		session1Reqs, err := store.ListBySession(ctx, "session-1")
		require.NoError(t, err)
		assert.Len(t, session1Reqs, 2)

		session2Reqs, err := store.ListBySession(ctx, "session-2")
		require.NoError(t, err)
		assert.Len(t, session2Reqs, 1)
	})

	// AC2: resolving a pending, non-expired request stamps
	// status/responded_by/responded_at/response exactly once.
	t.Run("RespondToRequest", func(t *testing.T) {
		store = NewInMemoryHumanRequestStore()

		now := time.Now()
		req := &HumanRequest{
			ID:        "respond-test",
			Status:    "pending",
			ExpiresAt: now.Add(5 * time.Minute),
		}
		err := store.Store(ctx, req)
		require.NoError(t, err)

		// Respond to the request
		err = store.RespondToRequest(ctx, "respond-test", "approved", "Yes, proceed", "bob@example.com", map[string]interface{}{
			"reason": "Looks good",
		})
		require.NoError(t, err)

		// Verify response
		retrieved, err := store.Get(ctx, "respond-test")
		require.NoError(t, err)
		assert.Equal(t, "approved", retrieved.Status)
		assert.Equal(t, "Yes, proceed", retrieved.Response)
		assert.Equal(t, "bob@example.com", retrieved.RespondedBy)
		assert.NotNil(t, retrieved.RespondedAt)
		assert.Equal(t, "Looks good", retrieved.ResponseData["reason"])
	})

	t.Run("RespondToRequest_NotFound", func(t *testing.T) {
		err := store.RespondToRequest(ctx, "nonexistent", "approved", "Yes", "test@example.com", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	// AC3: a second respond on an already-decided row is a no-op that returns nil
	// (not an error) and does not mutate the row — the original outcome stands.
	t.Run("RespondToRequest_AlreadyDecidedIsNoOp", func(t *testing.T) {
		store = NewInMemoryHumanRequestStore()

		now := time.Now()
		req := &HumanRequest{
			ID:        "already-responded",
			Status:    "pending",
			ExpiresAt: now.Add(5 * time.Minute),
		}
		err := store.Store(ctx, req)
		require.NoError(t, err)

		// Resolve once.
		err = store.RespondToRequest(ctx, "already-responded", "approved", "Yes", "alice@example.com", nil)
		require.NoError(t, err)

		// A conflicting second response is a no-op: nil, first outcome preserved.
		err = store.RespondToRequest(ctx, "already-responded", "rejected", "No", "bob@example.com", nil)
		require.NoError(t, err)

		unchanged, err := store.Get(ctx, "already-responded")
		require.NoError(t, err)
		assert.Equal(t, "approved", unchanged.Status)
		assert.Equal(t, "Yes", unchanged.Response)
		assert.Equal(t, "alice@example.com", unchanged.RespondedBy)
	})

	// AC4: respond on a request whose ExpiresAt is already past does not resolve
	// it and returns nil (not an error); the row stays pending.
	t.Run("RespondToRequest_ExpiredIsNoOp", func(t *testing.T) {
		store = NewInMemoryHumanRequestStore()

		now := time.Now()
		req := &HumanRequest{
			ID:        "expired",
			Status:    "pending",
			ExpiresAt: now.Add(-1 * time.Minute),
		}
		err := store.Store(ctx, req)
		require.NoError(t, err)

		err = store.RespondToRequest(ctx, "expired", "approved", "Yes", "alice@example.com", nil)
		require.NoError(t, err)

		unresolved, err := store.Get(ctx, "expired")
		require.NoError(t, err)
		assert.Equal(t, "pending", unresolved.Status)
		assert.Empty(t, unresolved.RespondedBy)
	})
}

func TestInMemoryHumanRequestStore_Concurrent(t *testing.T) {
	// This test verifies thread-safety with -race detector
	ctx := context.Background()
	store := NewInMemoryHumanRequestStore()

	// Concurrent writes
	t.Run("ConcurrentWrites", func(t *testing.T) {
		const numGoroutines = 10
		done := make(chan bool)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer func() { done <- true }()

				req := &HumanRequest{
					ID:        fmt.Sprintf("concurrent-%d", id),
					Status:    "pending",
					ExpiresAt: time.Now().Add(time.Hour),
					Question:  fmt.Sprintf("Question %d", id),
				}

				err := store.Store(ctx, req)
				assert.NoError(t, err)
			}(i)
		}

		// Wait for all goroutines
		for i := 0; i < numGoroutines; i++ {
			<-done
		}

		// Verify all requests were stored
		pending, err := store.ListPending(ctx)
		require.NoError(t, err)
		assert.Len(t, pending, numGoroutines)
	})

	// Concurrent reads
	t.Run("ConcurrentReads", func(t *testing.T) {
		store = NewInMemoryHumanRequestStore()

		// Store a request
		req := &HumanRequest{
			ID:        "read-test",
			Status:    "pending",
			ExpiresAt: time.Now().Add(time.Hour),
		}
		err := store.Store(ctx, req)
		require.NoError(t, err)

		// Concurrent reads
		const numReaders = 20
		done := make(chan bool)

		for i := 0; i < numReaders; i++ {
			go func() {
				defer func() { done <- true }()

				retrieved, err := store.Get(ctx, "read-test")
				assert.NoError(t, err)
				assert.Equal(t, "read-test", retrieved.ID)
			}()
		}

		// Wait for all readers
		for i := 0; i < numReaders; i++ {
			<-done
		}
	})

	// Concurrent read/write
	t.Run("ConcurrentReadWrite", func(t *testing.T) {
		store = NewInMemoryHumanRequestStore()

		// Initial request
		req := &HumanRequest{
			ID:        "rw-test",
			Status:    "pending",
			ExpiresAt: time.Now().Add(time.Hour),
		}
		err := store.Store(ctx, req)
		require.NoError(t, err)

		done := make(chan bool)

		// Writers
		for i := 0; i < 5; i++ {
			go func(id int) {
				defer func() { done <- true }()

				newReq := &HumanRequest{
					ID:        fmt.Sprintf("rw-test-%d", id),
					Status:    "pending",
					ExpiresAt: time.Now().Add(time.Hour),
				}
				_ = store.Store(ctx, newReq)
			}(i)
		}

		// Readers
		for i := 0; i < 5; i++ {
			go func() {
				defer func() { done <- true }()

				_, _ = store.Get(ctx, "rw-test")
			}()
		}

		// Wait for all
		for i := 0; i < 10; i++ {
			<-done
		}
	})
}

func TestNoOpNotifier(t *testing.T) {
	notifier := &NoOpNotifier{}
	ctx := context.Background()

	req := &HumanRequest{
		ID:       "test",
		Question: "Test question",
	}

	err := notifier.Notify(ctx, req)
	assert.NoError(t, err)
}

func TestJSONNotifier(t *testing.T) {
	// Create a test HTTP server to receive webhook requests
	received := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = true
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Loom-HITL/1.0", r.Header.Get("User-Agent"))

		// Verify request body can be decoded
		var req HumanRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)
		assert.Equal(t, "test", req.ID)
		assert.Equal(t, "Test question", req.Question)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := NewJSONNotifier(server.URL)
	ctx := context.Background()

	req := &HumanRequest{
		ID:          "test",
		Question:    "Test question",
		RequestType: "approval",
		Priority:    "high",
		Context: map[string]interface{}{
			"key": "value",
		},
	}

	// Should successfully POST to webhook
	err := notifier.Notify(ctx, req)
	assert.NoError(t, err)
	assert.True(t, received, "Webhook should have been called")
}

// Findings 16/17 — store law: a zero expiry means "no expiry" (the row stays
// resolvable), and a terminal "timeout" write closes an already-expired row.
// Round-3 finding 9 — the resolve-once + expiry guarantee is the STORE's own,
// not a function of the caller's payload: no status value can lift the expiry
// guard, a pending row cannot be stored without an expiry, and terminal closes
// past expiry go through the dedicated ExpireRequest path only.
func TestRespondToRequest_StoreOwnedExpiryGuard_InMemory(t *testing.T) {
	store := NewInMemoryHumanRequestStore()

	noExpiry := &HumanRequest{ID: "r1", SessionID: "s", Status: "pending"}
	require.Error(t, store.Store(context.Background(), noExpiry),
		"a pending row with no expiry would be permanently approvable — the store must refuse it")

	expired := &HumanRequest{ID: "r2", SessionID: "s", Status: "pending",
		ExpiresAt: time.Now().Add(-time.Minute)}
	require.NoError(t, store.Store(context.Background(), expired))
	require.NoError(t, store.RespondToRequest(context.Background(), "r2", "approved", "", "human", nil))
	got, err := store.Get(context.Background(), "r2")
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status, "an expired row cannot be resolved")
	require.NoError(t, store.RespondToRequest(context.Background(), "r2", "timeout", "", "attacker", nil))
	got, err = store.Get(context.Background(), "r2")
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status,
		"the caller-supplied status must not decide whether the expiry guard applies")
	require.NoError(t, store.ExpireRequest(context.Background(), "r2", "system:expiry"))
	got, err = store.Get(context.Background(), "r2")
	require.NoError(t, err)
	require.Equal(t, "timeout", got.Status, "ExpireRequest is the one path that closes an expired row")
	require.Equal(t, "system:expiry", got.RespondedBy)

	// Closing is not resolving: ExpireRequest must never overwrite a decision.
	live := &HumanRequest{ID: "r3", SessionID: "s", Status: "pending",
		ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, store.Store(context.Background(), live))
	require.NoError(t, store.RespondToRequest(context.Background(), "r3", "approved", "yes", "human", nil))
	require.NoError(t, store.ExpireRequest(context.Background(), "r3", "system:expiry"))
	got, err = store.Get(context.Background(), "r3")
	require.NoError(t, err)
	require.Equal(t, "approved", got.Status)
	require.Equal(t, "human", got.RespondedBy)
}

// The contact_human window obeys the same ceiling: a model-chosen
// timeout_seconds cannot stretch a question's row past the turn deadline.
func TestContactHuman_TimeoutCeilingAtTurnDeadline(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	tool := NewContactHumanTool(ContactHumanConfig{
		Store:        store,
		Notifier:     &NoOpNotifier{},
		PollInterval: 5 * time.Millisecond,
	})

	deadline := time.Now().Add(30 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	go func() {
		_, _ = tool.Execute(ctx, map[string]interface{}{
			"question":        "proceed?",
			"timeout_seconds": float64(86400), // a day, model-chosen
		})
	}()

	var hr *HumanRequest
	require.Eventually(t, func() bool {
		pending, err := store.ListPending(context.Background())
		if err != nil || len(pending) == 0 {
			return false
		}
		hr = pending[0]
		return true
	}, 5*time.Second, 5*time.Millisecond)
	cancel()

	require.False(t, hr.ExpiresAt.After(deadline),
		"a model-chosen window must be ceilinged AT the turn deadline — not near it")
}

// questionAbsentStore reports every row absent, postgres-style.
type questionAbsentStore struct{ *InMemoryHumanRequestStore }

func (s *questionAbsentStore) Get(ctx context.Context, id string) (*HumanRequest, error) {
	return nil, nil
}

func TestContactHuman_WaitSurvivesAbsentRow(t *testing.T) {
	store := &questionAbsentStore{NewInMemoryHumanRequestStore()}
	tool := NewContactHumanTool(ContactHumanConfig{
		Store:        store,
		Notifier:     &NoOpNotifier{},
		Timeout:      200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	})

	// Must not panic; times out and reports failure.
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"question": "still there?",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.Error, "an absent row is a timeout, not a panic")
}

func TestContactHuman_KindStampOverridesModelValue(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	tool := NewContactHumanTool(ContactHumanConfig{
		Store:        store,
		Notifier:     &NoOpNotifier{},
		Timeout:      100 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	})

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"question": "may I?",
		"context":  map[string]interface{}{"kind": "approval", "note": "model-authored"},
	})
	require.NoError(t, err)

	// The unanswered question is terminally closed on the give-up exit, so the
	// row is read back by session, not from the pending set.
	rows, err := store.ListBySession(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "timeout", rows[0].Status,
		"an unanswered question's give-up exit closes its row terminally")
	assert.Equal(t, "question", rows[0].Context["kind"],
		"the harness stamps the origin discriminator over any model-supplied value")
	assert.Equal(t, "model-authored", rows[0].Context["note"],
		"the rest of the model's context payload survives")
}
