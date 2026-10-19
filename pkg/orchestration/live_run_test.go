// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"go.uber.org/zap/zaptest"
)

// A process, run for real: the real tool registry, the real executor, real
// shell commands, a real directory.
//
// Every other test here stubs the tool door, and four defects lived happily
// behind that stub — a tool's output arriving wrapped in its own envelope,
// arguments never interpolated so a command could not name the directory it
// was meant to act on, a child handed a directory nobody had made, and a list
// of items that could not be written on the one line a signal rides. None of
// them are visible until something actually runs.
func TestLiveRun_PublishDeliverValidate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "draft"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "draft", "dag.yaml"),
		[]byte("tasks: [D-1, D-2]\n"), 0o644))

	registry := shuttle.NewRegistry()
	registry.Register(builtin.NewShellExecuteTool(dir))

	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: shuttle.NewExecutor(registry),
	})

	sh := func(cmd string) *loomv1.WorkflowState_Tool {
		return &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{
			Tool:   "shell_execute",
			Params: map[string]string{"command": cmd, "working_dir": "{" + ParamDir + "}"},
		}}
	}

	task := &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_StateMachine{
		StateMachine: &loomv1.StateMachinePattern{
			EntryState: "BUILD",
			States: map[string]*loomv1.WorkflowState{
				"BUILD": {
					Body:  sh(`echo built > out.txt && echo built`),
					Vocab: []string{"built"},
					Route: map[string]*loomv1.Transition{"built": to("DELIVERED")},
				},
				"DELIVERED": {Terminal: true},
			},
		},
	}}

	pattern := &loomv1.WorkflowPattern{
		WorkflowId: "TER-LIVE",
		Pattern: &loomv1.WorkflowPattern_StateMachine{StateMachine: &loomv1.StateMachinePattern{
			EntryState: "PUBLISH",
			Dir:        dir,
			MaxCostUsd: 400,
			States: map[string]*loomv1.WorkflowState{
				"PUBLISH": {
					Body: sh(`mv draft/dag.yaml . && rmdir draft && ` +
						`printf '{"signal": "published", "tasks": "D-1,D-2"}\n'`),
					Vocab: []string{"published"},
					Route: map[string]*loomv1.Transition{"published": to("DELIVER")},
				},
				"DELIVER": {
					Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{
						Workflow:    task,
						ItemsKey:    "tasks",
						DirTemplate: "tasks/{item}",
					}},
					Vocab: []string{FannedSignal},
					Route: map[string]*loomv1.Transition{FannedSignal: {
						Target:     &loomv1.Transition_NextState{NextState: "VALIDATE"},
						Checkpoint: true,
					}},
				},
				"VALIDATE": {
					Body: sh(`test -f dag.yaml && test -f tasks/D-1/out.txt && ` +
						`test -f tasks/D-2/out.txt && echo validated || echo env`),
					Vocab: []string{"validated", "env"},
					Route: map[string]*loomv1.Transition{"validated": to("DONE")},
				},
				"DONE": {Terminal: true},
			},
		}},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	assert.Equal(t, "DONE", result.Metadata["terminal_state"])
	assert.Zero(t, result.Cost.LlmCalls, "a process of deterministic work consults no model")
	assert.Zero(t, result.Cost.TotalCostUsd)

	// The world actually moved: the draft was published and both children did
	// their work in their own places.
	assert.FileExists(t, filepath.Join(dir, "dag.yaml"))
	assert.NoDirExists(t, filepath.Join(dir, "draft"))
	assert.FileExists(t, filepath.Join(dir, "tasks", "D-1", "out.txt"))
	assert.FileExists(t, filepath.Join(dir, "tasks", "D-2", "out.txt"))
}
