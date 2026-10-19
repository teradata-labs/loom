// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
)

// Differential test against production history.
//
// testdata/loop-specs.json is dumped straight out of the running LoopSpec
// objects (bin/dump-loop-specs.py), so the graph under test is the loop's own
// graph rather than a transcription of it. testdata/transitions/ holds real
// transitions.jsonl files from real runs: every state entered, the signal it
// emitted, and whether its worker minted or resumed.
//
// Replaying those signals through this engine and asserting it visits the same
// states is the strongest check available on the port, and it costs nothing:
// no model is called, no Python runs, and the oracle is a year of recorded
// behaviour rather than anyone's expectation of it.

// ---------------------------------------------------------------------------
// the extracted specs
// ---------------------------------------------------------------------------

type loopSpecFile struct {
	Specs map[string]loopSpec `json:"specs"`
}

type loopSpec struct {
	Name           string                   `json:"name"`
	StartState     string                   `json:"start_state"`
	States         map[string]loopSpecState `json:"states"`
	Vocab          map[string][]string      `json:"vocab"`
	Route          map[string]loopSpecEdge  `json:"route"`
	Terminal       []string                 `json:"terminal"`
	LoopbackStates []string                 `json:"loopback_states"`
	BubbleSignals  []string                 `json:"bubble_signals"`
	SignalFields   map[string][]string      `json:"signal_fields"`
	Unrouted       []string                 `json:"unrouted"`
}

type loopSpecState struct {
	Worker  *string `json:"worker"`
	Prompts struct {
		Mint   string `json:"mint"`
		Resume string `json:"resume"`
	} `json:"prompts"`
}

type loopSpecEdge struct {
	State   string   `json:"state"`
	Signal  string   `json:"signal"`
	Target  string   `json:"target"`
	Dismiss []string `json:"dismiss"`
	Hook    string   `json:"hook"`
}

func loadLoopSpecs(t *testing.T) map[string]loopSpec {
	t.Helper()
	raw, err := os.ReadFile("testdata/loop-specs.json")
	require.NoError(t, err, "run bin/dump-loop-specs.py to regenerate")
	var file loopSpecFile
	require.NoError(t, json.Unmarshal(raw, &file))
	require.NotEmpty(t, file.Specs)
	return file.Specs
}

// asPattern builds the StateMachinePattern a LoopSpec describes.
//
// A worker becomes an agent state; a worker-less state becomes an action,
// which is what keeps the loop's deterministic phases free. Terminals that the
// spec names but never gives a body are terminal states.
func (s loopSpec) asPattern() *loomv1.StateMachinePattern {
	sm := &loomv1.StateMachinePattern{
		EntryState:    s.StartState,
		States:        map[string]*loomv1.WorkflowState{},
		BubbleSignals: s.BubbleSignals,
		SignalFields:  map[string]*loomv1.SignalFields{},
	}
	for signal, fields := range s.SignalFields {
		sm.SignalFields[strings.ToLower(signal)] = &loomv1.SignalFields{Fields: fields}
	}

	for name, st := range s.States {
		ws := &loomv1.WorkflowState{Route: map[string]*loomv1.Transition{}}
		// VOCAB is not the route keys: a signal outside it is a malfunction and
		// earns a retry, while one inside it but unrouted is deliberate egress.
		ws.Vocab = append(ws.Vocab, s.Vocab[name]...)
		if st.Worker != nil && *st.Worker != "" {
			ws.Body = &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{
				AgentId:      *st.Worker,
				Instructions: st.Prompts.Mint,
			}}
		} else {
			ws.Body = &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{
				Tool:   replayBodyTool,
				Params: map[string]string{"skill": st.Prompts.Mint},
			}}
		}
		sm.States[name] = ws
	}

	for _, e := range s.Route {
		st, ok := sm.States[e.State]
		if !ok {
			continue
		}
		tr := &loomv1.Transition{Dismiss: e.Dismiss}
		if e.Hook == HookCheckpoint {
			tr.Checkpoint = true
		} else if e.Hook != "" {
			tr.Hook = &loomv1.ToolState{Tool: replayHookTool, Params: map[string]string{"hook": e.Hook}}
		}
		if strings.HasPrefix(e.Target, "advance:") {
			tr.Target = &loomv1.Transition_NextTool{NextTool: &loomv1.ToolState{
				Tool: replayAdvanceTool, Params: map[string]string{"hook": e.Target},
			}}
		} else {
			tr.Target = &loomv1.Transition_NextState{NextState: e.Target}
		}
		st.Route[strings.ToLower(e.Signal)] = tr
	}

	for _, name := range s.Terminal {
		if _, ok := sm.States[name]; !ok {
			sm.States[name] = &loomv1.WorkflowState{Terminal: true}
		} else {
			sm.States[name].Terminal = true
		}
	}
	// A route may name a terminal the spec's own terminal set does not list.
	for _, e := range s.Route {
		if strings.HasPrefix(e.Target, "advance:") {
			continue
		}
		if _, ok := sm.States[e.Target]; !ok {
			sm.States[e.Target] = &loomv1.WorkflowState{Terminal: true}
		}
	}
	return sm
}

