// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap/zaptest"
)

// call is one model call a script answers: words, a tool to call, what it
// cost — or a failure.
type call struct {
	say  string
	tool string
	cost float64
	fail bool
}

// costedLLM answers each call from its script, in order, reporting what each
// cost; the last answer repeats.
type costedLLM struct {
	mu     sync.Mutex
	script []call
	n      int
}

// outage is the model unreachable for longer than the agent retries a call.
func outage() []call {
	down := make([]call, 6)
	for i := range down {
		down[i] = call{fail: true}
	}
	return down
}

// script is calls in order, outages spread in.
func script(parts ...any) []call {
	var out []call
	for _, p := range parts {
		switch x := p.(type) {
		case call:
			out = append(out, x)
		case []call:
			out = append(out, x...)
		}
	}
	return out
}

func (m *costedLLM) Name() string  { return "costed" }
func (m *costedLLM) Model() string { return "costed" }
func (m *costedLLM) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.script[min(m.n, len(m.script)-1)]
	m.n++
	if c.fail {
		return nil, errors.New("the model could not be reached")
	}
	resp := &llmtypes.LLMResponse{Content: c.say, StopReason: "end_turn",
		Usage: llmtypes.Usage{InputTokens: 10, OutputTokens: 10, TotalTokens: 20, CostUSD: c.cost}}
	if c.tool != "" {
		resp.StopReason = "tool_use"
		resp.ToolCalls = []llmtypes.ToolCall{{ID: fmt.Sprintf("c%d", m.n), Name: c.tool, Input: map[string]interface{}{}}}
	}
	return resp, nil
}

// lookTool is a tool a seat calls while it works.
type lookTool struct{}

func (lookTool) Name() string                     { return "look" }
func (lookTool) Description() string              { return "look" }
func (lookTool) Backend() string                  { return "" }
func (lookTool) InputSchema() *shuttle.JSONSchema { return &shuttle.JSONSchema{Type: "object"} }
func (lookTool) Execute(context.Context, map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: "seen"}, nil
}

// stepRecords keeps every record a run's observer is given: a step left by a
// transition, and a step the walk stopped in.
type stepRecords struct {
	mu    sync.Mutex
	steps []string
	cost  map[string]float64
}

func (r *stepRecords) add(workflowID, kind, state string, cost float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cost == nil {
		r.cost = map[string]float64{}
	}
	r.steps = append(r.steps, fmt.Sprintf("%s %s %s $%.2f", workflowID, kind, state, cost))
	r.cost[workflowID] += cost
}

func (r *stepRecords) ChildStarted(context.Context, string, string, *loomv1.WorkflowPattern) {}
func (r *stepRecords) ChildEnded(context.Context, string, string, string, string, map[string]string) {
}
func (r *stepRecords) Transitioned(_ context.Context, workflowID, from, _, _ string, _ map[string]string, cost float64) {
	r.add(workflowID, "left", from, cost)
}
func (r *stepRecords) Stopped(_ context.Context, workflowID, state, _ string, cost float64) {
	r.add(workflowID, "stopped in", state, cost)
}

func (r *stepRecords) of(workflowID string) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cost[workflowID]
}

func costedAgent(t *testing.T, name string, store *agent.SessionStore, script ...call) *agent.Agent {
	t.Helper()
	ag := machineAgent(t, name, store, &costedLLM{script: script})
	ag.RegisterTool(lookTool{})
	return ag
}

