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
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// flightTracker records, across every probe that shares it, the start/end of
// each execution in order and the peak number of executions in flight.
type flightTracker struct {
	active atomic.Int32
	peak   atomic.Int32

	mu     sync.Mutex
	events []string
}

func (f *flightTracker) enter(label string) {
	n := f.active.Add(1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	f.mu.Lock()
	f.events = append(f.events, "start:"+label)
	f.mu.Unlock()
}

func (f *flightTracker) leave(label string) {
	f.mu.Lock()
	f.events = append(f.events, "end:"+label)
	f.mu.Unlock()
	f.active.Add(-1)
}

func (f *flightTracker) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

// parallelProbe is a recording tool: it sleeps delay, reports to its tracker
// and declares ConcurrencySafe as safe.
type parallelProbe struct {
	name  string
	safe  bool
	delay time.Duration
	tr    *flightTracker
	fn    func(v string) (*shuttle.Result, error)
	runs  atomic.Int32
	gate  *gate
}

func (p *parallelProbe) Name() string        { return p.name }
func (p *parallelProbe) Description() string { return "parallel probe" }
func (p *parallelProbe) Backend() string     { return "" }
func (p *parallelProbe) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object", Properties: map[string]*shuttle.JSONSchema{"v": {Type: "string"}}}
}
func (p *parallelProbe) ConcurrencySafe() bool { return p.safe }
func (p *parallelProbe) Execute(_ context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	v, _ := params["v"].(string)
	p.runs.Add(1)
	label := p.name + ":" + v
	p.tr.enter(label)
	defer p.tr.leave(label)
	if p.gate != nil {
		p.gate.started <- label
		<-p.gate.release
	}
	time.Sleep(p.delay)
	if p.fn != nil {
		return p.fn(v)
	}
	return &shuttle.Result{Success: true, Data: "ran:" + v}, nil
}

// gate holds every execution of the probes sharing it until release is
// closed, after signalling on started, so a test sees calls in flight together
// without relying on timing.
type gate struct {
	started chan string
	release chan struct{}
}

func newGate() *gate {
	return &gate{started: make(chan string, 64), release: make(chan struct{})}
}

// chatHeld runs one Chat while g holds every execution, requires n executions
// in flight at once, then releases them. A dispatch that never overlaps n calls
// fails after the safety timeout instead of hanging.
func chatHeld(t *testing.T, ag *Agent, sid string, g *gate, n int) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := ag.Chat(context.Background(), sid, "go")
		done <- err
	}()
	got := 0
	timeout := time.After(10 * time.Second)
wait:
	for got < n {
		select {
		case <-g.started:
			got++
		case <-timeout:
			break wait
		}
	}
	close(g.release)
	require.NoError(t, <-done)
	require.Equal(t, n, got, "calls in flight at once before release")
}

// noPrompts is a prompt registry with no entries, so a PromptAwareTool falls
// back to the wrapped tool's own description.
type noPrompts struct{ prompts.PromptRegistry }

func (noPrompts) Get(context.Context, string, map[string]interface{}) (string, error) {
	return "", errors.New("no prompt")
}

// parallelAgent builds an agent with behavior.parallel_read_tools on (mod may
// change any field, the flag included) over a scripted LLM.
func parallelAgent(t *testing.T, mod func(*Config), responses []mockLLMResponse, opts ...Option) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.ParallelReadTools = true
	if mod != nil {
		mod(cfg)
	}
	llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{responses: responses}}
	return NewAgent(&mockBackend{}, llm, append([]Option{WithConfig(cfg)}, opts...)...)
}

func batchOf(calls ...llmtypes.ToolCall) []mockLLMResponse {
	return []mockLLMResponse{{toolCalls: calls}, {content: "done"}}
}

func TestParallel_OffByDefault_Sequential(t *testing.T) {
	tr := &flightTracker{}
	r := &parallelProbe{name: "read", safe: true, delay: 50 * time.Millisecond, tr: tr}
	ag := parallelAgent(t, func(c *Config) { c.ParallelReadTools = false },
		batchOf(probeCall("id-1", "read", "1"), probeCall("id-2", "read", "2")))
	ag.RegisterTool(r)

	_, err := ag.Chat(context.Background(), "par-off", "go")
	require.NoError(t, err)
	assert.EqualValues(t, 1, tr.peak.Load(), "flag unset: no two calls overlap")
	assert.Equal(t, []string{"id-1", "id-2"}, toolRowIDs(toolRows(t, ag, "par-off")))
}