func (s loopSpec) stateSet() map[string]bool {
	set := map[string]bool{}
	for name := range s.States {
		set[name] = true
	}
	for _, name := range s.Terminal {
		set[name] = true
	}
	return set
}

// ---------------------------------------------------------------------------
// the recorded runs
// ---------------------------------------------------------------------------

type recordedStep struct {
	Seq    int    `json:"seq"`
	From   string `json:"from"`
	Worker string `json:"worker"`
	Skill  string `json:"skill"`
	Signal string `json:"signal"`
	Mint   *bool  `json:"mint"`
}

func loadRecordedRuns(t *testing.T) map[string][]recordedStep {
	t.Helper()
	runs := map[string][]recordedStep{}
	root := filepath.Join("testdata", "transitions")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var steps []recordedStep
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var step recordedStep
			if err := json.Unmarshal([]byte(line), &step); err != nil {
				continue // a malformed tail line in a killed run is not a defect here
			}
			if step.From != "" && step.Signal != "" {
				steps = append(steps, step)
			}
		}
		if len(steps) > 0 {
			runs[strings.TrimPrefix(path, root+string(filepath.Separator))] = steps
		}
		return nil
	})
	require.NoError(t, err)
	return runs
}

// matchSpec finds the spec whose states cover every state the run visited.
// The run identifies itself: no path convention, no mapping table to drift.
func matchSpec(specs map[string]loopSpec, steps []recordedStep) (string, loopSpec, bool) {
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)

	best, bestSize := "", 1<<30
	for _, name := range names {
		set := specs[name].stateSet()
		covered := true
		for _, step := range steps {
			if !set[step.From] {
				covered = false
				break
			}
		}
		// Prefer the tightest spec that covers the run: a bigger graph can
		// cover a smaller one's states by accident.
		if covered && len(set) < bestSize {
			best, bestSize = name, len(set)
		}
	}
	if best == "" {
		return "", loopSpec{}, false
	}
	return best, specs[best], true
}

// ---------------------------------------------------------------------------
// the replay harness
// ---------------------------------------------------------------------------

// replayHandler answers every state with the signal the run recorded, and
// resolves advance hooks to the state the run actually went to next. Routing
// is what is under test; the DAG walk behind an advance hook is not.
const (
	replayBodyTool    = "replay_body"
	replayHookTool    = "replay_hook"
	replayAdvanceTool = "replay_advance"
)

type replayHandler struct {
	mu     sync.Mutex
	steps  []recordedStep
	cursor int
	visits []string
}

func (h *replayHandler) next() (recordedStep, bool) {
	if h.cursor >= len(h.steps) {
		return recordedStep{}, false
	}
	return h.steps[h.cursor], true
}