func approve() *loomv1.GateDecision {
	return &loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// A state whose seat failed part-way, and was taken up again, is one visit:
// it is recorded once, when it moves on, with what the failed turn spent
// before it failed as well as what the next attempt spent.
func TestAStateTakenUpAgainIsRecordedWithBothAttempts(t *testing.T) {
	store := machineStore(t)
	records := &stepRecords{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), RunObserver: records})
	orch.RegisterAgent("worker", costedAgent(t, "worker", store, script(
		call{tool: "look", cost: 0.30}, // works a while…
		outage(),                       // …and the model goes away
		call{say: "done\nnext", cost: 0.20},
	)...))
	p := wrap(&loomv1.StateMachinePattern{EntryState: "WORK", States: map[string]*loomv1.WorkflowState{
		"WORK": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker"}},
			Vocab: []string{"next"}, Route: map[string]*loomv1.Transition{"next": to("END")}},
		"END": {Terminal: true},
	}})
	p.WorkflowId = "taken-up"

	_, err := orch.ExecutePattern(context.Background(), p)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_ERROR, stopped.Checkpoint.SuspendReason)

	result, err := orch.ResumeWorkflow(context.Background(), p, stopped.Checkpoint, approve())
	require.NoError(t, err)
	require.Equal(t, []string{"taken-up left WORK $0.50"}, records.steps)
	require.True(t, near(records.of("taken-up"), 0.50), "recorded %.2f, spent 0.50", records.of("taken-up"))
	require.True(t, near(result.Cost.TotalCostUsd, 0.50), "the run's cost is %.2f, spent 0.50", result.Cost.TotalCostUsd)
}

// A state that could not run at all leaves no record; the state before it,
// recorded when the walk left it, is not recorded again.
func TestAStateThatCouldNotRunRecordsNothingTwice(t *testing.T) {
	store := machineStore(t)
	records := &stepRecords{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), RunObserver: records})
	orch.RegisterAgent("reviewer", costedAgent(t, "reviewer", store, call{say: "reviewed\nescalate", cost: 0.51}))
	orch.RegisterAgent("manager", costedAgent(t, "manager", store, script(
		outage(), // its first call finds the credentials expired
		call{say: "settled\nclose", cost: 0.10},
	)...))
	p := wrap(&loomv1.StateMachinePattern{EntryState: "REVIEW", States: map[string]*loomv1.WorkflowState{
		"REVIEW": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "reviewer"}},
			Vocab: []string{"escalate"}, Route: map[string]*loomv1.Transition{"escalate": to("MANAGE")}},
		"MANAGE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "manager"}},
			Vocab: []string{"close"}, Route: map[string]*loomv1.Transition{"close": to("END")}},
		"END": {Terminal: true},
	}})
	p.WorkflowId = "could-not-run"

	_, err := orch.ExecutePattern(context.Background(), p)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped)
	require.Equal(t, []string{"could-not-run left REVIEW $0.51"}, records.steps,
		"the review was recorded again when the manager could not run")

	_, err = orch.ResumeWorkflow(context.Background(), p, stopped.Checkpoint, approve())
	require.NoError(t, err)
	require.Equal(t, []string{"could-not-run left REVIEW $0.51", "could-not-run left MANAGE $0.10"}, records.steps)
}

// twiceParked answers a fan's tools: the repos to fan out over, and a review
// that stops the second one for a person every other time it looks.
type twiceParked struct {
	mu     sync.Mutex
	looked map[string]int
}

func (f *twiceParked) Execute(_ context.Context, name string, params map[string]interface{}) (*shuttle.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "list_repos" {
		return &shuttle.Result{Success: true, Data: `{"signal": "listed", "repos": "alpha\nbeta"}`}, nil
	}
	dir, _ := params[ParamDir].(string)
	item := filepath.Base(dir)
	f.looked[item]++
	if item == "beta" && f.looked[item]%2 == 1 {
		return &shuttle.Result{Success: true, Data: "needs a look\nhitl"}, nil
	}
	return &shuttle.Result{Success: true, Data: "reviewed\nclear"}, nil
}

