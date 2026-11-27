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

import (
	"context"
	"errors"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Undoing a turn that waits for a person's decision undoes the wait too: the
// request is closed, the session takes its next turn, and the decision for
// the undone turn runs nothing.
func TestARewindOfAParkedTurnClosesItsRequest(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, []mockLLMResponse{
		{content: "first reply"},
		{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "export_csv", Input: map[string]interface{}{"v": "w"}}}},
		{content: "after the undo"},
	}, "export_csv")
	ctx := context.Background()
	if _, err := f.ag.Chat(ctx, "s1", "hello"); err != nil {
		t.Fatal(err)
	}
	_, err := f.ag.Chat(ctx, "s1", "export it")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("want a park, got %v", err)
	}
	hr := f.pendingParked(t, "s1")

	removed, err := f.ag.RewindSession(ctx, "s1", 1)
	if err != nil || removed != 1 {
		t.Fatalf("rewind removed %d turns, err %v; want 1, nil", removed, err)
	}

	reqs, err := f.store.ListBySession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reqs {
		if r.RequestType == "parked" && r.Status == "pending" {
			t.Fatalf("request %s of the undone turn is still pending", r.ID)
		}
	}

	resp, err := f.ag.Chat(ctx, "s1", "something else")
	if err != nil {
		t.Fatalf("a turn after undoing the parked turn is refused: %v", err)
	}
	if resp.Content != "after the undo" {
		t.Fatalf("reply = %q", resp.Content)
	}

	if _, err := f.ag.ResumeChat(ctx, "s1", ParkDecision{RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true}, nil); err == nil {
		t.Fatal("the decision for the undone turn was accepted")
	}
	if runs := f.tools["export_csv"].runs.Load(); runs != 0 {
		t.Fatalf("export_csv ran %d times for a turn that was undone", runs)
	}
}
