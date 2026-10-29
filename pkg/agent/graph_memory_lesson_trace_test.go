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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// assertSpanAttrs checks a set of attributes on a captured span.
func assertSpanAttrs(t *testing.T, span *observability.Span, want map[string]interface{}) {
	t.Helper()
	for k, v := range want {
		assert.Equal(t, v, span.Attributes[k], "span %s attribute %s", span.Name, k)
	}
}

// The error-triggered injection lane adds a system message mid-conversation;
// it must leave a span with what it recalled and injected (review:
// Josh-Schoen F3 — injection previously left no record anywhere).
func TestInjectErrorLessonsTraced(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	_, err := store.Remember(ctx, &memory.Memory{
		AgentID:       fleetLessonAgentID,
		Content:       "numeric overflow inserting CC_Number into an INTEGER column — declare the column BIGINT",
		MemoryType:    memory.MemoryTypeLesson,
		Source:        "lesson_mined",
		MemoryAgentID: "runner-4o-09",
		Salience:      0.9,
	})
	require.NoError(t, err)

	tracer := observability.NewMockTracer()
	a := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-01"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, FleetLessonSharing: true},
		tracer:            tracer,
	}
	session := &types.Session{ID: "s-trace-inject"}
	errText := "Error 2616: numeric overflow occurred during computation of CC_Number"

	a.injectErrorLessons(ctx, session, []string{errText})
	a.injectErrorLessons(ctx, session, []string{errText})  // repeat: nothing new
	a.injectErrorLessons(ctx, session, []string{"!!! ##"}) // no usable query terms

	spans := tracer.GetSpansByName(spanLessonInjection)
	require.Len(t, spans, 3)
	assertSpanAttrs(t, spans[0], map[string]interface{}{
		observability.AttrSessionID: "s-trace-inject",
		"lesson.outcome":            "injected",
		"lesson.errors":             1,
		"lesson.recalled":           1,
		"lesson.injected":           1,
		"lesson.injected_verified":  1,
		"lesson.injected_probation": 0,
	})
	assertSpanAttrs(t, spans[1], map[string]interface{}{
		"lesson.outcome":  "already_shown",
		"lesson.recalled": 1,
		"lesson.injected": 0,
	})
	assertSpanAttrs(t, spans[2], map[string]interface{}{
		"lesson.outcome":  "no_query",
		"lesson.recalled": 0,
		"lesson.injected": 0,
	})
}

// Outcome credit changes lesson salience; its span carries the win/loss
// tally so a demotion is visible in hawk, not only in logs.
func TestApplyLessonCreditTraced(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	mk := func(content string) string {
		m, err := store.Remember(ctx, &memory.Memory{
			AgentID:    fleetLessonAgentID,
			Content:    content,
			MemoryType: memory.MemoryTypeLesson,
			Source:     "lesson_mined",
			Salience:   0.8,
		})
		require.NoError(t, err)
		return m.ID
	}
	winID := mk("numeric overflow on card columns — declare card_id BIGINT")
	lossID := mk("numeric overflow — widen the DECIMAL precision")
	idleID := mk("numeric overflow — cast to FLOAT")

	tracer := observability.NewMockTracer()
	a := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-01"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, FleetLessonSharing: true},
		tracer:            tracer,
	}

	// One recovery after winID's injection; a failure after lossID's; idleID
	// was injected after the last event, so nothing scores it.
	events := []minedEvent{
		{key: "execute_statement:INSERT VT", ok: false, errText: "overflow"},
		{key: "execute_statement:INSERT VT", ok: true},
		{key: "execute_statement:INSERT VU", ok: false, errText: "overflow"},
		{key: "execute_statement:INSERT VU", ok: false, errText: "overflow"},
	}
	a.applyLessonCredit(ctx, events, map[string]int{winID: 1, lossID: 3, idleID: 4})

	// No injections: no span (nothing to score is not an operation).
	a.applyLessonCredit(ctx, events, nil)
	// Injections but an empty ledger: one span, all unscored.
	a.applyLessonCredit(ctx, nil, map[string]int{winID: 0})

	spans := tracer.GetSpansByName(spanLessonCredit)
	require.Len(t, spans, 2)
	assertSpanAttrs(t, spans[0], map[string]interface{}{
		"lesson.outcome":              "scored",
		"lesson.injections":           3,
		"lesson.ledger_events":        4,
		"lesson.credit.wins":          1,
		"lesson.credit.losses":        1,
		"lesson.credit.unscored":      1,
		"lesson.credit.adjust_errors": 0,
	})
	assertSpanAttrs(t, spans[1], map[string]interface{}{
		"lesson.outcome":         "no_events",
		"lesson.injections":      1,
		"lesson.credit.unscored": 1,
		"lesson.credit.wins":     0,
		"lesson.credit.losses":   0,
	})
}

