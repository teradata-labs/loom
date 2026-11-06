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
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Review round 3, Q1: a step the log keeps because its tool row did not
// persist can have its assistant message folded by compression. Folded rows
// are dropped from the L1 view resume reads (and on reload), so the message is
// no longer there for unsavedBatchStep to find. This drives a real fold, both
// from a later turn and inside the same turn, and asks what resume's check
// makes of the kept step.
func TestUnsavedBatchStep_KeptStepWhoseMessageWasFolded(t *testing.T) {
	kept := llmtypes.ToolCall{ID: "toolu_kept", Name: "crashy", Input: map[string]interface{}{"n": float64(1)}}
	filler := strings.Repeat("grant audit 123 db=SALES cpu=456; ", 100)

	tests := []struct {
		name string
		// build saves the conversation. The kept step's assistant message is
		// saved but its tool row reaches memory only, as if that write failed.
		build func(save func(Message, bool), addMemoryOnly func(Message))
	}{
		{
			name: "folded by a later turn",
			build: func(save func(Message, bool), addMemoryOnly func(Message)) {
				save(Message{Role: "user", Content: "turn 1: " + filler}, true)
				save(Message{Role: "assistant", ToolCalls: []llmtypes.ToolCall{kept}}, false)
				addMemoryOnly(Message{Role: "tool", ToolUseID: kept.ID, Content: "ok"})
				for turn := 2; turn <= 7; turn++ {
					save(Message{Role: "user", Content: fmt.Sprintf("turn %d: ", turn) + filler}, true)
					save(Message{Role: "assistant", Content: fmt.Sprintf("noted %d", turn)}, false)
				}
			},
		},
		{
			// One turn whose mass is the model's own text: no prior turn to
			// fold, so relief folds the current turn up to its last assistant.
			name: "folded within the same turn",
			build: func(save func(Message, bool), addMemoryOnly func(Message)) {
				save(Message{Role: "user", Content: "turn 1: go"}, true)
				save(Message{Role: "assistant", Content: filler, ToolCalls: []llmtypes.ToolCall{kept}}, false)
				addMemoryOnly(Message{Role: "tool", ToolUseID: kept.ID, Content: "ok"})
				for i := 0; i < 6; i++ {
					save(Message{Role: "assistant", Content: fmt.Sprintf("step %d: ", i) + filler}, false)
				}
				save(Message{Role: "assistant", Content: "final"}, false)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			const sessionID = "sess-q1"
			require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

			sm := NewSegmentedMemory("ROM", 6000, 600)
			sm.SetThreshold(6000)
			sm.SetSessionStore(store, sessionID)
			sm.SetCompressor(&foldCompressor{out: "covers msg:1-999\nfolded conversation"})
			tt.build(func(m Message, turnStart bool) {
				require.NoError(t, store.SaveMessage(ctx, sessionID, &m, turnStart))
				sm.AddMessage(ctx, m)
			}, func(m Message) { sm.AddMessage(ctx, m) })

			shed, _, _ := sm.ReleasePressure(ctx, 0)
			require.True(t, shed, "the conversation must be heavy enough to fold")
			require.True(t, sm.HasL2Content(), "a fold must have run")

			foldedAway := true
			for _, m := range sm.GetMessages() {
				if m.Role == "assistant" && len(m.ToolCalls) > 0 && m.ToolCalls[0].ID == kept.ID {
					foldedAway = false
				}
			}
			require.True(t, foldedAway, "precondition: the kept step's assistant message was folded out of L1")

			a := &Agent{memory: &Memory{store: store}}

			// L1 alone no longer holds the message; the folded row does.
			_, err = unsavedBatchStep(stepEvents(t, 0, kept), sm.GetMessages())
			require.ErrorIs(t, err, ErrBatchNotSaved, "precondition: the L1 view alone reads the folded step as lost")

			_, err = a.unsavedBatchStepWithFolded(ctx, sessionID, stepEvents(t, 0, kept), sm.GetMessages())
			require.NoError(t, err, "a folded message is saved, not lost: resume must not refuse as ErrBatchNotSaved")

			// A step whose message was never saved is still refused.
			lost := llmtypes.ToolCall{ID: "toolu_lost", Name: "crashy", Input: map[string]interface{}{"n": float64(2)}}
			events := append(stepEvents(t, 0, kept), stepEvents(t, 1, lost)...)
			step, err := a.unsavedBatchStepWithFolded(ctx, sessionID, events, sm.GetMessages())
			require.ErrorIs(t, err, ErrBatchNotSaved)
			require.Equal(t, 1, step)
		})
	}
}

// Review round 5, B13: the Q1 fix is only worth anything if ResumeSession
// calls the folded-aware check. The test above calls the helper directly, so
// it passes even with ResumeSession back on the L1-only check. This goes
// through ResumeSession, on a session reloaded from the store the way a fresh
// process would see it.
func TestResumeSession_KeptStepWhoseMessageWasFolded_IsNotRefusedAsUnsaved(t *testing.T) {
	kept := llmtypes.ToolCall{ID: "toolu_kept", Name: "crashy", Input: map[string]interface{}{"n": float64(1)}}
	filler := strings.Repeat("grant audit 123 db=SALES cpu=456; ", 100)

	store := newSQLiteSessions(t)
	ctx := context.Background()
	const sessionID = "sess-b13"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

	sm := NewSegmentedMemory("ROM", 6000, 600)
	sm.SetThreshold(6000)
	sm.SetSessionStore(store, sessionID)
	sm.SetCompressor(&foldCompressor{out: "covers msg:1-999\nfolded conversation"})
	save := func(m Message, turnStart bool) {
		require.NoError(t, store.SaveMessage(ctx, sessionID, &m, turnStart))
		sm.AddMessage(ctx, m)
	}
	save(Message{Role: "user", Content: "turn 1: " + filler}, true)
	save(Message{Role: "assistant", ToolCalls: []llmtypes.ToolCall{kept}}, false)
	for turn := 2; turn <= 7; turn++ {
		save(Message{Role: "user", Content: fmt.Sprintf("turn %d: ", turn) + filler}, true)
		save(Message{Role: "assistant", Content: fmt.Sprintf("noted %d", turn)}, false)
	}
	shed, _, _ := sm.ReleasePressure(ctx, 0)
	require.True(t, shed, "the conversation must be heavy enough to fold")

	// A fresh process: new agent and memory over the same durable store, with
	// the kept step's events still in the log.
	recorder := &recordingEventLog{}
	for _, e := range stepEvents(t, 0, kept) {
		e.SessionID = sessionID
		recorder.events = append(recorder.events, e)
	}
	ag := newEventLogAgent(t, &mockSimpleLLM{}, store, recorder, &crashingTool{})

	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, sessionID, ag.config.Name, "")
	for _, m := range rawSessionMessages(sess) {
		require.False(t, len(m.ToolCalls) > 0 && m.ToolCalls[0].ID == kept.ID,
			"precondition: the kept step's assistant message is not in the reloaded L1 view")
	}

	_, err := ag.ResumeSession(ctx, sessionID)
	require.NotErrorIs(t, err, ErrBatchNotSaved, "a folded message is saved, not lost")
	require.ErrorIs(t, err, ErrNothingParked, "the folded step is accepted and the session tail, a finished turn, has nothing to resume")
}
