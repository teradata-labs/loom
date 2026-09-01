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
package agent

// claimParkedRequest is the serialization point of the whole feature: exactly
// one resume may take ownership of a decision, and only the owner executes the
// batch. Driving it directly is deliberate — through ResumeChat the claim
// happens microseconds after the row is read, so goroutine jitter alone
// decides whether two resumes ever collide, and an end-to-end test passes
// whether or not the mechanism works. These hit the mechanism itself.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// newClaimFixture builds a park-enabled agent and one pending parked row.
func newClaimFixture(t *testing.T, ttl time.Duration) (*Agent, *shuttle.InMemoryHumanRequestStore, *shuttle.HumanRequest) {
	t.Helper()
	store := shuttle.NewInMemoryHumanRequestStore()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{responses: []mockLLMResponse{{content: "x"}}},
		WithConfig(cfg), WithHITLPark(store, ttl, nil))

	now := time.Now()
	hr := &shuttle.HumanRequest{
		ID:          uuid.New().String(),
		AgentID:     ag.id,
		SessionID:   "s-claim-unit",
		RequestType: "parked",
		Kind:        "approval",
		Status:      "pending",
		CreatedAt:   now,
		ExpiresAt:   now.Add(ttl),
		Params:      map[string]interface{}{"c-1": map[string]interface{}{"kind": "approval"}},
	}
	if err := store.Store(context.Background(), hr); err != nil {
		t.Fatalf("Store: %v", err)
	}
	return ag, store, hr
}

// TestClaimParkedRequest_ExactlyOneWinner is the at-most-once proof. Every
// claimant's store write returns nil — the winner's because it landed, the
// losers' because the store's conditional write refused them as a deliberate
// no-op — and every claimant then reads back a non-pending row. Only the
// token distinguishes them. Without that check every loser would take the
// winner's close for its own and run the approved batch again.
func TestClaimParkedRequest_ExactlyOneWinner(t *testing.T) {
	const claimants = 8
	ag, store, hr := newClaimFixture(t, time.Hour)
	ctx := context.Background()
	decision := ParkDecision{RequestID: hr.ID, Approved: true, Reason: "ok"}

	var wg sync.WaitGroup
	results := make([]error, claimants)
	start := make(chan struct{})
	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release together, into the same window
			results[i] = ag.claimParkedRequest(ctx, hr, decision, false)
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for i, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrUnknownRequest):
			// correct loser: someone else owns this decision
		default:
			t.Errorf("claimant %d got %v, want nil or ErrUnknownRequest", i, err)
		}
	}
	if winners != 1 {
		t.Errorf("%d claimants won the decision, want exactly 1 — every extra winner "+
			"executes the human-approved batch again", winners)
	}

	after, err := store.Get(ctx, hr.ID)
	if err != nil || after == nil {
		t.Fatalf("Get: %v", err)
	}
	if after.Status != "approved" {
		t.Errorf("row status = %q, want approved", after.Status)
	}
}

// TestClaimParkedRequest_LapsedRowIsClosedNotSilentlyLeftPending — the row
// expires between the resume reading it and claiming it. RespondToRequest
// refuses to write past expiry and returns nil, so a claim judged by that
// error would proceed to execute against a row still marked pending, leaving
// the session refused at guardParkedTail forever.
func TestClaimParkedRequest_LapsedRowIsClosedNotSilentlyLeftPending(t *testing.T) {
	ag, store, hr := newClaimFixture(t, 80*time.Millisecond)
	ctx := context.Background()

	// The window closes while the caller is still deciding what to do.
	time.Sleep(150 * time.Millisecond)

	// expired=false is the point: the resume checked expiry BEFORE the lapse,
	// exactly as ResumeChat does, and only the read-back can catch it.
	err := ag.claimParkedRequest(ctx, hr, ParkDecision{RequestID: hr.ID, Approved: true}, false)
	if !errors.Is(err, ErrDecisionExpired) {
		t.Fatalf("claim of a lapsed row = %v, want ErrDecisionExpired", err)
	}

	after, gerr := store.Get(ctx, hr.ID)
	if gerr != nil || after == nil {
		t.Fatalf("Get: %v", gerr)
	}
	if after.Status == "pending" {
		t.Errorf("lapsed row left pending; guardParkedTail will refuse every later turn")
	}
}

// TestClaimParkedRequest_RejectionClaimsToo — the refusal path takes ownership
// on the same terms, so a rejected batch cannot be refused twice and cannot
// leave its row open.
func TestClaimParkedRequest_RejectionClaimsToo(t *testing.T) {
	ag, store, hr := newClaimFixture(t, time.Hour)
	ctx := context.Background()
	decision := ParkDecision{RequestID: hr.ID, Approved: false, Reason: "rejected by user: no"}

	if err := ag.claimParkedRequest(ctx, hr, decision, false); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := ag.claimParkedRequest(ctx, hr, decision, false); !errors.Is(err, ErrUnknownRequest) {
		t.Errorf("second claim = %v, want ErrUnknownRequest", err)
	}

	after, _ := store.Get(ctx, hr.ID)
	if after == nil || after.Status != "rejected" {
		t.Fatalf("row status = %v, want rejected", after)
	}
	if after.Response != decision.Reason {
		t.Errorf("row response = %q, want the human's verbatim reason %q", after.Response, decision.Reason)
	}
}