// The mining pass makes an LLM call and writes lessons other conversations
// will be shown; its span carries the pair/stored/dropped counts, and the
// credit pass it runs first is recorded as its child.
func TestExtractLessonsAtEndTraced(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	mockLLM := &extractionMockLLM{response: `{"entities":[],"relationships":[],"memories":[
	  {"content":"16-digit identifiers overflow an INTEGER column — declare such columns BIGINT","summary":"declare BIGINT","memory_type":"lesson","tags":["schema"],"salience":0.9},
	  {"content":"numeric overflow is really about the DECIMAL precision being too small","summary":"decimal theory","memory_type":"lesson","tags":["schema"],"salience":0.95}
	]}`}

	tracer := observability.NewMockTracer()
	a := &Agent{
		llm:                         mockLLM,
		graphMemoryStore:            store,
		enableGraphMemoryExtraction: true,
		graphMemoryConfig:           &loomv1.GraphMemoryConfig{Enabled: true},
		memory:                      NewMemory(),
		config:                      &Config{Name: "runner-4o-09"},
		tracer:                      tracer,
	}

	create := func(colType string) types.ToolCall {
		return types.ToolCall{Name: "execute_statement", Input: map[string]interface{}{
			"sql": "CREATE VOLATILE TABLE vt_card_day (card_id " + colType + ", amt DECIMAL(14,2))"}}
	}
	insert := types.ToolCall{Name: "execute_statement", Input: map[string]interface{}{
		"sql": "INSERT INTO vt_card_day SELECT CC_Number, SUM(Amount) FROM txns GROUP BY 1"}}

	a.recordToolLedger("s-trace-mine", create("INTEGER"), &shuttle.Result{Success: true})
	a.recordToolLedger("s-trace-mine", insert, &shuttle.Result{Success: false,
		Error: &shuttle.Error{Code: "db_error", Message: "[Error 2616] Numeric overflow occurred during computation."}})
	// An injection landed here (ledger position 2), so credit has work too.
	a.errorLessonMu.Lock()
	a.errorLessonState = map[string]*errorLessonSession{
		"s-trace-mine": {injected: map[string]int{"lesson-that-was-forgotten": 2}, injections: 1},
	}
	a.errorLessonMu.Unlock()
	a.recordToolLedger("s-trace-mine", types.ToolCall{Name: "execute_statement",
		Input: map[string]interface{}{"sql": "DROP TABLE vt_card_day"}}, &shuttle.Result{Success: true})
	a.recordToolLedger("s-trace-mine", create("BIGINT"), &shuttle.Result{Success: true})
	a.recordToolLedger("s-trace-mine", insert, &shuttle.Result{Success: true,
		Data: `{"activity_count":55316,"status":"success"}`})

	a.extractLessonsAtEnd(ctx, "s-trace-mine")
	// Second pass: the ledger was consumed, so nothing is mined.
	a.extractLessonsAtEnd(ctx, "s-trace-mine")

	mining := tracer.GetSpansByName(spanLessonMining)
	require.Len(t, mining, 2)
	assertSpanAttrs(t, mining[0], map[string]interface{}{
		observability.AttrSessionID: "s-trace-mine",
		"lesson.outcome":            "mined",
		"lesson.ledger_events":      5,
		"lesson.pairs":              1,
		"lesson.pairs_grounded":     1,
		"lesson.stored":             1,
		"lesson.dropped_ungrounded": 1,
		"lesson.store_errors":       0,
	})
	assertSpanAttrs(t, mining[1], map[string]interface{}{
		"lesson.outcome":       "no_grounded_pairs",
		"lesson.ledger_events": 0,
		"lesson.stored":        0,
	})

	credit := tracer.GetSpansByName(spanLessonCredit)
	require.Len(t, credit, 1, "credit runs only when the error lane injected something")
	assert.Equal(t, mining[0].SpanID, credit[0].ParentID, "credit is a child of the mining pass")
	assert.Equal(t, 1, credit[0].Attributes["lesson.injections"])
	// A win against a lesson the store no longer has: AdjustSalience is a
	// no-op for unknown IDs, so it counts as a win, not an adjust error.
	assert.Equal(t, 1, credit[0].Attributes["lesson.credit.wins"])
}

// Agents built as struct literals carry no tracer; the lesson lane must not
// panic on them.
func TestLessonTracerNilSafe(t *testing.T) {
	a := &Agent{}
	require.NotNil(t, a.lessonTracer())
	tracer := observability.NewMockTracer()
	a.tracer = tracer
	assert.Same(t, tracer, a.lessonTracer())
}
