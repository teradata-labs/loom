// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestration

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
)

// keepsCheckpoints is a host that keeps every checkpoint an edge writes.
type keepsCheckpoints struct {
	mu    sync.Mutex
	saved []*loomv1.WorkflowCheckpoint
}

func (k *keepsCheckpoints) ChildStarted(context.Context, string, string, *loomv1.WorkflowPattern) {}
func (k *keepsCheckpoints) Transitioned(context.Context, string, string, string, string, map[string]string, float64) {
}
func (k *keepsCheckpoints) ChildEnded(context.Context, string, string, string, string, map[string]string) {
}
func (k *keepsCheckpoints) Checkpointed(_ context.Context, ckpt *loomv1.WorkflowCheckpoint) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.saved = append(k.saved, ckpt)
}

// each answers a tool with the signal named for it.
type each map[string]string

func (e each) Execute(_ context.Context, name string, _ map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: `{"signal": "` + e[name] + `"}`}, nil
}

// An edge marked checkpoint hands its host the checkpoint it wrote, before
// the move, and a run resumed from it walks on from where that edge led.
func TestAnEdgeCheckpointReachesTheHostAndResumes(t *testing.T) {
	t.Parallel()
	host := &keepsCheckpoints{}
	tools := each{"plan": "planned", "build": "built"}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(),
		ToolExecutor: tools, RunObserver: host})
	p := wrap(&loomv1.StateMachinePattern{
		EntryState: "PLAN",
		States: map[string]*loomv1.WorkflowState{
			"PLAN": {Body: &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "plan"}},
				Route: map[string]*loomv1.Transition{"planned": {Target: &loomv1.Transition_NextState{NextState: "BUILD"}, Checkpoint: true}}},
			"BUILD": {Body: &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "build"}},
				Route: map[string]*loomv1.Transition{"built": to("DONE")}},
			"DONE": {Terminal: true},
		},
	})
	p.WorkflowId = "edge-ckpt"

	result, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, "DONE", result.Metadata["terminal_state"])

	require.Len(t, host.saved, 1, "the edge's checkpoint did not reach the host")
	ckpt := host.saved[0]
	assert.Equal(t, "edge-ckpt", ckpt.GetWorkflowId())
	assert.Equal(t, []string{"BUILD"}, ckpt.GetStatePath(), "the checkpoint records the state the edge enters")

	// A run that died after the edge is taken up from it: BUILD runs, PLAN
	// does not run again.
	again := each{"plan": "planned", "build": "built"}
	ran := &countingTools{inner: again}
	orch2 := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), ToolExecutor: ran})
	resumed, err := orch2.ResumeWorkflow(context.Background(), p, ckpt,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	assert.Equal(t, "DONE", resumed.Metadata["terminal_state"])
	assert.Equal(t, []string{"build"}, ran.names(), "the resumed run re-ran the state it had finished")
}

// countingTools records which tools ran, in order.
type countingTools struct {
	mu    sync.Mutex
	inner ToolExecutor
	ran   []string
}

func (c *countingTools) Execute(ctx context.Context, name string, p map[string]interface{}) (*shuttle.Result, error) {
	c.mu.Lock()
	c.ran = append(c.ran, name)
	c.mu.Unlock()
	return c.inner.Execute(ctx, name, p)
}

func (c *countingTools) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ran...)
}