func TestParallel_ReadsOverlap(t *testing.T) {
	tr := &flightTracker{}
	g := newGate()
	plain := &parallelProbe{name: "read_a", safe: true, tr: tr, gate: g}
	inner := &parallelProbe{name: "read_b", safe: true, tr: tr, gate: g}
	ag := parallelAgent(t, nil, batchOf(probeCall("id-1", "read_a", "1"), probeCall("id-2", "read_b", "2")))
	ag.RegisterTool(plain)
	ag.RegisterTool(shuttle.NewPromptAwareTool(inner, noPrompts{}, "tools.read_b"))

	chatHeld(t, ag, "par-overlap", g, 2)
	assert.EqualValues(t, 2, tr.peak.Load(), "both reads, one of them prompt-wrapped, run at once")
	assert.Equal(t, []string{"id-1", "id-2"}, toolRowIDs(toolRows(t, ag, "par-overlap")))
}

func TestParallel_RowsCommittedInCallOrder(t *testing.T) {
	tr := &flightTracker{}
	slow := &parallelProbe{name: "slow_read", safe: true, delay: 300 * time.Millisecond, tr: tr}
	fast := &parallelProbe{name: "fast_read", safe: true, delay: 20 * time.Millisecond, tr: tr}
	ag := parallelAgent(t, nil, batchOf(probeCall("id-A", "slow_read", "A"), probeCall("id-B", "fast_read", "B")))
	ag.RegisterTool(slow)
	ag.RegisterTool(fast)

	resp, err := ag.Chat(context.Background(), "par-order", "go")
	require.NoError(t, err)

	log := tr.log()
	require.Len(t, log, 4)
	assert.Equal(t, "end:fast_read:B", log[2], "the fast second call finishes first: %v", log)

	rows := toolRows(t, ag, "par-order")
	require.Equal(t, []string{"id-A", "id-B"}, toolRowIDs(rows), "rows commit in call order, not completion order")
	assert.Contains(t, rows[0].Content, "ran:A")
	assert.Contains(t, rows[1].Content, "ran:B")
	require.Len(t, resp.ToolExecutions, 2)
	assert.Equal(t, "A", resp.ToolExecutions[0].Input["v"])
	assert.Equal(t, "B", resp.ToolExecutions[1].Input["v"])
}

func TestParallel_OneFailureOthersCommit(t *testing.T) {
	tr := &flightTracker{}
	r := &parallelProbe{name: "read", safe: true, delay: 50 * time.Millisecond, tr: tr, fn: func(v string) (*shuttle.Result, error) {
		if v == "B" {
			return nil, errors.New("read B failed")
		}
		return &shuttle.Result{Success: true, Data: "ran:" + v}, nil
	}}
	ag := parallelAgent(t, nil, batchOf(probeCall("id-A", "read", "A"), probeCall("id-B", "read", "B"), probeCall("id-C", "read", "C")))
	ag.RegisterTool(r)

	_, err := ag.Chat(context.Background(), "par-fail", "go")
	require.NoError(t, err)
	assert.EqualValues(t, 3, tr.peak.Load())

	rows := toolRows(t, ag, "par-fail")
	require.Equal(t, []string{"id-A", "id-B", "id-C"}, toolRowIDs(rows))
	assert.Contains(t, rows[0].Content, "ran:A")
	assert.Contains(t, rows[1].Content, "read B failed")
	assert.Contains(t, rows[2].Content, "ran:C")
}

