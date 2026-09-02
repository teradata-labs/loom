// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

// TaskBoardConfig.implicit_tasks has to reach the emitter that NewAgent wires
// for itself.
//
// The auto-wired path is the one almost every agent takes: only a host that
// passes WithImplicitTaskEmitter explicitly supplies its own emitter, and
// registry-built agents never do. That path resolved the HARDCODED default and
// ignored the config, so mode, triggers, excluded_triggers and max_per_session
// were all inert — an off switch that did not switch anything off, on a feature
// that writes durable rows by default. It also let the emitter and
// buildTaskContext (which does read the real config) resolve two different
// policies for one setting.
//
// These tests go through the real Chat path with a real task store, because
// that is the only way to prove the policy the AGENT built for itself is the one
// in force. Asserting on a hand-built emitter would test ResolveImplicitPolicy,
// which is already tested in pkg/task, and would have passed throughout the bug.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	sqlitestore "github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
)

// implicitCfgRig is an agent whose emitter is the one NewAgent auto-wired: no
// WithImplicitTaskEmitter, so the only thing that can shape the policy is the
// TaskBoardConfig handed to WithTaskBoard.
type implicitCfgRig struct {
	agent *Agent
	tasks *task.Manager
	tool  *countingTool
}

// newImplicitCfgRig scripts turns qualifying turns: each is one tool call
// followed by a text reply, which is the shape that fires TOOL_CALL exactly
// once per turn.
func newImplicitCfgRig(t *testing.T, cfg *loomv1.ImplicitTaskConfig, turns int) *implicitCfgRig {
	t.Helper()

	dir := t.TempDir()
	db := openMigratedTaskDB(t, filepath.Join(dir, "tasks.db"))
	mgr := task.NewManager(
		sqlitestore.NewTaskStore(db, observability.NewNoOpTracer()),
		nil, observability.NewNoOpTracer(), nil)

	responses := make([]mockLLMResponse, 0, turns*2)
	for i := 0; i < turns; i++ {
		responses = append(responses,
			mockLLMResponse{toolCalls: []llmtypes.ToolCall{
				{ID: "c-work", Name: "do_work", Input: map[string]interface{}{"v": "x"}},
			}},
			mockLLMResponse{content: "done"},
		)
	}

	agentCfg := DefaultConfig()
	agentCfg.PatternConfig = DefaultPatternConfig()
	agentCfg.PatternConfig.UseLLMClassifier = false

	a := NewAgent(&mockBackend{}, &mockToolCallingLLM{responses: responses},
		WithConfig(agentCfg),
		WithTaskBoard(mgr, nil, &loomv1.TaskBoardConfig{ImplicitTasks: cfg}),
	)
	tool := &countingTool{name: "do_work"}
	a.RegisterTool(tool)

	return &implicitCfgRig{agent: a, tasks: mgr, tool: tool}
}

// runTurns drives n turns on one session and fails if a turn errors.
func (r *implicitCfgRig) runTurns(t *testing.T, sessionID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := r.agent.Chat(context.Background(), sessionID, "do the work")
		require.NoError(t, err, "turn %d", i)
	}
	require.EqualValues(t, n, r.tool.runs.Load(),
		"rig sanity: every turn must actually call the tool, or nothing would qualify")
}

// sessionTasks returns the tasks recorded for a session.
//
// Keyed by BOARD, not by ListTasksOpts.SessionID: with no DefaultBoardId the
// board id IS the session id (what maybeRecordImplicitTask falls back to), and
// claimed_by_session is released when the task closes, so a session filter
// silently returns nothing for exactly the finished turns these tests read.
func (r *implicitCfgRig) sessionTasks(t *testing.T, sessionID string) []*task.Task {
	t.Helper()
	tasks, _, err := r.tasks.ListTasks(context.Background(), task.ListTasksOpts{
		BoardID: sessionID,
		Limit:   100,
	})
	require.NoError(t, err)
	return tasks
}

