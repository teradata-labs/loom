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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
)

// A host learns from the response that the context was compacted during the
// turn: Response.Metadata["auto_compaction"] has the keys the end-of-turn
// auto-compaction set. With several folds in one turn, tokens_before is the
// estimate when the first fold armed and tokens_after the estimate after the
// last fold. A turn that does not fold has no such entry.
func TestInloopFold_TheResponseSaysTheTurnFolded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		steps     int
		wantFolds bool
	}{
		{name: "a turn that folds several times", steps: 60, wantFolds: true},
		{name: "a short turn that does not fold", steps: 1, wantFolds: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tracer := observability.NewMockTracer()
			model := &foldingStream{steps: tt.steps}
			ag, _ := foldingAgentOn(t, model, nil, nil, WithTracer(tracer))

			resp, err := ag.Chat(context.Background(), "s-fold-notice", "do the long job")
			require.NoError(t, err, "the turn must end with a response")
			require.Equal(t, "final answer", resp.Content, "the turn must reach its answer")

			if !tt.wantFolds {
				require.Zero(t, model.folds, "the fixture must not fold")
				assert.NotContains(t, resp.Metadata, "auto_compaction", "a turn that did not fold reports a compaction")
				return
			}
			require.GreaterOrEqual(t, model.folds, 2, "the fixture must fold more than once in the turn")

			loops := tracer.GetSpansByName("agent.conversation_loop")
			require.Len(t, loops, 1, "the turn did not record its loop span")
			firstBefore, lastAfter, applied, lastArmed := -1, -1, 0, -1
			for _, e := range loops[0].Events {
				switch e.Name {
				case "fold.armed":
					lastArmed = e.Attributes["estimate_tokens"].(int)
				case "fold.applied":
					if applied == 0 {
						firstBefore = lastArmed
					}
					lastAfter = e.Attributes["estimate_tokens"].(int)
					applied++
				}
			}
			require.Equal(t, model.folds, applied, "every fold the model answered must commit")

			meta, ok := resp.Metadata["auto_compaction"].(map[string]interface{})
			require.True(t, ok, "the response does not say the turn folded")
			assert.Equal(t, firstBefore, meta["tokens_before"], "tokens_before is not the estimate when the first fold armed")
			assert.Equal(t, lastAfter, meta["tokens_after"], "tokens_after is not the estimate after the last fold")
			assert.Equal(t, max(firstBefore-lastAfter, 0), meta["tokens_saved"], "tokens_saved is not before less after")
			assert.Positive(t, firstBefore, "the first fold armed with no estimate")
			assert.Equal(t, true, meta["turns_folded"], "a fold folds turns")
			assert.Equal(t, false, meta["tool_results_stubbed"], "a fold stubs no tool result")
		})
	}
}