func TestParallel_PanickingToolBecomesErrorResult(t *testing.T) {
	tr := &flightTracker{}
	r := &parallelProbe{name: "read", safe: true, delay: 50 * time.Millisecond, tr: tr, fn: func(v string) (*shuttle.Result, error) {
		if v == "B" {
			panic("parser blew up")
		}
		return &shuttle.Result{Success: true, Data: "ran:" + v}, nil
	}}
	ag := parallelAgent(t, nil, batchOf(probeCall("id-A", "read", "A"), probeCall("id-B", "read", "B"), probeCall("id-C", "read", "C")))
	ag.RegisterTool(r)

	_, err := ag.Chat(context.Background(), "par-panic", "go")
	require.NoError(t, err)

	rows := toolRows(t, ag, "par-panic")
	require.Equal(t, []string{"id-A", "id-B", "id-C"}, toolRowIDs(rows))
	assert.Contains(t, rows[0].Content, "ran:A")
	assert.Contains(t, rows[1].Content, "tool read panicked: parser blew up")
	assert.Contains(t, rows[2].Content, "ran:C")
}

func TestParallel_IdenticalCallRunsOnce(t *testing.T) {
	tr := &flightTracker{}
	r := &parallelProbe{name: "read", safe: true, delay: 50 * time.Millisecond, tr: tr}
	ag := parallelAgent(t, nil, batchOf(probeCall("id-1", "read", "same"), probeCall("id-2", "read", "same")))
	ag.RegisterTool(r)

	_, err := ag.Chat(context.Background(), "par-dedup", "go")
	require.NoError(t, err)
	assert.EqualValues(t, 1, r.runs.Load(), "the identical second call replays the first result")

	rows := toolRows(t, ag, "par-dedup")
	require.Equal(t, []string{"id-1", "id-2"}, toolRowIDs(rows))
	assert.Contains(t, rows[1].Content, "ran:same")
	assert.Contains(t, rows[1].Content, "(deduplicated — reused result from identical call in this turn)")
}

// A call whose dedup key was cached by an earlier group must not join a later
// group: its replay row would commit in prepareCall before the earlier member
// of that group settles.
func TestParallel_CachedDedupAfterBarrierKeepsOrder(t *testing.T) {
	tr := &flightTracker{}
	r := &parallelProbe{name: "read", safe: true, delay: 30 * time.Millisecond, tr: tr}
	w := &parallelProbe{name: "write", safe: false, delay: 10 * time.Millisecond, tr: tr}
	ag := parallelAgent(t, nil, batchOf(probeCall("id-1", "read", "a"), probeCall("id-W", "write", "w"),
		probeCall("id-2", "read", "b"), probeCall("id-3", "read", "a")))
	ag.RegisterTool(r)
	ag.RegisterTool(w)

	_, err := ag.Chat(context.Background(), "par-cached-dedup", "go")
	require.NoError(t, err)
	assert.EqualValues(t, 2, r.runs.Load(), "the repeat of read a replays, it does not run")
	rows := toolRows(t, ag, "par-cached-dedup")
	require.Equal(t, []string{"id-1", "id-W", "id-2", "id-3"}, toolRowIDs(rows))
	assert.Contains(t, rows[3].Content, "(deduplicated — reused result from identical call in this turn)")
}

// A safe tool registered under a server-qualified name and called by its bare
// name resolves through the canonical-name fallback and still groups.
func TestParallel_CanonicalNameGroups(t *testing.T) {
	tr := &flightTracker{}
	r := &parallelProbe{name: "srv:lookup", safe: true, delay: 150 * time.Millisecond, tr: tr}
	ag := parallelAgent(t, nil, batchOf(probeCall("id-1", "lookup", "1"), probeCall("id-2", "lookup", "2"), probeCall("id-3", "lookup", "3")))
	ag.RegisterTool(r)

	_, err := ag.Chat(context.Background(), "par-canonical", "go")
	require.NoError(t, err)
	assert.EqualValues(t, 3, r.runs.Load())
	assert.EqualValues(t, 3, tr.peak.Load(), "bare-name calls to a safe qualified tool run at once")
	assert.Equal(t, []string{"id-1", "id-2", "id-3"}, toolRowIDs(toolRows(t, ag, "par-canonical")))
}

