// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// looksAround is a seat's model: it reads a file by a relative path — once
// with a command that names no directory, once with a file read — and ends its
// step. What it was told and what its tools returned are kept.
type looksAround struct {
	mu   sync.Mutex
	told string
	saw  string
}

func (m *looksAround) Name() string  { return "looks" }
func (m *looksAround) Model() string { return "looks" }
func (m *looksAround) Chat(_ context.Context, msgs []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var results []string
	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			m.told = msg.Content
		case "tool":
			results = append(results, msg.Content)
		}
	}
	if len(results) == 0 {
		return &llmtypes.LLMResponse{StopReason: "tool_use", ToolCalls: []llmtypes.ToolCall{
			{ID: "c1", Name: "shell_execute", Input: map[string]interface{}{"command": "cat WHICH"}},
			{ID: "c2", Name: "file_read", Input: map[string]interface{}{"path": "WHICH"}},
		}}, nil
	}
	m.saw = strings.Join(results, "\n")
	return &llmtypes.LLMResponse{Content: "looked\ndone", StopReason: "end_turn"}, nil
}

// placed is a root with three directories, each holding a file that says which
// it is: where the seat's tools were built, the run's own directory, and a
// third the run knows by a variable.
func placed(t *testing.T) (built, run, other string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("LOOM_SANDBOX_DIR", root)
	dirs := map[string]string{"built": "", "run": "", "other": "", filepath.Join("run", "sub"): ""}
	for name := range dirs {
		dir := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "WHICH"), []byte("in-"+filepath.Base(name)+"-dir"), 0o644))
	}
	return filepath.Join(root, "built"), filepath.Join(root, "run"), filepath.Join(root, "other")
}

// lookOnce walks a one-step machine whose seat looks around, with the machine
// declaring agentDir, and returns what the seat was told and saw.
func lookOnce(t *testing.T, built, run, agentDir string, vars map[string]string) (*looksAround, error) {
	t.Helper()
	store := machineStore(t)
	model := &looksAround{}
	seat := machineAgent(t, "looker", store, model)
	seat.RegisterTool(builtin.NewShellExecuteTool(built))
	seat.RegisterTool(builtin.NewFileReadTool(built))
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("looker", seat)
	p := wrap(&loomv1.StateMachinePattern{EntryState: "LOOK", Dir: run, AgentDir: agentDir,
		States: map[string]*loomv1.WorkflowState{
			"LOOK": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "looker", Instructions: "look around"}},
				Vocab: []string{"done"}, Route: map[string]*loomv1.Transition{"done": to("END")}},
			"END": {Terminal: true},
		}})
	p.WorkflowId = "placed-" + strings.NewReplacer("{", "", "}", "", "/", "-").Replace(agentDir)
	_, err := orch.ExecutePattern(WithRunVars(context.Background(), vars), p)
	return model, err
}

// A machine's agents work where its spec says. The directory is written with
// the run's variables; the seat's command and its read land there, whatever
// directory its tools were built with, and the step is told the same
// directory. A machine that declares none leaves its agents where they were
// built, and tells them nothing.
func TestAMachinesAgentsWorkWhereItsSpecSays(t *testing.T) {
	built, run, other := placed(t)
	for name, tc := range map[string]struct {
		agentDir string
		in       string // the directory the seat must work in
		told     bool
	}{
		"a variable of the run":       {"{place}", other, true},
		"the run's own directory":     {"{loom_dir}", run, true},
		"beneath the run's directory": {"sub", filepath.Join(run, "sub"), true},
		"none declared":               {"", built, false},
	} {
		model, err := lookOnce(t, built, run, tc.agentDir, map[string]string{"place": other})
		require.NoError(t, err, name)
		want := "in-" + filepath.Base(tc.in) + "-dir"
		require.Equal(t, 2, strings.Count(model.saw, want),
			"%s: the seat's command and its read did not both land in %s:\n%s", name, tc.in, model.saw)
		require.Equal(t, 2, strings.Count(model.saw, "in-"), "%s: the seat read another directory:\n%s", name, model.saw)
		if tc.told {
			require.Contains(t, model.told, "Working folder for this step: "+tc.in, "%s: the step was not told its directory", name)
		} else {
			require.NotContains(t, model.told, "Working folder for this step", "%s: the step was told a directory nobody declared", name)
		}
	}
}

// A directory that names a variable the run does not have is no directory:
// the step does not run, and the stop says which declaration it was.
func TestAnAgentDirNamingAnUnknownVariableStopsTheStep(t *testing.T) {
	built, run, _ := placed(t)
	model, err := lookOnce(t, built, run, "{nowhere}/repos", nil)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped)
	require.Contains(t, stopped.Checkpoint.GetError(), `agent_dir "{nowhere}/repos"`)
	require.Empty(t, model.told, "the seat took its turn with no directory to work in")
}

// The spec's agent_dir is read from YAML, on a machine and on a machine it
// delegates to: each loop declares its own.
func TestAgentDirIsReadFromTheSpec(t *testing.T) {
	p, err := LoadWorkflowFromYAMLBytes([]byte(`apiVersion: loom/v1
kind: Workflow
metadata:
  name: outer
spec:
  type: state-machine
  entry_state: A
  agent_dir: "{ws}"
  states:
    A:
      delegate:
        workflow:
          type: state-machine
          entry_state: B
          agent_dir: "{unit_dir}"
          states:
            B:
              terminal: true
      route:
        B: Z
    Z:
      terminal: true
`))
	require.NoError(t, err)
	sm := p.GetStateMachine()
	require.Equal(t, "{ws}", sm.GetAgentDir())
	require.Equal(t, "{unit_dir}", sm.GetStates()["A"].GetDelegate().GetWorkflow().GetStateMachine().GetAgentDir())
}
