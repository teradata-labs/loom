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
	sm, _ := labeledFoldFixtureWithStore(t, join, nil)
	return sm
}

// labeledFoldFixtureWithStore is labeledFoldFixture with the labeler
// selectable (nil = labelProseRow) and the session store returned, so a test
// can restore the session into a fresh memory.
func labeledFoldFixtureWithStore(t *testing.T, join func(a, b map[string]string) map[string]string, labeler func(*Message) map[string]string) (*SegmentedMemory, *SessionStore) {
	t.Helper()
	if labeler == nil {
		labeler = labelProseRow
	}
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
	sm.SetMessageLabeler(labeler)
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
	return sm, store
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

// With WithLabelJoin unset, the default join applies: per key, every distinct
// value across both maps is kept, sorted and comma-joined — no value is ever
// dropped, so a security label always survives into the summary.
func TestMessageLabels_DefaultJoinKeepsEveryValue(t *testing.T) {
	sm := labeledFoldFixture(t, nil)

	shed, _, _ := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed, "pressure must shed via fold")

	summary := summaryMessageOf(t, sm)
	require.Equal(t, map[string]string{"tier": "t0,t1,t2,t3,t4"}, summary.Labels,
		"default join keeps every distinct value, sorted")
}

// labelClassificationRow labels the first prose row "confidential" and the
// rest "public" — the fail-open case the lexicographically-greater default
// used to get wrong.
func labelClassificationRow(m *Message) map[string]string {
	if m.Role != "assistant" || !strings.HasPrefix(m.Content, "analysis ") {
		return nil
	}
	if strings.HasPrefix(m.Content, "analysis 0 ") {
		return map[string]string{"classification": "confidential"}
	}
	return map[string]string{"classification": "public"}
}

// The default join must not downgrade a security label: "confidential" folded
// next to "public" yields both values in the summary, never "public" alone.
func TestMessageLabels_DefaultJoinSecurityValuesFailClosed(t *testing.T) {
	sm, _ := labeledFoldFixtureWithStore(t, nil, labelClassificationRow)

	shed, _, _ := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed, "pressure must shed via fold")

	summary := summaryMessageOf(t, sm)
	require.Equal(t, map[string]string{"classification": "confidential,public"}, summary.Labels)
	require.Contains(t, summary.Labels["classification"], "confidential",
		"the confidential value must survive the default join")
}

// labelFirstProseRow labels only the first prose row, so the fold region has
// exactly one labelled message.
func labelFirstProseRow(m *Message) map[string]string {
	if m.Role == "assistant" && strings.HasPrefix(m.Content, "analysis 0 ") {
		return map[string]string{"tier": "t0"}
	}
	return nil
}

// The default join never returns one of its inputs: the summary's labels are
// a fresh map, so mutating them cannot reach a folded message's own labels.
func TestMessageLabels_DefaultJoinDoesNotAliasFoldedRows(t *testing.T) {
	sm, _ := labeledFoldFixtureWithStore(t, nil, labelFirstProseRow)

	var folded map[string]string
	for i := range sm.contextMessages {
		if m := &sm.contextMessages[i]; m.Role == "assistant" && strings.HasPrefix(m.Content, "analysis 0 ") {
			folded = m.Labels
		}
	}
	require.NotNil(t, folded, "the labelled row entered L1 labelled")

	shed, _, _ := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed, "pressure must shed via fold")
	require.Equal(t, map[string]string{"tier": "t0"}, sm.summary.labels)

	sm.summary.labels["probe"] = "x"
	require.NotContains(t, folded, "probe",
		"the summary's labels must be a fresh map, not the folded row's")
}

// failFoldStore fails every fold persist, modelling a store outage mid-fold.
type failFoldStore struct {
	*SessionStore
	calls int
}

func (s *failFoldStore) FoldMessages(ctx context.Context, sessionID string, seqs []int64, n int, text string, labels map[string]string) error {
	s.calls++
	return errors.New("fold persist down")
}