// A child that stopped and was resumed is counted once in its parent. Each
// time its stop stops the parent's state, that visit is recorded with what it
// spent; the visit taken up again records only what it spent itself — never a
// child's whole spend again on top.
func TestAChildTakenUpAgainIsCountedOnceInItsParent(t *testing.T) {
	store := machineStore(t)
	records := &stepRecords{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(),
		ToolExecutor: &twiceParked{looked: map[string]int{}}, RunObserver: records})
	orch.RegisterAgent("doer", costedAgent(t, "doer", store, call{say: "did it\ndone", cost: 0.50}))
	orch.RegisterAgent("finisher", costedAgent(t, "finisher", store, call{say: "finished\ndone", cost: 0.25}))
	orch.RegisterAgent("wrapper", costedAgent(t, "wrapper", store, call{say: "wrapped\ndone", cost: 0.10}))
	review := func(next string) *loomv1.WorkflowState {
		return &loomv1.WorkflowState{Body: &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "review"}},
			Vocab: []string{"clear", "hitl"}, Route: map[string]*loomv1.Transition{"clear": to(next)}}
	}
	child := wrap(&loomv1.StateMachinePattern{EntryState: "DO", States: map[string]*loomv1.WorkflowState{
		"DO": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "doer"}},
			Vocab: []string{"done"}, Route: map[string]*loomv1.Transition{"done": to("REVIEW")}},
		"REVIEW": review("FINISH"),
		"FINISH": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "finisher"}},
			Vocab: []string{"done"}, Route: map[string]*loomv1.Transition{"done": to("RECHECK")}},
		"RECHECK": review("WRAP"),
		"WRAP": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "wrapper"}},
			Vocab: []string{"done"}, Route: map[string]*loomv1.Transition{"done": to("CLEAR")}},
		"CLEAR": {Terminal: true},
	}})
	parent := fanPattern("alpha\nbeta")
	parent.GetStateMachine().GetStates()["FAN"].GetDelegate().Workflow = child

	// alpha runs through (0.85); beta does its work (0.50) and stops at review.
	_, err := orch.ExecutePattern(context.Background(), parent)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped)
	// Resumed, beta finishes (0.25) and stops again at the recheck.
	_, err = orch.ResumeWorkflow(context.Background(), parent, stopped.Checkpoint, approve())
	require.ErrorAs(t, err, &stopped)
	// Resumed again, beta clears and wraps up (0.10).
	result, err := orch.ResumeWorkflow(context.Background(), parent, stopped.Checkpoint, approve())
	require.NoError(t, err)

	var fan []string
	for _, s := range records.steps {
		if strings.HasPrefix(s, "fan ") {
			fan = append(fan, s)
		}
	}
	require.Equal(t, []string{"fan left LIST $0.00", "fan stopped in FAN $1.35", "fan stopped in FAN $0.25", "fan left FAN $0.10"}, fan)
	require.True(t, near(result.Cost.TotalCostUsd, 1.70), "the parent's cost is %.2f; its children spent 1.70", result.Cost.TotalCostUsd)
}

// A gate is recorded when the walk stops at it, and not again when it is
// approved and the walk moves on.
func TestAGateIsRecordedOnce(t *testing.T) {
	store := machineStore(t)
	records := &stepRecords{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), RunObserver: records})
	orch.RegisterAgent("writer", costedAgent(t, "writer", store, call{say: "drafted\nready", cost: 0.40}))
	p := wrap(&loomv1.StateMachinePattern{EntryState: "DRAFT", States: map[string]*loomv1.WorkflowState{
		"DRAFT": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "writer"}},
			Vocab: []string{"ready"}, Route: map[string]*loomv1.Transition{"ready": to("END")},
			HitlGate: &loomv1.HITLGate{RequestType: "design_approval", PromptTemplate: "approve?"}},
		"END": {Terminal: true},
	}})
	p.WorkflowId = "gated"

	_, err := orch.ExecutePattern(context.Background(), p)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped)
	_, err = orch.ResumeWorkflow(context.Background(), p, stopped.Checkpoint, approve())
	require.NoError(t, err)
	require.Equal(t, []string{"gated stopped in DRAFT $0.40"}, records.steps)
}

// A seat asked to restate a signal it did not give is still in the same visit:
// the visit is recorded once, with both turns.
func TestARestatedSignalIsTheSameVisit(t *testing.T) {
	store := machineStore(t)
	records := &stepRecords{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), RunObserver: records})
	orch.RegisterAgent("worker", costedAgent(t, "worker", store,
		call{say: "all of it is done", cost: 0.30},
		call{say: "done\nnext", cost: 0.20},
	))
	p := wrap(&loomv1.StateMachinePattern{EntryState: "WORK", States: map[string]*loomv1.WorkflowState{
		"WORK": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker"}},
			Vocab: []string{"next"}, Route: map[string]*loomv1.Transition{"next": to("END")}},
		"END": {Terminal: true},
	}})
	p.WorkflowId = "restated"

	result, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err)
	require.Equal(t, []string{"restated left WORK $0.50"}, records.steps)
	require.True(t, near(result.Cost.TotalCostUsd, 0.50), "the run's cost is %.2f, spent 0.50", result.Cost.TotalCostUsd)
}

