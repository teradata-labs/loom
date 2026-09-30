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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
)

// foldSkillConversation builds a conversation heavy enough that relief must
// fold: loadsByTurn names the skills whose manage_skills load pair lands in
// each turn (1-based).
func foldSkillConversation(t *testing.T, sm *SegmentedMemory, store *SessionStore, sessionID string, turns int, loadsByTurn map[int][]string) {
	t.Helper()
	ctx := context.Background()
	save := func(m Message, turnStart bool) {
		require.NoError(t, store.SaveMessage(ctx, sessionID, &m, turnStart))
		sm.AddMessage(ctx, m)
	}
	for turn := 1; turn <= turns; turn++ {
		save(Message{Role: "user", Content: fmt.Sprintf("turn %d: ", turn) + strings.Repeat("grant audit 123 db=SALES cpu=456; ", 100)}, true)
		for _, name := range loadsByTurn[turn] {
			id := fmt.Sprintf("load-%d-%s", turn, name)
			save(Message{Role: "assistant", ToolCalls: []ToolCall{{
				ID: id, Name: "manage_skills",
				Input: map[string]interface{}{"action": "load", "name": name},
			}}}, false)
			save(Message{Role: "tool", ToolUseID: id, Content: "Skill loaded: " + name}, false)
		}
		save(Message{Role: "assistant", Content: fmt.Sprintf("noted %d", turn)}, false)
	}
}

func TestReleasePressure_FoldKeepsSkillReloadedAfterRegion(t *testing.T) {
	tests := []struct {
		name           string
		loadsByTurn    map[int][]string
		priorNote      []string
		wantDeactivate []string
		wantNoted      []string
		wantNotNoted   []string
	}{
		{
			name:           "skill reloaded in the current turn stays active",
			loadsByTurn:    map[int][]string{1: {"alpha", "beta"}, 7: {"alpha"}},
			wantDeactivate: []string{"beta"},
			wantNoted:      []string{"beta"},
			wantNotNoted:   []string{"alpha"},
		},
		{
			name:           "skill loaded only in the folded region is deactivated",
			loadsByTurn:    map[int][]string{1: {"alpha"}},
			wantDeactivate: []string{"alpha"},
			wantNoted:      []string{"alpha"},
		},
		{
			name:           "a reload clears the note an earlier fold left",
			loadsByTurn:    map[int][]string{7: {"gamma"}},
			priorNote:      []string{"gamma"},
			wantDeactivate: nil,
			wantNotNoted:   []string{"gamma"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			const sessionID = "sess-fold-reload"
			require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

			sm := NewSegmentedMemory("ROM", 6000, 600)
			sm.SetThreshold(6000)
			sm.SetSessionStore(store, sessionID)
			sm.SetCompressor(&foldCompressor{out: "covers msg:1-999\nfolded conversation"})
			var mu sync.Mutex
			var deactivated []string
			sm.SetSkillDeactivationHook(func(_ string, name string) {
				mu.Lock()
				defer mu.Unlock()
				deactivated = append(deactivated, name)
			})
			if len(tt.priorNote) > 0 {
				sm.mu.Lock()
				sm.foldedSkills = map[string]bool{}
				for _, n := range tt.priorNote {
					sm.foldedSkills[n] = true
				}
				sm.mu.Unlock()
			}

			foldSkillConversation(t, sm, store, sessionID, 7, tt.loadsByTurn)
			shed, _, _ := sm.ReleasePressure(ctx, 0)
			require.True(t, shed, "the conversation must be heavy enough to fold")
			require.True(t, sm.HasL2Content(), "a fold must have run")

			mu.Lock()
			assert.ElementsMatch(t, tt.wantDeactivate, deactivated)
			mu.Unlock()
			summary := sm.GetL2Summary()
			for _, n := range tt.wantNoted {
				assert.Contains(t, summary, n, "folded skill %q is noted", n)
			}
			for _, n := range tt.wantNotNoted {
				assert.NotContains(t, summary, n, "live skill %q must not be noted as folded", n)
			}
		})
	}
}