// A join that writes into its inputs must not corrupt live state when the
// fold's persist fails: the fold rolls back, so the live summary labels must
// be exactly what they were, and the folded messages' own labels untouched.
func TestMessageLabels_InPlaceJoinCannotCorruptLiveState(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	fail := &failFoldStore{SessionStore: store}

	ctx := context.Background()
	const sessionID = "sess-alias"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

	sm := NewSegmentedMemory("ROM", 6000, 600)
	sm.SetThreshold(6000)
	sm.SetSessionStore(fail, sessionID)
	sm.SetCompressor(&foldCompressor{out: "covers msg:1-99\nsummary"})
	sm.SetMessageLabeler(labelProseRow)
	sm.SetLabelJoin(func(a, b map[string]string) map[string]string { // in-place join
		for k, v := range b {
			if a == nil {
				a = map[string]string{}
			}
			a[k] = v
		}
		return a
	})
	// The post-restore state: a summary whose labels are live state the next
	// fold's join must never receive.
	sm.setSummary(1, "covers msg:1-99\nsummary", map[string]string{"tier": "t0"})

	persist := func(role, content, toolUseID string, calls []ToolCall, turnStart bool) {
		m := Message{Role: role, Content: content, ToolUseID: toolUseID, ToolCalls: calls}
		require.NoError(t, store.SaveMessage(ctx, sessionID, &m, turnStart))
		sm.AddMessage(ctx, m)
	}
	persist("user", "TICKET: build the marts", "", nil, true)
	for i := 0; i < 5; i++ {
		persist("assistant", strings.Repeat(fmt.Sprintf("analysis %d of models io=17 cpu=23; ", i), 120), "", nil, false)
	}
	persist("assistant", "", "", []ToolCall{{ID: "cLast", Name: "bulk_scan", Input: map[string]interface{}{}}}, false)
	persist("tool", "small result", "cLast", nil, false)

	shed, _, _ := sm.ReleasePressure(ctx, 0)
	require.False(t, shed, "the failed persist must fail the fold")
	require.Positive(t, fail.calls, "the fold must have attempted its persist")
	require.Equal(t, map[string]string{"tier": "t0"}, sm.summary.labels,
		"a failed fold must not mutate the live summary labels through an in-place join")

	for _, m := range sm.GetMessages() {
		if m.Role == "assistant" && strings.HasPrefix(m.Content, "analysis 3 ") {
			require.Equal(t, map[string]string{"tier": "t3"}, m.Labels,
				"the folded messages' own labels are intact")
		}
	}
}

// labelTicketAndProse labels the current-turn ticket row too — the row the
// fold protects, whose label must stay off the summary.
func labelTicketAndProse(m *Message) map[string]string {
	if m.Role == "user" && strings.HasPrefix(m.Content, "TICKET:") {
		return map[string]string{"source": "ticket"}
	}
	return labelProseRow(m)
}

// The fold joins only the replaced region: the protected ticket row keeps its
// own label in L1, unjoined, and its label never rides the summary.
func TestMessageLabels_ProtectedRowLabelDoesNotLeak(t *testing.T) {
	sm, _ := labeledFoldFixtureWithStore(t, concatJoin, labelTicketAndProse)

	shed, _, _ := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed, "pressure must shed via fold")

	summary := summaryMessageOf(t, sm)
	require.NotContains(t, summary.Labels, "source",
		"the protected ticket row's label must not ride the summary")
	require.Equal(t, map[string]string{"tier": "t0,t1,t2,t3,t4"}, summary.Labels)

	var ticket *Message
	for i := range sm.contextMessages {
		if sm.contextMessages[i].Role == "user" {
			ticket = &sm.contextMessages[i]
		}
	}
	require.NotNil(t, ticket)
	require.Equal(t, map[string]string{"source": "ticket"}, ticket.Labels,
		"the ticket keeps its own label in L1, unjoined")
}

// newLabeledEventLogAgentWithJoin mirrors newLabeledEventLogAgent and wires
// the join through the options as well — the agent wiring under test here.
func newLabeledEventLogAgentWithJoin(t *testing.T, llm LLMProvider, store SessionStorage, log eventlog.EventLogStore, labeler func(*Message) map[string]string, join func(a, b map[string]string) map[string]string) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	opts := []Option{WithConfig(cfg), WithEventLog(log), WithMessageLabeler(labeler), WithLabelJoin(join)}
	if store != nil {
		opts = append(opts, WithMemory(NewMemoryWithStore(store)))
	}
	return NewAgent(&mockBackend{}, llm, opts...)
}