// A state that finished and escalated has ended its visit; run again when the
// walk is resumed, it is a second visit. Each is recorded with what it spent.
func TestAnEscalatedStateRunAgainIsASecondVisit(t *testing.T) {
	store := machineStore(t)
	records := &stepRecords{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), RunObserver: records})
	orch.RegisterAgent("worker", costedAgent(t, "worker", store,
		call{say: "stuck\nescalate", cost: 0.30},
		call{say: "done\nnext", cost: 0.20},
	))
	p := wrap(&loomv1.StateMachinePattern{EntryState: "WORK", States: map[string]*loomv1.WorkflowState{
		"WORK": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker"}},
			Vocab: []string{"next", "escalate"}, Route: map[string]*loomv1.Transition{"next": to("END")}},
		"END": {Terminal: true},
	}})
	p.WorkflowId = "escalated"

	_, err := orch.ExecutePattern(context.Background(), p)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, stopped.Checkpoint.SuspendReason)
	_, err = orch.ResumeWorkflow(context.Background(), p, stopped.Checkpoint, approve())
	require.NoError(t, err)
	require.Equal(t, []string{"escalated stopped in WORK $0.30", "escalated left WORK $0.20"}, records.steps)
}

// A seat asked to restate the fields its signal owes is still in the same
// visit: the visit is recorded once, with both turns.
func TestARestatedPayloadIsTheSameVisit(t *testing.T) {
	store := machineStore(t)
	records := &stepRecords{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), RunObserver: records})
	orch.RegisterAgent("worker", costedAgent(t, "worker", store,
		call{say: "found it\n{\"signal\":\"next\"}", cost: 0.30},
		call{say: "{\"signal\":\"next\",\"problem\":\"the port is taken\"}", cost: 0.20},
	))
	sm := &loomv1.StateMachinePattern{EntryState: "WORK", States: map[string]*loomv1.WorkflowState{
		"WORK": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker"}},
			Vocab: []string{"next"}, Route: map[string]*loomv1.Transition{"next": to("END")}},
		"END": {Terminal: true},
	}, SignalFields: map[string]*loomv1.SignalFields{"next": {Fields: []string{"problem"}}}}
	p := wrap(sm)
	p.WorkflowId = "payload"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err)
	require.Equal(t, []string{"payload left WORK $0.50"}, records.steps)
}

// A person who sends a gated state back, there and then, has ended its visit;
// the state run again is a second visit. Each is recorded with what it spent.
func TestAStateSentBackAtItsGateIsASecondVisit(t *testing.T) {
	store := machineStore(t)
	records := &stepRecords{}
	handler := &scriptedHITLHandler{decisions: []*loomv1.GateDecision{
		{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "shorter"},
		{Action: loomv1.GateAction_GATE_ACTION_APPROVE},
	}}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(),
		RunObserver: records, HITLHandler: handler})
	orch.RegisterAgent("writer", costedAgent(t, "writer", store,
		call{say: "drafted\nready", cost: 0.40},
		call{say: "shorter\nready", cost: 0.25},
	))
	p := wrap(&loomv1.StateMachinePattern{EntryState: "DRAFT", States: map[string]*loomv1.WorkflowState{
		"DRAFT": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "writer"}},
			Vocab: []string{"ready"}, Route: map[string]*loomv1.Transition{"ready": to("END")},
			HitlGate: &loomv1.HITLGate{RequestType: "design_approval", PromptTemplate: "approve?"}},
		"END": {Terminal: true},
	}})
	p.WorkflowId = "sent-back"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err)
	require.Equal(t, []string{"sent-back stopped in DRAFT $0.40", "sent-back left DRAFT $0.25"}, records.steps)
}
