// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// varsWitness keeps the vars every machine reported on each transition.
type varsWitness struct {
	mu   sync.Mutex
	seen map[string]map[string]string
}

func (w *varsWitness) ChildStarted(context.Context, string, string, *loomv1.WorkflowPattern) {}
func (w *varsWitness) ChildEnded(context.Context, string, string, string, string, map[string]string) {
}
func (w *varsWitness) Transitioned(_ context.Context, workflowID, _, _, _ string, vars map[string]string, _ float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil {
		w.seen = map[string]map[string]string{}
	}
	copied := map[string]string{}
	for k, v := range vars {
		copied[k] = v
	}
	w.seen[workflowID] = copied
}

// What a child is given — the run's vars, its item, the delegation's constants —
// is part of the child's own vars from its first state, and so is in every
// record the child reports. A position rebuilt from such a record feeds the
// child exactly what it had; one rebuilt from a record without them starves it
// of the values its prompts are written against.
func TestDelegate_AChildReportsWhatItWasGivenAmongItsVars(t *testing.T) {
	t.Parallel()

	tools := newFanTools("alpha")
	witness := &varsWitness{}
	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: tools,
		RunObserver:  witness,
	})
	pattern := fanPattern("alpha")
	fan := pattern.GetStateMachine().States["FAN"].GetDelegate()
	fan.Vars = map[string]string{"scope": "dit"}
	fan.Workflow.GetStateMachine().States["REVIEW"].GetTool().Params = map[string]string{"told": "{item} on {repo} proves {scope}"}

	ctx := WithRunVars(context.Background(), map[string]string{"repo": "loom"})
	_, err := orch.ExecutePattern(ctx, pattern)
	require.NoError(t, err)
	assert.Equal(t, "alpha on loom proves dit", tools.told["alpha"], "the child renders what it was given")

	child := witness.seen[childID("fan", "FAN", "alpha")]
	require.NotNil(t, child, "the child reported a transition")
	assert.Equal(t, "loom", child["repo"], "the run var the child inherited is among the vars it reports")
	assert.Equal(t, "alpha", child["item"], "the child's item is among the vars it reports")
	assert.Equal(t, "dit", child["scope"], "the constant the child was handed is among the vars it reports")
}
