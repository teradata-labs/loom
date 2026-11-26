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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/eventlog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// toolEventTrail lists sessionID's tool-call events in the order they were
// recorded, as "intent:<id>@<pos>" / "result:<id>@<pos>".
func toolEventTrail(rec *recordingEventLog, sessionID string) []string {
	var out []string
	for _, e := range rec.AllRecordedEvents(sessionID) {
		switch e.EventType {
		case eventlog.EventToolCallIntent:
			out = append(out, fmt.Sprintf("intent:%s@%d", e.ToolUseID, e.BatchPosition))
		case eventlog.EventToolCallResult:
			out = append(out, fmt.Sprintf("result:%s@%d", e.ToolUseID, e.BatchPosition))
		}
	}
	return out
}

// A group records every member's intent, in call order, before any member
// runs, and every result in call order after all have run. read_a is the
// slower member, so recording a result inside the goroutines would put id-2's
// result first.
func TestParallel_EventLog_IntentsThenResultsInCallOrder(t *testing.T) {
	rec := &recordingEventLog{}
	tr := &flightTracker{}
	g := newGate()
	slow := &parallelProbe{name: "read_a", safe: true, delay: 100 * time.Millisecond, tr: tr, gate: g}
	fast := &parallelProbe{name: "read_b", safe: true, tr: tr, gate: g}
	ag := parallelAgent(t, nil,
		batchOf(probeCall("id-1", "read_a", "1"), probeCall("id-2", "read_b", "2")),
		WithEventLog(rec))
	ag.RegisterTool(slow)
	ag.RegisterTool(fast)

	chatHeld(t, ag, "par-evlog", g, 2)

	assert.Equal(t,
		[]string{"intent:id-1@0", "intent:id-2@1", "result:id-1@0", "result:id-2@1"},
		toolEventTrail(rec, "par-evlog"))
	live, err := rec.LoadEvents(context.Background(), "par-evlog")
	require.NoError(t, err)
	assert.Empty(t, live, "a finished turn's events are pruned")
}

// intentFailingLog refuses the tool-call intent at one batch position.
type intentFailingLog struct {
	*recordingEventLog
	failPos int
}

func (l *intentFailingLog) RecordIntent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	if eventType == eventlog.EventToolCallIntent && batchPosition == l.failPos {
		return errors.New("injected: intent write failed")
	}
	return l.recordingEventLog.RecordIntent(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload)
}

// When a group member's intent cannot be recorded, its error row must still
// land after the rows of the members before it. Both calls share an ID, as
// Gemini's parallel calls to one tool do: resume credits rows to calls in
// order within an ID, and Gemini and Ollama pair results with calls by order,
// so a row out of call order is credited to the wrong call.
func TestParallel_IntentFailureRow_LandsInCallOrder(t *testing.T) {
	log := &intentFailingLog{recordingEventLog: &recordingEventLog{}, failPos: 1}
	tr := &flightTracker{}
	read := &parallelProbe{name: "read", safe: true, tr: tr}
	ag := parallelAgent(t, nil,
		batchOf(probeCall("read", "read", "1"), probeCall("read", "read", "2")),
		WithEventLog(log))
	ag.RegisterTool(read)

	_, err := ag.Chat(context.Background(), "par-intent-fail", "go")
	require.NoError(t, err)

	rows := toolRows(t, ag, "par-intent-fail")
	require.Len(t, rows, 2)
	require.NotNil(t, rows[0].ToolResult)
	assert.True(t, rows[0].ToolResult.Success, "row 0 is the call that ran")
	assert.Equal(t, "ran:1", rows[0].ToolResult.Data)
	require.NotNil(t, rows[1].ToolResult)
	require.NotNil(t, rows[1].ToolResult.Error)
	assert.Equal(t, "event_log_intent_failed", rows[1].ToolResult.Error.Code)
	assert.EqualValues(t, 1, read.runs.Load(), "the call without an intent ran anyway")
}

// resultDroppingLog stops recording tool results once crashed is set: the
// process died before any result write reached the store.
type resultDroppingLog struct {
	*recordingEventLog
	crashed atomic.Bool
}

func (l *resultDroppingLog) RecordResult(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	if l.crashed.Load() && eventType == eventlog.EventToolCallResult {
		return errors.New("injected: process died")
	}
	return l.recordingEventLog.RecordResult(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload)
}