// A member that leaves through runtime.Goexit (recover cannot see it) settles
// as an error row, not as a nil result.
func TestParallel_GoexitMemberSettlesAsError(t *testing.T) {
	tr := &flightTracker{}
	r := &parallelProbe{name: "read", safe: true, delay: 20 * time.Millisecond, tr: tr, fn: func(v string) (*shuttle.Result, error) {
		if v == "B" {
			runtime.Goexit()
		}
		return &shuttle.Result{Success: true, Data: "ran:" + v}, nil
	}}
	ag := parallelAgent(t, nil, batchOf(probeCall("id-A", "read", "A"), probeCall("id-B", "read", "B"), probeCall("id-C", "read", "C")))
	ag.RegisterTool(r)

	resp, err := ag.Chat(context.Background(), "par-goexit", "go")
	require.NoError(t, err)
	rows := toolRows(t, ag, "par-goexit")
	require.Equal(t, []string{"id-A", "id-B", "id-C"}, toolRowIDs(rows))
	assert.Contains(t, rows[0].Content, "ran:A")
	assert.Contains(t, rows[1].Content, "tool read did not return")
	assert.Contains(t, rows[2].Content, "ran:C")
	require.Len(t, resp.ToolExecutions, 3)
	assert.Error(t, resp.ToolExecutions[1].Error)
}

func TestParallel_WriteIsBarrier(t *testing.T) {
	const sid = "par-barrier"
	tr := &flightTracker{}
	var ag *Agent
	var writeRowBeforeRead2 atomic.Bool
	r := &parallelProbe{name: "read", safe: true, delay: 50 * time.Millisecond, tr: tr}
	r.fn = func(v string) (*shuttle.Result, error) {
		if v == "2" {
			for _, id := range toolRowIDs(toolRows(t, ag, sid)) {
				if id == "id-W" {
					writeRowBeforeRead2.Store(true)
				}
			}
		}
		return &shuttle.Result{Success: true, Data: "ran:" + v}, nil
	}
	w := &parallelProbe{name: "write", safe: false, delay: 50 * time.Millisecond, tr: tr}
	ag = parallelAgent(t, nil, batchOf(probeCall("id-1", "read", "1"), probeCall("id-W", "write", "w"), probeCall("id-2", "read", "2")))
	ag.RegisterTool(r)
	ag.RegisterTool(w)

	_, err := ag.Chat(context.Background(), sid, "go")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"start:read:1", "end:read:1",
		"start:write:w", "end:write:w",
		"start:read:2", "end:read:2",
	}, tr.log(), "a write runs alone: the read before it has finished, the read after it waits")
	assert.True(t, writeRowBeforeRead2.Load(), "the write's row is committed before the second read starts")
	assert.Equal(t, []string{"id-1", "id-W", "id-2"}, toolRowIDs(toolRows(t, ag, sid)))
}

func TestParallel_ContactHumanStaysSequential(t *testing.T) {
	tr := &flightTracker{}
	r := &parallelProbe{name: "read", safe: true, delay: 50 * time.Millisecond, tr: tr}
	// A contact_human that (wrongly) declares itself safe still runs alone.
	h := &parallelProbe{name: "contact_human", safe: true, delay: 50 * time.Millisecond, tr: tr}
	ag := parallelAgent(t, nil, batchOf(probeCall("id-1", "read", "1"), probeCall("id-H", "contact_human", "h"), probeCall("id-2", "read", "2")))
	ag.RegisterTool(r)
	ag.RegisterTool(h)

	_, err := ag.Chat(context.Background(), "par-hitl", "go")
	require.NoError(t, err)
	assert.EqualValues(t, 1, tr.peak.Load(), "contact_human is a barrier")
	assert.Equal(t, []string{"id-1", "id-H", "id-2"}, toolRowIDs(toolRows(t, ag, "par-hitl")))
}

func TestParallel_CapRespected(t *testing.T) {
	tr := &flightTracker{}
	r := &parallelProbe{name: "read", safe: true, delay: 20 * time.Millisecond, tr: tr}
	ag := parallelAgent(t, func(c *Config) { c.MaxToolExecutions = 3 }, batchOf(
		probeCall("id-1", "read", "1"), probeCall("id-2", "read", "2"), probeCall("id-3", "read", "3"),
		probeCall("id-4", "read", "4"), probeCall("id-5", "read", "5")))
	ag.RegisterTool(r)

	resp, err := ag.Chat(context.Background(), "par-cap", "go")
	require.NoError(t, err)
	assert.EqualValues(t, 3, r.runs.Load(), "MaxToolExecutions: 3 executes exactly three")
	assert.Len(t, resp.ToolExecutions, 3)
	assert.Equal(t, []string{"id-1", "id-2", "id-3"}, toolRowIDs(toolRows(t, ag, "par-cap")))
}