// The WithMessageLabeler/WithLabelJoin options drive a session's fold label
// join without any direct SetMessageLabeler/SetLabelJoin call: the agent
// wiring lands them on the memory, and GetOrCreateSession forwards them to
// the session's SegmentedMemory.
func TestMessageLabels_AgentWiringJoinsFolds(t *testing.T) {
	recorder := &recordingEventLog{}
	sessions := newSQLiteSessions(t)
	ag := newLabeledEventLogAgentWithJoin(t, &mockSimpleLLM{}, sessions, recorder, labelProseRow, concatJoin)

	// Size a new session's window so the prose mass is over the relief start
	// mark, then create it — the options must already be on the memory.
	ag.memory.SetContextLimits(6000, 600)
	sess := ag.memory.GetOrCreateSession(context.Background(), "s-labels-agent")
	segMem, ok := sess.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	segMem.SetThreshold(6000)
	segMem.SetCompressor(&foldCompressor{out: "covers msg:1-99\nsummary of the analysis prose"})

	ctx := context.Background()
	persist := func(role, content, toolUseID string, calls []ToolCall, turnStart bool) {
		m := Message{Role: role, Content: content, ToolUseID: toolUseID, ToolCalls: calls}
		require.NoError(t, sessions.SaveMessage(ctx, "s-labels-agent", &m, turnStart))
		segMem.AddMessage(ctx, m)
	}
	persist("user", "TICKET: build the marts", "", nil, true)
	for i := 0; i < 5; i++ {
		persist("assistant", strings.Repeat(fmt.Sprintf("analysis %d of models io=17 cpu=23; ", i), 120), "", nil, false)
	}
	persist("assistant", "", "", []ToolCall{{ID: "cLast", Name: "bulk_scan", Input: map[string]interface{}{}}}, false)
	persist("tool", "small result", "cLast", nil, false)

	shed, _, _ := segMem.ReleasePressure(ctx, 0)
	require.True(t, shed, "pressure must shed via fold")
	require.Equal(t, map[string]string{"tier": "t0,t1,t2,t3,t4"}, summaryMessageOf(t, segMem).Labels,
		"the option-wired join must drive the fold's label join")
}

// Memory.SetLabelJoin fans out onto sessions that already exist: a session
// created before the call folds with the new join, not the default.
func TestMessageLabels_SetLabelJoinReachesExistingSession(t *testing.T) {
	m := NewMemory()
	m.SetContextLimits(6000, 600)

	ctx := context.Background()
	sess := m.GetOrCreateSession(ctx, "sess-fanout")
	segMem, ok := sess.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	segMem.SetThreshold(6000)
	segMem.SetCompressor(&foldCompressor{out: "covers msg:1-99\nsummary of the analysis prose"})

	// Labels ride the messages themselves; no labeler is wired here. The
	// first two tiers are out of order on purpose: concatJoin preserves join
	// order, the default join re-sorts — so this test discriminates the wired
	// join from the default, which post-F2 agrees with concatJoin on sorted
	// input.
	tiers := []string{"t1", "t0", "t2", "t3", "t4"}
	for i, tier := range tiers {
		segMem.AddMessage(ctx, Message{
			Role:    "assistant",
			Content: strings.Repeat(fmt.Sprintf("analysis %d of models io=17 cpu=23; ", i), 120),
			Labels:  map[string]string{"tier": tier},
		})
	}
	segMem.AddMessage(ctx, Message{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "cLast", Name: "bulk_scan", Input: map[string]interface{}{}}}})
	segMem.AddMessage(ctx, Message{Role: "tool", Content: "small result", ToolUseID: "cLast"})

	// The join arrives AFTER the session exists — the fan-out under test.
	m.SetLabelJoin(concatJoin)

	shed, _, _ := segMem.ReleasePressure(ctx, 0)
	require.True(t, shed, "pressure must shed via fold")
	require.Equal(t, map[string]string{"tier": "t1,t0,t2,t3,t4"}, summaryMessageOf(t, segMem).Labels,
		"SetLabelJoin must reach a session created before the call")
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

