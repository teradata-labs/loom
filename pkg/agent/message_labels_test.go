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

	"github.com/teradata-labs/loom/pkg/eventlog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
)

// labelProseRow labels the assistant prose rows of the fold fixture by their
// index; every other message is unlabelled.
func labelProseRow(m *Message) map[string]string {
	var i int
	if m.Role == "assistant" && strings.HasPrefix(m.Content, "analysis ") {
		if _, err := fmt.Sscanf(m.Content, "analysis %d", &i); err == nil {
			return map[string]string{"tier": fmt.Sprintf("t%d", i)}
		}
	}
	return nil
}

// concatJoin joins pairwise by concatenating each key's values in join order,
// so a test can see whether the join ran in context order.
func concatJoin(a, b map[string]string) map[string]string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if prev, ok := out[k]; ok {
			out[k] = prev + "," + v
		} else {
			out[k] = v
		}
	}
	return out
}

// labeledFoldFixture builds the single-turn fold fixture (the shape of
// reliefSingleTurnFixture: assistant-text pressure mass that must escalate to
// the rung-0 fold) with a labeler and join wired directly on the memory.
func labeledFoldFixture(t *testing.T, join func(a, b map[string]string) map[string]string) *SegmentedMemory {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	const sessionID = "sess-labels"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

	sm := NewSegmentedMemory("ROM", 6000, 600)
	sm.SetThreshold(6000)
	sm.SetSessionStore(store, sessionID)
	sm.SetCompressor(&foldCompressor{out: "covers msg:1-99\nsummary of the analysis prose"})
	sm.SetMessageLabeler(labelProseRow)
	if join != nil {
		sm.SetLabelJoin(join)
	}

	persist := func(role, content, toolUseID string, calls []ToolCall, turnStart bool) {
		m := Message{Role: role, Content: content, ToolUseID: toolUseID, ToolCalls: calls}
		require.NoError(t, store.SaveMessage(ctx, sessionID, &m, turnStart))
		sm.AddMessage(ctx, m)
	}
	// The ticket is a current-turn user row: it is protected from the fold, so
	// its label must NOT ride the summary.
	persist("user", "TICKET: build the marts", "", nil, true)
	for i := 0; i < 5; i++ {
		persist("assistant", strings.Repeat(fmt.Sprintf("analysis %d of models io=17 cpu=23; ", i), 120), "", nil, false)
	}
	persist("assistant", "", "", []ToolCall{{ID: "cLast", Name: "bulk_scan", Input: map[string]interface{}{}}}, false)
	persist("tool", "small result", "cLast", nil, false)
	require.Equal(t, int64(1), sm.CurrentTurn())
	return sm
}

// summaryMessageOf returns the fold summary's system message from the compiled
// context (ROM first, summary second).
func summaryMessageOf(t *testing.T, sm *SegmentedMemory) *Message {
	t.Helper()
	compiled := sm.GetMessagesForLLM()
	require.GreaterOrEqual(t, len(compiled), 2)
	require.Equal(t, "system", compiled[1].Role)
	require.Contains(t, compiled[1].Content, "covers msg:")
	return &compiled[1]
}

// The compaction summary replaces labelled messages: its labels are the join
// of the dropped messages' labels, in context order. The protected user row's
// label must not leak into the summary.
func TestMessageLabels_SurviveCompaction(t *testing.T) {
	sm := labeledFoldFixture(t, concatJoin)

	shed, _, _ := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed, "pressure must shed via fold")
	require.Contains(t, sm.summary.text, "summary of the analysis prose", "the fold must have committed")

	// The dropped prose rows left L1; their labels ride the summary message.
	for _, m := range sm.GetMessages() {
		require.NotContains(t, m.Content, "analysis ", "the prose rows must have folded away")
	}
	summary := summaryMessageOf(t, sm)
	require.Equal(t, map[string]string{"tier": "t0,t1,t2,t3,t4"}, summary.Labels,
		"summary labels = join of the dropped rows' labels in context order")

	// The protected ticket stays in L1 with its own label, unjoined.
	var ticket *Message
	for i := range sm.contextMessages {
		if sm.contextMessages[i].Role == "user" {
			ticket = &sm.contextMessages[i]
		}
	}
	require.NotNil(t, ticket)
	require.Nil(t, ticket.Labels, "the labeler labels nothing on the ticket row")
}

