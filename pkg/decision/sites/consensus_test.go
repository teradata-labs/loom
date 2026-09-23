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

package sites

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
)

func TestConsensusRequest(t *testing.T) {
	t.Parallel()
	many := make([]string, maxConsensusArguments+3)
	for i := range many {
		many[i] = strings.Repeat("a", maxConsensusArgumentRunes+5)
	}
	req, err := ConsensusRequest("Indexes or partitions?", []Position{
		{AgentID: "a1", Position: "Use indexes", Arguments: []string{"faster reads"}, Confidence: 0.8},
		{AgentID: "a2", Position: "Use partitions", Arguments: many, Confidence: 0.9},
	})
	require.NoError(t, err)
	assert.Equal(t, SiteDebateConsensus, req.Site)
	require.NotNil(t, req.Questions[QConsensus].GetNoul())

	state := req.State.GetStructValue().AsMap()
	assert.Equal(t, "Indexes or partitions?", state["topic"])
	ps := state["positions"].([]any)
	require.Len(t, ps, 2)
	second := ps[1].(map[string]any)
	args := second["arguments"].([]any)
	assert.Len(t, args, maxConsensusArguments, "argument count bounded")
	assert.True(t, strings.HasSuffix(args[0].(string), "…"), "argument length bounded")
	assert.Equal(t, 0.9, second["confidence"])

	_, err = ConsensusRequest("t", []Position{{Position: "alone"}})
	assert.ErrorIs(t, err, decision.ErrValidation)
}

func TestConsensusReferenceAndVerdict(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "true", ConsensusReference(true)[QConsensus].Answer)
	assert.Equal(t, ReferenceSourceConsensusHeuristic, ConsensusReference(false)[QConsensus].Source)

	req, err := ConsensusRequest("t", []Position{{Position: "x"}, {Position: "y"}})
	require.NoError(t, err)
	out := decision.NewRouter(mock.New().AnswerNoul(QConsensus, 0.15)).Decide(context.Background(), req)
	require.NoError(t, out.Err)
	reached, ok := ConsensusVerdict(out.Response)
	assert.True(t, ok)
	assert.False(t, reached)

	_, ok = ConsensusVerdict(nil)
	assert.False(t, ok)
}