// The fold snapshot must carry the summary's joined labels: after a fold, a
// restore (replay + summary reload) into a fresh memory reinstates the same
// summary labels, and a fold after the restore keeps the pre-restart values.
func TestMessageLabels_SurviveRestore(t *testing.T) {
	live, store := labeledFoldFixtureWithStore(t, concatJoin, nil)
	ctx := context.Background()
	joined := map[string]string{"tier": "t0,t1,t2,t3,t4"}

	shed, _, _ := live.ReleasePressure(ctx, 0)
	require.True(t, shed, "pressure must shed via fold")
	require.Equal(t, joined, summaryMessageOf(t, live).Labels)

	// A fresh process: replay the surviving rows into a new memory over the
	// same store, then reload the newest fold snapshot as the summary.
	fresh := NewSegmentedMemory("ROM", 6000, 600)
	fresh.SetThreshold(6000)
	fresh.SetSessionStore(store, "sess-labels")
	fresh.SetCompressor(&foldCompressor{out: "covers msg:1-99\nsummary of the analysis prose"})
	fresh.SetMessageLabeler(labelProseRow)
	// The join is pinned so the fold-after-restore assertion isolates label
	// survival, not default-join semantics.
	fresh.SetLabelJoin(concatJoin)

	rows, err := store.LoadMessages(ctx, "sess-labels")
	require.NoError(t, err)
	fresh.ReplayMessages(ctx, rows)
	m := NewMemory()
	m.loadSummary(ctx, store, "sess-labels", fresh)

	require.Equal(t, joined, summaryMessageOf(t, fresh).Labels,
		"the restored summary must carry the pre-restart labels")

	// A fold after the restore keeps the pre-restart label values: more
	// labelled pressure mass enters turn 2, and the fold joins it on top of
	// the restored summary's labels.
	persist := func(role, content, toolUseID string, calls []ToolCall, turnStart bool) {
		msg := Message{Role: role, Content: content, ToolUseID: toolUseID, ToolCalls: calls}
		require.NoError(t, store.SaveMessage(ctx, "sess-labels", &msg, turnStart))
		fresh.AddMessage(ctx, msg)
	}
	persist("user", "TICKET 2: build the marts", "", nil, true)
	for i := 5; i < 10; i++ {
		persist("assistant", strings.Repeat(fmt.Sprintf("analysis %d of models io=17 cpu=23; ", i), 120), "", nil, false)
	}
	persist("assistant", "", "", []ToolCall{{ID: "cLast2", Name: "bulk_scan", Input: map[string]interface{}{}}}, false)
	persist("tool", "small result", "cLast2", nil, false)

	shed2, _, _ := fresh.ReleasePressure(ctx, 0)
	require.True(t, shed2, "the post-restore pressure must shed via fold")
	require.Contains(t, summaryMessageOf(t, fresh).Labels["tier"], "t0,t1,t2,t3,t4",
		"the pre-restart label value must survive the post-restore fold")
}

// Old fold snapshots (written before the snapshot carried labels) load as
// before: a snapshot JSON without a "labels" key restores a summary with nil
// labels and renders normally.
func TestMessageLabels_SurviveRestore_OldSnapshotLoadsAsNil(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	const sessionID = "sess-old"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))
	require.NoError(t, store.SaveMemorySnapshot(ctx, sessionID, "fold",
		`{"n":1,"text":"covers msg:1-99\nsummary"}`, 10))

	fresh := NewSegmentedMemory("ROM", 6000, 600)
	fresh.SetSessionStore(store, sessionID)
	m := NewMemory()
	m.loadSummary(ctx, store, sessionID, fresh)

	compiled := fresh.GetMessagesForLLM()
	require.GreaterOrEqual(t, len(compiled), 2)
	require.Contains(t, compiled[1].Content, "covers msg:")
	require.Nil(t, compiled[1].Labels,
		"a pre-labels snapshot restores nil labels, exactly as before")
}