// With WithLabelJoin unset, the default join applies: per key, the
// lexicographically greater value wins.
func TestMessageLabels_DefaultJoinIsLexicographicallyGreater(t *testing.T) {
	sm := labeledFoldFixture(t, nil)

	shed, _, _ := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed, "pressure must shed via fold")

	summary := summaryMessageOf(t, sm)
	require.Equal(t, map[string]string{"tier": "t4"}, summary.Labels,
		"default join keeps the lexicographically greatest value per key")
}

// newLabeledEventLogAgent mirrors newEventLogAgent with the labeler wired.
func newLabeledEventLogAgent(t *testing.T, llm LLMProvider, store SessionStorage, log eventlog.EventLogStore, labeler func(*Message) map[string]string) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	opts := []Option{WithConfig(cfg), WithEventLog(log), WithMessageLabeler(labeler)}
	if store != nil {
		opts = append(opts, WithMemory(NewMemoryWithStore(store)))
	}
	ag := NewAgent(&mockBackend{}, llm, opts...)
	return ag
}

// labeledL1 returns the session's L1 context messages.
func labeledL1(t *testing.T, ag *Agent, sessionID string) []Message {
	t.Helper()
	sess, ok := ag.memory.GetSession(sessionID)
	require.True(t, ok, "session %s must be in memory", sessionID)
	segMem, ok := sess.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok, "session carries segmented memory")
	return segMem.GetMessages()
}

// A recorded session's labels survive replay: the resumed session's context
// carries the same labels for the same messages (the replay path re-runs the
// labeler where the restored rows enter the context).
func TestMessageLabels_SurviveReplay(t *testing.T) {
	recorder := &recordingEventLog{}
	sessions := newSQLiteSessions(t)

	// Content-keyed labeler: labels are a function of the message, so the two
	// agents' rows are comparable by content.
	labeler := func(m *Message) map[string]string {
		if m.Role == "user" {
			return map[string]string{"origin": "user"}
		}
		if m.Role == "assistant" && m.Content == "Mock response" {
			return map[string]string{"origin": "reply"}
		}
		return nil
	}

	turnCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := newLabeledEventLogAgent(t,
		&scriptedLLM{steps: []func(context.Context) (*llmtypes.LLMResponse, error){crashStep(cancel)}},
		sessions, recorder, labeler)
	_, err := first.Chat(turnCtx, "s-labels-replay", "hello")
	require.Error(t, err, "the turn should end with the simulated crash")

	// The live session: the user row entered the context labelled.
	live := map[string]map[string]string{}
	for _, m := range labeledL1(t, first, "s-labels-replay") {
		if len(m.Labels) > 0 {
			live[m.Content] = m.Labels
		}
	}
	require.Equal(t, map[string]string{"origin": "user"}, live["hello"],
		"the live row entered the context labelled")

	// A fresh process: new agent and memory over the same durable stores, and
	// a resume that loads the session through the replay path.
	second := newLabeledEventLogAgent(t, &mockSimpleLLM{}, sessions, recorder, labeler)
	_, err = second.ResumeSession(context.Background(), "s-labels-replay")
	require.NoError(t, err)

	resumed := map[string]map[string]string{}
	for _, m := range labeledL1(t, second, "s-labels-replay") {
		if len(m.Labels) > 0 {
			resumed[m.Content] = m.Labels
		}
	}
	require.Equal(t, live["hello"], resumed["hello"],
		"the replayed row carries the same labels as the live session's row")
	require.Equal(t, map[string]string{"origin": "reply"}, resumed["Mock response"],
		"the resumed turn's new reply is labelled too")
}