func TestParallel_PerTurnCapKeepsRowOrder(t *testing.T) {
	tr := &flightTracker{}
	r := &parallelProbe{name: "read", safe: true, delay: 20 * time.Millisecond, tr: tr}
	ag := parallelAgent(t, func(c *Config) { c.MaxIterations = 2 }, batchOf(
		probeCall("id-1", "read", "1"), probeCall("id-2", "read", "2"),
		probeCall("id-3", "read", "3"), probeCall("id-4", "read", "4")))
	ag.RegisterTool(r)

	_, err := ag.Chat(context.Background(), "par-turncap", "go")
	require.NoError(t, err)
	assert.EqualValues(t, 2, r.runs.Load())

	rows := toolRows(t, ag, "par-turncap")
	require.Equal(t, []string{"id-1", "id-2", "id-3", "id-4"}, toolRowIDs(rows), "cap rows sit at their call positions")
	for i := 0; i < 2; i++ {
		require.NotNil(t, rows[i].ToolResult)
		assert.True(t, rows[i].ToolResult.Success, "row %d is a real result", i)
	}
	for i := 2; i < 4; i++ {
		require.NotNil(t, rows[i].ToolResult)
		require.NotNil(t, rows[i].ToolResult.Error)
		assert.Equal(t, "turn_limit_exceeded", rows[i].ToolResult.Error.Code, "row %d", i)
	}
}

// With the default serve config (tools.permissions.yolo) the chain holds only
// the name-level permission hook: it is order-independent, so reads still
// overlap.
func TestParallel_YoloPermissionChainStillParallel(t *testing.T) {
	chain, err := shuttle.BuildChainFromConfig(shuttle.HooksConfig{},
		shuttle.ChainDeps{Perm: shuttle.NewPermissionChecker(shuttle.PermissionConfig{YOLO: true})})
	require.NoError(t, err)
	g := newGate()
	r := &parallelProbe{name: "read", safe: true, tr: &flightTracker{}, gate: g}
	ag := parallelAgent(t, nil, batchOf(probeCall("id-1", "read", "1"), probeCall("id-2", "read", "2")),
		WithAdmissionHooks(chain))
	ag.RegisterTool(r)

	chatHeld(t, ag, "par-admission-yolo", g, 2)
	assert.Equal(t, []string{"id-1", "id-2"}, toolRowIDs(toolRows(t, ag, "par-admission-yolo")))
}

// An order-dependent admission chain makes every call a barrier.
func TestParallel_OrderDependentAdmissionForcesSequential(t *testing.T) {
	yolo := shuttle.NewPermissionChecker(shuttle.PermissionConfig{YOLO: true})
	cases := map[string]struct {
		hooks shuttle.HooksConfig
		perm  *shuttle.PermissionChecker
	}{
		// The binding scopes another tool, so the reads are admitted; its
		// recorder post-tool hook alone makes the chain order-dependent.
		"gated-allowlist": {hooks: shuttle.HooksConfig{Bindings: []shuttle.HookBinding{{
			Kind: "gated-allowlist", Scope: "run_sql", StateKey: "k", SourceTool: "render_sql", StmtParam: "sql",
		}}}, perm: yolo},
		"ask": {hooks: shuttle.HooksConfig{Bindings: []shuttle.HookBinding{{Kind: "ask", Scope: "other_tool"}}}, perm: yolo},
		// require_approval is the human-approval path of the permission hook.
		"permission-requires-approval": {perm: shuttle.NewPermissionChecker(shuttle.PermissionConfig{
			RequireApproval: true, AllowedTools: []string{"read"},
		})},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			chain, err := shuttle.BuildChainFromConfig(tc.hooks, shuttle.ChainDeps{Perm: tc.perm})
			require.NoError(t, err)
			tr := &flightTracker{}
			r := &parallelProbe{name: "read", safe: true, delay: 100 * time.Millisecond, tr: tr}
			ag := parallelAgent(t, nil, batchOf(probeCall("id-1", "read", "1"), probeCall("id-2", "read", "2")),
				WithAdmissionHooks(chain))
			ag.RegisterTool(r)

			_, err = ag.Chat(context.Background(), "par-admission-"+name, "go")
			require.NoError(t, err)
			assert.EqualValues(t, 2, r.runs.Load(), "both reads are admitted")
			assert.EqualValues(t, 1, tr.peak.Load(), "an order-dependent chain makes every call a barrier")
			assert.Equal(t, []string{"id-1", "id-2"}, toolRowIDs(toolRows(t, ag, "par-admission-"+name)))
		})
	}
}

