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

package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestSwarmVotingPromptsPreserveStandpoint pins the voting prompts: the
// question and the reply format are there, the agent's own instructions
// govern the vote, and there is no role framing ("You are participating…"),
// which is what made oppositely configured voters converge on the rig.
func TestSwarmVotingPromptsPreserveStandpoint(t *testing.T) {
	t.Parallel()
	e := &SwarmExecutor{pattern: &loomv1.SwarmPattern{Question: "Kafka or NATS?"}}
	prev := []*loomv1.SwarmVote{{AgentId: "a", Choice: "Kafka", Confidence: 0.9, Reasoning: "mature"}}
	for name, p := range map[string]string{
		"independent":   e.buildVotingPrompt("voter", 1),
		"collaborative": e.buildCollaborativeVotingPrompt("voter", 2, prev),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Contains(t, p, "Question: Kafka or NATS?")
			assert.Contains(t, p, "VOTE: <your choice>")
			assert.Contains(t, p, "CONFIDENCE: <0.0-1.0>")
			assert.Contains(t, p, "your own instructions")
			assert.Contains(t, p, "disagreeing with other voters is expected")
			assert.NotContains(t, p, "You are")
		})
	}
	assert.Contains(t, e.buildCollaborativeVotingPrompt("voter", 2, prev), "Agent a voted: Kafka", "previous votes are still shown when share_votes is on")
}
