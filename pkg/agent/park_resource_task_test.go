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

// The task lifecycle across a RESOURCE-await park.
//
// park_task_lifecycle_test.go pins this for the HITL park: a parked turn has
// not finished, so its implicit task must stay open rather than be closed as
// "Turn ended with an error." Resource-await park (#386) landed afterwards and
// is a SECOND way to end a turn early, on a different trigger — a successful
// tool result that carries AwaitResource — with no human asked to decide
// anything.
//
// It inherits the protection today only because it reuses *TurnParkedError and
// chat()'s exception is written as errors.As on that type rather than as a
// check on the park reason. That is the right shape, but nothing pinned it:
// park_resource_test.go never constructs a task board, and the task lifecycle
// tests never park on a resource, so the two features were only ever tested
// apart. Giving resource-await its own terminal type — a reasonable-looking
// refactor, since the two parks differ in whether a human is involved — would
// silently restore the closed-too-early bug on the newer path and every
// existing test would still pass.
//
// These tests are that pin. They assert the property, not the mechanism, so
// they stay honest if the mechanism changes.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	sqlitestore "github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
)

// newResourceTaskRig is newParkTaskRig with a resource-await handler wired and
// the approval hook left off, so the only thing that can end the turn early is
// the resource hold. It returns the same *parkTaskRig so boardTasks/onlyTask/
// parkAndAssert read exactly as they do for the HITL park — the point being
// that the assertions are shared, not re-derived.
//
// The awaiting tool is returned separately because parkTaskRig.tools holds
// *countingTool and this one is an *awaitingTool (defined in
// park_resource_test.go).
func newResourceTaskRig(t *testing.T, responses []mockLLMResponse, awaitURI string,
	plainTools ...string) (*parkTaskRig, *awaitingTool, *fakeAwaitHandler) {
	t.Helper()

	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	sessions, err := NewSessionStore(filepath.Join(dir, "sessions.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessions.Close() })

	mgr := task.NewManager(
		sqlitestore.NewTaskStore(openMigratedTaskDB(t, filepath.Join(dir, "tasks.db")),
			observability.NewNoOpTracer()),
		nil, observability.NewNoOpTracer(), nil)

	parkStore := shuttle.NewInMemoryHumanRequestStore()
	handler := &fakeAwaitHandler{}
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{responses: responses},
		WithConfig(cfg),
		WithMemory(NewMemoryWithStore(sessions)),
		WithHITLPark(parkStore, 0, NewProgressNotifier()),
		WithResourceAwait(handler),
		WithTaskBoard(mgr, nil, &loomv1.TaskBoardConfig{}),
	)

	tools := make(map[string]*countingTool, len(plainTools))
	for _, n := range plainTools {
		ct := &countingTool{name: n}
		ag.RegisterTool(ct)
		tools[n] = ct
	}
	await := &awaitingTool{name: "start_job", uri: awaitURI}
	ag.RegisterTool(await)

	return &parkTaskRig{ag: ag, park: parkStore, sessions: sessions, tasks: mgr, tools: tools},
		await, handler
}

// workThenAwaitScript: an ordinary call runs first, which is what fires
// TOOL_CALL and mints the turn's task, and only then does the awaiting call
// hold and park. Ordering it this way means the test still has a task to
// assert about even if the awaiting call's own trigger placement changes.
func workThenAwaitScript() []mockLLMResponse {
	return []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{
			{ID: "c-read", Name: "read_table", Input: map[string]interface{}{"v": "1"}},
		}},
		{toolCalls: []llmtypes.ToolCall{
			{ID: "c-job", Name: "start_job", Input: map[string]interface{}{"v": "go"}},
		}},
		{content: "job finished"},
	}
}

// TestResourceAwaitPark_TaskStaysOpenWhileTheResourceIsAwaited is the
// regression pin. A turn awaiting a resource is unfinished for the same reason
// a turn awaiting a human is: the work it represents has not concluded. Closing
// its task reports the opposite twice over — DONE, and captioned as an error,
// because implicitCloseReason reads a park terminal as a failure.
func TestResourceAwaitPark_TaskStaysOpenWhileTheResourceIsAwaited(t *testing.T) {
	r, await, handler := newResourceTaskRig(t, workThenAwaitScript(), "job://j-1", "read_table")

	hr := r.parkAndAssert(t, "s-res-open", "read then start the job")

	// Rig sanity: without these the assertion below could pass for the wrong
	// reason — a turn that never ran anything records no task, and a turn that
	// never held would not be a resource park at all.
	require.EqualValues(t, 1, r.tools["read_table"].runs.Load(), "the pre-park work ran")
	require.EqualValues(t, 1, await.runs.Load(), "the awaiting tool executed before it held")
	require.Len(t, handler.prepared, 1, "the embedder was asked to wait before the park committed")
	require.Equal(t, "resource", hr.Kind, "this must be the resource park, not an approval park")

	tk := r.onlyTask(t, "s-res-open")
	if task.IsTerminal(tk.Status) {
		t.Fatalf("resource-parked task is %s with close_reason %q: the turn is awaiting a "+
			"resource, so its task must stay open until the resumed turn finishes",
			tk.Status, tk.CloseReason)
	}
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS, tk.Status)
	require.Empty(t, tk.CloseReason,
		"a turn awaiting a resource must not be captioned as ended, least of all as an error")
}

// TestResourceAwaitPark_RaisesTheSharedParkTerminal states the coupling the
// test above depends on, so a change to it fails here with a message naming the
// consequence instead of only failing the lifecycle assertion.
func TestResourceAwaitPark_RaisesTheSharedParkTerminal(t *testing.T) {
	r, _, _ := newResourceTaskRig(t, workThenAwaitScript(), "job://j-1", "read_table")

	_, err := r.ag.Chat(context.Background(), "s-res-terminal", "read then start the job")

	var parked *TurnParkedError
	require.Truef(t, errors.As(err, &parked),
		"resource-await park must raise *TurnParkedError; chat()'s implicit-task close "+
			"exception is an errors.As check on that type, so a distinct terminal here "+
			"would close the turn's task while the resource is still being awaited. "+
			"got %T: %v", err, err)
}