// Each tool span records how the call was dispatched: group members carry
// dispatch.parallel=true and the group size, a call run alone carries why.
// With the flag off no dispatch attribute is added.
func TestParallel_ToolSpansRecordDispatch(t *testing.T) {
	run := func(t *testing.T, on bool) map[string]map[string]interface{} {
		tr := &flightTracker{}
		tracer := observability.NewMockTracer()
		ag := parallelAgent(t, func(c *Config) { c.ParallelReadTools = on }, batchOf(
			probeCall("id-1", "read_a", "1"), probeCall("id-2", "read_b", "2"),
			probeCall("id-W", "write", "w"), probeCall("id-3", "read_c", "3")),
			WithTracer(tracer))
		for _, name := range []string{"read_a", "read_b", "read_c"} {
			ag.RegisterTool(&parallelProbe{name: name, safe: true, tr: tr})
		}
		ag.RegisterTool(&parallelProbe{name: "write", safe: false, tr: tr})
		_, err := ag.Chat(context.Background(), "par-spans", "go")
		require.NoError(t, err)

		byTool := map[string]map[string]interface{}{}
		for _, sp := range tracer.GetSpansByName("agent.tool_execution") {
			byTool[sp.Attributes["tool_name"].(string)] = sp.Attributes
		}
		require.Len(t, byTool, 4)
		return byTool
	}

	t.Run("on", func(t *testing.T) {
		got := run(t, true)
		for _, name := range []string{"read_a", "read_b"} {
			assert.Equal(t, true, got[name]["dispatch.parallel"], name)
			assert.Equal(t, 2, got[name]["dispatch.group_size"], name)
			assert.NotContains(t, got[name], "dispatch.barrier_reason", name)
		}
		assert.Equal(t, false, got["write"]["dispatch.parallel"])
		assert.Equal(t, "not_concurrency_safe", got["write"]["dispatch.barrier_reason"])
		assert.NotContains(t, got["write"], "dispatch.group_size")
		assert.Equal(t, false, got["read_c"]["dispatch.parallel"])
		assert.Equal(t, "single", got["read_c"]["dispatch.barrier_reason"])
	})
	t.Run("off", func(t *testing.T) {
		for name, attrs := range run(t, false) {
			for k := range attrs {
				assert.NotContains(t, k, "dispatch.", "flag off adds no dispatch attribute (%s)", name)
			}
		}
	})
}