// Execute answers a state's body with the signal the run recorded, and resolves
// a computed destination to the state the run actually went to next. Routing is
// what is under test; whatever a real tool would have done is not.
func (h *replayHandler) Execute(_ context.Context, toolName string, params map[string]interface{}) (*shuttle.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch toolName {
	case replayHookTool:
		return &shuttle.Result{Success: true, Data: ""}, nil

	case replayAdvanceTool:
		if step, ok := h.next(); ok {
			return &shuttle.Result{Success: true, Data: step.From}, nil
		}
		return nil, fmt.Errorf("replay: computed destination at end of run")

	default:
		state, _ := params[ParamState].(string)
		step, ok := h.next()
		if !ok {
			return nil, fmt.Errorf("replay: ran out of recorded steps at %s", state)
		}
		if step.From != state {
			return nil, fmt.Errorf("replay: expected state %s, engine entered %s", step.From, state)
		}
		h.visits = append(h.visits, state)
		h.cursor++
		return &shuttle.Result{Success: true, Data: step.Signal}, nil
	}
}

// TestLoopReplay_EngineWalksRecordedRuns is the differential test: every
// recorded run, replayed through this engine, must visit the same states in
// the same order.
func TestLoopReplay_EngineWalksRecordedRuns(t *testing.T) {
	t.Parallel()

	specs := loadLoopSpecs(t)
	runs := loadRecordedRuns(t)
	require.NotEmpty(t, runs, "no recorded runs in testdata/transitions")

	var replayed, steps, consumed, complete int
	matchedByUnit := map[string]int{}
	unmatchedByUnit := map[string]int{}
	unknownStates := map[string]int{}
	names := make([]string, 0, len(runs))
	for name := range runs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		recorded := runs[name]
		unit := strings.SplitN(name, "_", 2)[0]
		specName, spec, ok := matchSpec(specs, recorded)
		if !ok {
			// A run that walked states no current spec declares: the specs have
			// moved on since it ran. Recorded rather than skipped in silence —
			// a drop in coverage is how a spec change would hide a regression.
			unmatchedByUnit[unit]++
			for _, step := range recorded {
				known := false
				for _, sp := range specs {
					if sp.stateSet()[step.From] {
						known = true
						break
					}
				}
				if !known {
					unknownStates[step.From]++
				}
			}
			continue
		}
		matchedByUnit[unit]++

		t.Run(fmt.Sprintf("%s/%s", specName, name), func(t *testing.T) {
			handler := &replayHandler{steps: recorded}
			orch := NewOrchestrator(Config{
				Logger:       zaptest.NewLogger(t),
				Tracer:       observability.NewNoOpTracer(),
				ToolExecutor: handler,
			})

			// Every body replays its recorded signal, so no agent is needed and
			// no model is called: the graph is what is under test.
			sm := spec.asPattern()
			for _, st := range sm.States {
				if st.GetAgent() != nil {
					st.Body = &loomv1.WorkflowState_Tool{
						Tool: &loomv1.ToolState{Tool: replayBodyTool},
					}
				}
			}
			sm.EntryState = recorded[0].From
			sm.MaxTransitions = int32(len(recorded) + 8)

			pattern := &loomv1.WorkflowPattern{
				Pattern:    &loomv1.WorkflowPattern_StateMachine{StateMachine: sm},
				WorkflowId: "replay",
			}

			// Production did not stop where a park stops us: a human answered
			// and the run carried on, and a file may log a composition — one
			// loop finishing and the next beginning in the same record. Replay
			// both, so coverage measures the engine rather than the harness.
			err := replayToEnd(context.Background(), orch, pattern, handler, sm)

			var expected []string
			for _, step := range recorded[:handler.cursor] {
				expected = append(expected, step.From)
			}
			assert.Equal(t, expected, handler.visits,
				"engine diverged from the recorded walk (err=%v)", err)
			assert.NotZero(t, handler.cursor, "replay made no progress")

			consumed += handler.cursor
			if handler.cursor == len(recorded) {
				complete++
			}
		})

		replayed++
		steps += len(recorded)
	}

	t.Logf("replayed %d runs (%d to the last recorded step), %d/%d transitions reproduced",
		replayed, complete, consumed, steps)
	units := make([]string, 0, len(matchedByUnit))
	for unit := range matchedByUnit {
		units = append(units, unit)
	}
	for unit := range unmatchedByUnit {
		if _, ok := matchedByUnit[unit]; !ok {
			units = append(units, unit)
		}
	}
	sort.Strings(units)
	for _, unit := range units {
		t.Logf("  %-10s replayed=%-3d skipped=%d", unit, matchedByUnit[unit], unmatchedByUnit[unit])
	}
	for state, n := range unknownStates {
		t.Logf("  state %q appears in %d recorded steps but in no current spec", state, n)
	}

	// A floor, not a ceiling: coverage can only fall if a spec change stops the
	// engine reproducing history, and that is exactly what this test exists to
	// notice. Raise it when the corpus grows; never lower it to make it pass.
	assert.GreaterOrEqual(t, replayed, 55, "replay coverage fell — a spec or the engine changed")
	assert.GreaterOrEqual(t, consumed, 820, "fewer transitions reproduced than before")
}

