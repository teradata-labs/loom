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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
)

// pingPong answers every tool with "next", so A and B hand to each other for
// ever unless something bounds the walk.
type pingPong struct {
	calls  atomic.Int64
	cancel func()
	after  int64
}

func (p *pingPong) Execute(context.Context, string, map[string]interface{}) (*shuttle.Result, error) {
	if n := p.calls.Add(1); p.cancel != nil && n == p.after {
		p.cancel()
	}
	return &shuttle.Result{Success: true, Data: `{"signal": "next"}`}, nil
}

func cycle(loopback []string) *loomv1.WorkflowPattern {
	p := wrap(&loomv1.StateMachinePattern{
		EntryState:     "A",
		MaxTransitions: 5,
		LoopbackStates: loopback,
		States: map[string]*loomv1.WorkflowState{
			"A": {Body: &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "a"}}, Route: map[string]*loomv1.Transition{"next": to("B")}},
			"B": {Body: &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "b"}}, Route: map[string]*loomv1.Transition{"next": to("A")}},
			"Z": {Terminal: true},
		},
	})
	p.WorkflowId = "cycle"
	return p
}

// A walk that cycles through states its author did not list as loopback is
// not counted as stalling, by design — but it is still bounded: it stops at a
// ceiling far above the loopback bound rather than running for ever.
func TestWalk_ACycleOutsideTheLoopbackStatesStillStops(t *testing.T) {
	t.Parallel()
	tools := &pingPong{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), ToolExecutor: tools})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = orch.ExecutePattern(context.Background(), cycle([]string{"Z"}))
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("the walk did not stop; %d tool calls so far", tools.calls.Load())
	}
	assert.LessOrEqual(t, tools.calls.Load(), int64(totalTransitionFactor*5+1),
		"the walk ran past its ceiling of %d transitions", totalTransitionFactor*5)
}

// The loopback bound still applies as it always has: a cycle through listed
// states parks after max_transitions re-entries.
func TestWalk_ACycleThroughLoopbackStatesParksAtItsBound(t *testing.T) {
	t.Parallel()
	tools := &pingPong{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), ToolExecutor: tools})
	_, _ = orch.ExecutePattern(context.Background(), cycle([]string{"A", "B"}))
	assert.LessOrEqual(t, tools.calls.Load(), int64(2*5+2))
}

// A walk whose context is cancelled stops at the next state, even when the
// states it runs ignore the context.
func TestWalk_ACancelledWalkStops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tools := &pingPong{cancel: cancel, after: 3}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), ToolExecutor: tools})

	_, err := orch.ExecutePattern(ctx, cycle(nil))
	require.ErrorIs(t, err, context.Canceled)
	assert.LessOrEqual(t, tools.calls.Load(), int64(4), "the walk ran on after it was cancelled")
}