// The barrier reasons the spans report for calls run alone.
func TestParallel_BarrierReasons(t *testing.T) {
	tr := &flightTracker{}
	ag := parallelAgent(t, nil, nil)
	ag.RegisterTool(&parallelProbe{name: "read", safe: true, tr: tr})
	ag.RegisterTool(&parallelProbe{name: "write", safe: false, tr: tr})
	ag.RegisterTool(&parallelProbe{name: "contact_human", safe: true, tr: tr})
	st := &batchState{maxPerTurn: 10, turnDedup: map[string]*shuttle.Result{}, toolExecutionCount: new(int)}

	for _, tc := range []struct {
		calls  []ToolCall
		n      int
		reason string
	}{
		{[]ToolCall{{Name: "write"}}, 0, "not_concurrency_safe"},
		{[]ToolCall{{Name: "contact_human"}}, 0, "contact_human"},
		{[]ToolCall{{Name: "no_such_tool"}}, 0, "unresolved_tool"},
		{[]ToolCall{{Name: "read"}, {Name: "write"}}, 1, "single"},
		{[]ToolCall{{Name: "read"}, {Name: "read"}}, 1, "single"},
		{[]ToolCall{{Name: "read", Input: map[string]interface{}{"v": "1"}}, {Name: "read", Input: map[string]interface{}{"v": "2"}}}, 2, "single"},
	} {
		n, reason := ag.parallelGroupLen(tc.calls, st)
		assert.Equal(t, tc.n, n, "%v", tc.calls)
		assert.Equal(t, tc.reason, reason, "%v", tc.calls)
	}

	st.turnDedup["read|"+canonicalJSON(nil)] = &shuttle.Result{}
	// A call already cached this turn never joins a group. It replays without
	// opening a tool span, so no span reason names it (see
	// TestParallel_ReplayLeavesNoDispatchAttributes).
	n, reason := ag.parallelGroupLen([]ToolCall{{Name: "read"}}, st)
	assert.Equal(t, 0, n)
	assert.Equal(t, "single", reason)

	chain, err := shuttle.BuildChainFromConfig(shuttle.HooksConfig{Bindings: []shuttle.HookBinding{{Kind: "ask", Scope: "other_tool"}}}, shuttle.ChainDeps{})
	require.NoError(t, err)
	gated := parallelAgent(t, nil, nil, WithAdmissionHooks(chain))
	gated.RegisterTool(&parallelProbe{name: "read", safe: true, tr: tr})
	n, reason = gated.parallelGroupLen([]ToolCall{{Name: "read", Input: map[string]interface{}{"v": "1"}}, {Name: "read", Input: map[string]interface{}{"v": "2"}}}, st)
	assert.Equal(t, 0, n)
	assert.Equal(t, "admission_order_dependent", reason)
}

func TestParallel_UnknownToolIsBarrier(t *testing.T) {
	tr := &flightTracker{}
	r := &parallelProbe{name: "read", safe: true, delay: 100 * time.Millisecond, tr: tr}
	ag := parallelAgent(t, nil, batchOf(probeCall("id-1", "read", "1"), probeCall("id-X", "no_such_tool", "x"), probeCall("id-2", "read", "2")))
	ag.RegisterTool(r)

	_, err := ag.Chat(context.Background(), "par-unknown", "go")
	require.NoError(t, err)
	assert.EqualValues(t, 1, tr.peak.Load(), "an unresolved tool is not safe: it splits the reads")
	rows := toolRows(t, ag, "par-unknown")
	require.Equal(t, []string{"id-1", "id-X", "id-2"}, toolRowIDs(rows))
	assert.Contains(t, rows[1].Content, "not found")
}

func TestParallel_FanOutCapped(t *testing.T) {
	tr := &flightTracker{}
	g := newGate()
	r := &parallelProbe{name: "read", safe: true, tr: tr, gate: g}
	calls := []llmtypes.ToolCall{
		probeCall("id-1", "read", "1"), probeCall("id-2", "read", "2"), probeCall("id-3", "read", "3"),
		probeCall("id-4", "read", "4"), probeCall("id-5", "read", "5"), probeCall("id-6", "read", "6")}
	ag := parallelAgent(t, nil, batchOf(calls...))
	ag.RegisterTool(r)

	// The group is cut at maxParallelReads; members of the next group start
	// only after this one has settled.
	n, _ := ag.parallelGroupLen(calls, &batchState{maxPerTurn: 10, turnDedup: map[string]*shuttle.Result{}, toolExecutionCount: new(int)})
	require.Equal(t, maxParallelReads, n)

	chatHeld(t, ag, "par-fanout", g, maxParallelReads)
	assert.EqualValues(t, 6, r.runs.Load())
	assert.EqualValues(t, maxParallelReads, tr.peak.Load(), "never more than maxParallelReads in flight")
	assert.Equal(t, []string{"id-1", "id-2", "id-3", "id-4", "id-5", "id-6"}, toolRowIDs(toolRows(t, ag, "par-fanout")))
}