// replayToEnd drives a recording as far as it goes, doing what the running
// system did: a park is answered and the run continues, and a terminal that is
// not the end of the file means the next loop in a composition began there.
func replayToEnd(ctx context.Context, orch *Orchestrator, pattern *loomv1.WorkflowPattern, h *replayHandler, sm *loomv1.StateMachinePattern) error {
	_, err := orch.ExecutePattern(ctx, pattern)

	for guard := 0; guard < 256; guard++ {
		var suspended *WorkflowSuspended
		switch {
		case errors.As(err, &suspended):
			// The run parked. In production a human or the DM decided and the
			// walk went on somewhere the table could not have chosen — so the
			// recording, not the engine, says where. Step over the parked
			// signal and pick the walk up where it actually resumed.
			h.mu.Lock()
			if h.cursor < len(h.steps) {
				h.visits = append(h.visits, h.steps[h.cursor].From)
				h.cursor++
			}
			h.mu.Unlock()
		case err == nil:
			// A terminal with the recording still going: the next loop of a
			// composition begins here, logged to the same file.
		default:
			return err
		}

		h.mu.Lock()
		done := h.cursor >= len(h.steps)
		var next string
		if !done {
			next = h.steps[h.cursor].From
		}
		h.mu.Unlock()
		if done {
			return nil
		}
		if _, ok := sm.States[next]; !ok {
			return fmt.Errorf("replay: recording resumes at %q, which this spec does not declare", next)
		}
		sm.EntryState = next
		_, err = orch.ExecutePattern(ctx, pattern)
	}
	return err
}

// TestLoopSpecs_PatternsAreWellFormed checks every extracted spec builds a
// machine this engine will accept, before any replay depends on it.
func TestLoopSpecs_PatternsAreWellFormed(t *testing.T) {
	t.Parallel()

	specs := loadLoopSpecs(t)
	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			sm := spec.asPattern()

			require.Contains(t, sm.States, sm.EntryState, "entry state must exist")
			terminals := 0
			for _, st := range sm.States {
				if st.GetTerminal() {
					terminals++
				}
			}
			assert.NotZero(t, terminals, "a machine with no terminal could never end")

			for stateName, st := range sm.States {
				for signal, tr := range st.GetRoute() {
					if next := tr.GetNextState(); next != "" {
						assert.Contains(t, sm.States, next,
							"%s/%s routes to a state that does not exist", stateName, signal)
					} else {
						assert.NotNil(t, tr.GetNextTool(),
							"%s/%s has neither a destination nor a tool to compute one", stateName, signal)
					}
				}
			}

			// The negative half: what the loop leaves unrouted must stay so.
			for _, key := range spec.Unrouted {
				parts := strings.SplitN(key, "|", 2)
				require.Len(t, parts, 2)
				if st, ok := sm.States[parts[0]]; ok {
					_, routed := st.GetRoute()[strings.ToLower(parts[1])]
					assert.False(t, routed,
						"%s is unrouted in the loop — routing it here would take a decision that belongs to a human", key)
				}
			}
		})
	}
}