// TestImplicitTasks_ModeDisabledMintsNothing is the off switch. An operator who
// writes mode: DISABLED has said the runtime must not create task rows; before
// this the auto-wired emitter never saw the setting and minted anyway.
func TestImplicitTasks_ModeDisabledMintsNothing(t *testing.T) {
	r := newImplicitCfgRig(t, &loomv1.ImplicitTaskConfig{
		Mode:          loomv1.ImplicitTaskMode_IMPLICIT_TASK_MODE_DISABLED,
		MaxPerSession: 1,
	}, 2)

	r.runTurns(t, "s-disabled", 2)

	if got := r.sessionTasks(t, "s-disabled"); len(got) != 0 {
		t.Fatalf("mode=DISABLED recorded %d task(s), want 0: the auto-wired emitter must "+
			"resolve TaskBoardConfig.implicit_tasks, not the hardcoded default — an off "+
			"switch that still writes durable rows is not an off switch", len(got))
	}
}

// TestImplicitTasks_MaxPerSessionIsHonoured pins a second knob, so a fix that
// happened to special-case only the mode enum would not pass. Three qualifying
// turns under a cap of one must leave exactly one row.
func TestImplicitTasks_MaxPerSessionIsHonoured(t *testing.T) {
	r := newImplicitCfgRig(t, &loomv1.ImplicitTaskConfig{MaxPerSession: 1}, 3)

	r.runTurns(t, "s-capped", 3)

	if got := r.sessionTasks(t, "s-capped"); len(got) != 1 {
		t.Fatalf("max_per_session=1 recorded %d task(s) over 3 qualifying turns, want 1: "+
			"the cap is configured on the agent and must reach the emitter it built", len(got))
	}
}

// TestImplicitTasks_DefaultConfigUnchanged is the other half of the contract:
// reading the config must not quietly change what an agent that configures
// nothing does. Implicit recording is opt-out, so three working turns still
// record three tasks.
func TestImplicitTasks_DefaultConfigUnchanged(t *testing.T) {
	r := newImplicitCfgRig(t, nil, 3)

	r.runTurns(t, "s-default", 3)

	got := r.sessionTasks(t, "s-default")
	require.Len(t, got, 3,
		"a nil implicit_tasks config must still resolve to the opt-out default: one task per working turn")
	for _, tk := range got {
		require.Equal(t, "implicit", tk.CreatedVia)
	}
}

// TestImplicitTasks_YAMLRoundTrip closes the other half of Finding 2: the proto
// message was unreachable from an agent's YAML file, so the documented
// configuration could not actually be written down anywhere.
func TestImplicitTasks_YAMLRoundTrip(t *testing.T) {
	cfg := parseTaskBoardConfig(&TaskBoardConfigYAML{
		ImplicitTasks: &ImplicitTasksConfigYAML{
			Mode:             "disabled",
			Triggers:         []string{"tool_call", "IMPLICIT_TASK_TRIGGER_HUMAN_REQUEST"},
			ExcludedTriggers: []string{"human_request", "not_a_trigger"},
			MaxPerSession:    7,
			AgentVisible:     true,
		},
	})
	require.NotNil(t, cfg.ImplicitTasks)
	require.Equal(t, loomv1.ImplicitTaskMode_IMPLICIT_TASK_MODE_DISABLED, cfg.ImplicitTasks.Mode)
	require.Equal(t, []loomv1.ImplicitTaskTrigger{
		loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL,
		loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_HUMAN_REQUEST,
	}, cfg.ImplicitTasks.Triggers, "bare and fully-qualified enum names both parse")
	require.Equal(t, []loomv1.ImplicitTaskTrigger{
		loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_HUMAN_REQUEST,
	}, cfg.ImplicitTasks.ExcludedTriggers, "an unrecognised name is dropped, not mapped to UNSPECIFIED")
	require.EqualValues(t, 7, cfg.ImplicitTasks.MaxPerSession)
	require.True(t, cfg.ImplicitTasks.AgentVisible)

	// The resolver must agree with what was written, all the way through.
	policy := task.ResolveImplicitPolicy(cfg.ImplicitTasks)
	require.False(t, policy.Enabled)
	require.Equal(t, 7, policy.MaxPerSession)
	require.True(t, policy.AgentVisible)

	// An absent block stays absent: nil, not a zero-valued message.
	require.Nil(t, parseTaskBoardConfig(&TaskBoardConfigYAML{}).ImplicitTasks)
}