// The YAML key is the only way to opt in, and it reaches the agent's Config
// through the registry.
func TestParallelReadTools_OptIn(t *testing.T) {
	yamlFor := func(extra string) string {
		return `
agent:
  name: parallel_agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20241022
  behavior:
` + extra
	}
	registry, _ := createTestRegistry(t)
	build := func(extra string) *Agent {
		cfg, err := LoadConfigFromString(yamlFor(extra))
		require.NoError(t, err)
		cfg.Llm = createTestAgentConfig("x").Llm // the registry's mock provider
		ag, err := registry.buildAgent(context.Background(), cfg)
		require.NoError(t, err)
		return ag
	}

	assert.True(t, build("    parallel_read_tools: true\n").GetConfig().ParallelReadTools)
	assert.False(t, build("    parallel_read_tools: false\n").GetConfig().ParallelReadTools)
	assert.False(t, build("    max_turns: 5\n").GetConfig().ParallelReadTools)

	// Each key sets only its own field.
	on := build("    parallel_read_tools: true\n").GetConfig()
	assert.False(t, on.ToolChangeNote)
	assert.False(t, on.ContextReliefWarning)
	other := build("    tool_change_note: true\n").GetConfig()
	assert.False(t, other.ParallelReadTools)
}

// The documented barrier reasons are exactly the ones a span can carry. A
// deduplicated replay returns before its tool span opens, so it has no
// dispatch attributes and no reason of its own; the write keeps its reason.
func TestParallel_ReplayLeavesNoDispatchAttributes(t *testing.T) {
	tr := &flightTracker{}
	tracer := observability.NewMockTracer()
	ag := parallelAgent(t, nil, batchOf(
		probeCall("id-1", "read", "1"), probeCall("id-W", "write", "w"), probeCall("id-2", "read", "1")),
		WithTracer(tracer))
	read := &parallelProbe{name: "read", safe: true, tr: tr}
	ag.RegisterTool(read)
	ag.RegisterTool(&parallelProbe{name: "write", safe: false, tr: tr})

	_, err := ag.Chat(context.Background(), "par-replay", "go")
	require.NoError(t, err)
	assert.Equal(t, []string{"id-1", "id-W", "id-2"}, toolRowIDs(toolRows(t, ag, "par-replay")), "the replay still commits its row")
	assert.EqualValues(t, 1, read.runs.Load(), "the repeated read replayed from the turn cache")

	var reasons []interface{}
	spans := tracer.GetSpansByName("agent.tool_execution")
	for _, sp := range spans {
		reasons = append(reasons, sp.Attributes["dispatch.barrier_reason"])
	}
	require.Len(t, spans, 2, "the replay opens no tool span")
	assert.Equal(t, []interface{}{"single", "not_concurrency_safe"}, reasons)
}

// An embedder that sets only a require_approval checker (no admission chain)
// still gets sequential dispatch; the same checker under yolo stays parallel.
func TestParallel_CheckerWithoutChain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		yolo     bool
		wantN    int
		wantWhy  string
		wantPeak int32
	}{
		{"require_approval", false, 0, "admission_order_dependent", 1},
		{"yolo", true, 2, "single", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checker := shuttle.NewPermissionChecker(shuttle.PermissionConfig{RequireApproval: true, YOLO: tc.yolo, AllowedTools: []string{"read"}})
			tr := &flightTracker{}
			r := &parallelProbe{name: "read", safe: true, delay: 100 * time.Millisecond, tr: tr}
			ag := parallelAgent(t, nil, batchOf(probeCall("id-1", "read", "1"), probeCall("id-2", "read", "2")),
				WithPermissionChecker(checker))
			ag.RegisterTool(r)

			calls := []ToolCall{{Name: "read", Input: map[string]interface{}{"v": "1"}}, {Name: "read", Input: map[string]interface{}{"v": "2"}}}
			n, why := ag.parallelGroupLen(calls, &batchState{maxPerTurn: 10, turnDedup: map[string]*shuttle.Result{}, toolExecutionCount: new(int)})
			assert.Equal(t, tc.wantN, n)
			assert.Equal(t, tc.wantWhy, why)

			_, err := ag.Chat(context.Background(), "par-checker-"+tc.name, "go")
			require.NoError(t, err)
			assert.EqualValues(t, 2, r.runs.Load(), "both reads are admitted")
			assert.Equal(t, tc.wantPeak, tr.peak.Load())
		})
	}
}