// A crash while a group runs leaves one execution gap per member. Resume
// reports them one at a time, in call order, and finishes the turn once each
// is resolved, without running any member again.
func TestResumeSession_CrashDuringParallelGroup_ReportsEachGapInCallOrder(t *testing.T) {
	recorder := &recordingEventLog{}
	crashing := &resultDroppingLog{recordingEventLog: recorder}
	sessions := newSQLiteSessions(t)
	rowsLost := &toolRowFailingStore{SessionStorage: sessions, failTools: true}

	turnCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := newGate()
	tr := &flightTracker{}
	crashWhileRunning := func(v string) (*shuttle.Result, error) {
		crashing.crashed.Store(true)
		cancel()
		return &shuttle.Result{Success: true, Data: "ran:" + v}, nil
	}
	readA := &parallelProbe{name: "read_a", safe: true, tr: tr, gate: g, fn: crashWhileRunning}
	readB := &parallelProbe{name: "read_b", safe: true, tr: tr, gate: g, fn: crashWhileRunning}
	llm := &scriptedLLM{steps: []func(context.Context) (*llmtypes.LLMResponse, error){
		toolCallStep(probeCall("id-1", "read_a", "1"), probeCall("id-2", "read_b", "2")),
		crashStep(cancel),
	}}
	first := newEventLogAgent(t, llm, rowsLost, crashing, readA, readB)
	first.config.ParallelReadTools = true

	done := make(chan error, 1)
	go func() {
		_, err := first.Chat(turnCtx, "s-group-crash", "read both")
		done <- err
	}()
	for range 2 {
		select {
		case <-g.started:
		case <-time.After(10 * time.Second):
			t.Fatal("the group's members were not in flight together")
		}
	}
	close(g.release)
	require.Error(t, <-done, "the turn should end with the simulated crash")
	require.Equal(t, []string{"intent:id-1@0", "intent:id-2@1"}, toolEventTrail(recorder, "s-group-crash"))

	// A fresh process: new agent and memory over the same durable stores.
	ctx := context.Background()
	second := newEventLogAgent(t, &mockSimpleLLM{}, sessions, recorder, readA, readB)
	for _, want := range []struct {
		id  string
		pos int
	}{{"id-1", 0}, {"id-2", 1}} {
		_, err := second.ResumeSession(ctx, "s-group-crash")
		var gap *ExecutionGap
		require.ErrorAs(t, err, &gap)
		assert.Equal(t, want.id, gap.ToolUseID)
		assert.Equal(t, want.pos, gap.BatchPosition)
		_, err = second.ResolveExecutionGap(ctx, "s-group-crash", GapFailed, nil)
		require.NoError(t, err)
	}
	_, err := second.ResumeSession(ctx, "s-group-crash")
	require.NoError(t, err)
	assert.EqualValues(t, 1, readA.runs.Load(), "resume ran read_a again")
	assert.EqualValues(t, 1, readB.runs.Load(), "resume ran read_b again")
}

// A call whose intent could not be recorded never runs, and its span must say
// so: an error status, error.code and success=false on the tool span, and a
// failed turn.tool_execution event on the loop span. Tracers score a span
// without an error status as a success. In a group, the member also carries
// the group's dispatch attributes.
func TestIntentFailure_TracedAsFailedCall(t *testing.T) {
	for name, parallel := range map[string]bool{"sequential": false, "group": true} {
		t.Run(name, func(t *testing.T) {
			log := &intentFailingLog{recordingEventLog: &recordingEventLog{}, failPos: 1}
			tracer := observability.NewMockTracer()
			tr := &flightTracker{}
			ag := parallelAgent(t, func(c *Config) { c.ParallelReadTools = parallel },
				batchOf(probeCall("id-1", "read_a", "1"), probeCall("id-2", "read_b", "2")),
				WithEventLog(log), WithTracer(tracer))
			ag.RegisterTool(&parallelProbe{name: "read_a", safe: true, tr: tr})
			ag.RegisterTool(&parallelProbe{name: "read_b", safe: true, tr: tr})

			_, err := ag.Chat(context.Background(), "intent-fail-trace", "go")
			require.NoError(t, err)

			var failed *observability.Span
			for _, sp := range tracer.GetSpansByName("agent.tool_execution") {
				if sp.Attributes["tool_name"] == "read_b" {
					failed = sp
				}
			}
			require.NotNil(t, failed, "read_b has a tool span")
			assert.Equal(t, observability.StatusError, failed.Status.Code)
			assert.Equal(t, "event_log_intent_failed", failed.Attributes["error.code"])
			assert.Equal(t, "false", failed.Attributes["success"])
			if parallel {
				assert.Equal(t, true, failed.Attributes["dispatch.parallel"])
				assert.Equal(t, 2, failed.Attributes["dispatch.group_size"])
			}

			var event map[string]interface{}
			for _, sp := range tracer.GetSpansByName("agent.conversation_loop") {
				for _, e := range sp.Events {
					if e.Name == "turn.tool_execution" && e.Attributes["tool_name"] == "read_b" {
						event = e.Attributes
					}
				}
			}
			require.NotNil(t, event, "read_b has a turn.tool_execution event")
			assert.Equal(t, false, event["success"])
			assert.Equal(t, 2, event["index"])
		})
	}
}
