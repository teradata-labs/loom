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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkflowCommPrompt_AbsentWithoutATeam proves the default: an agent that
// no one attached a workflow context to renders no team block at all. This is
// every standalone agent, every cloud workflow step, and every spawned
// sub-agent that did not auto-subscribe — none of their construction paths
// attaches a context, so the knowledge cannot leak to them.
func TestWorkflowCommPrompt_AbsentWithoutATeam(t *testing.T) {
	ag := NewAgent(nil, nil)
	prompt := ag.getSystemPrompt(context.Background())

	assert.NotContains(t, prompt, "WORKFLOW COMMUNICATION",
		"an agent with no attached team must carry no team block")
	assert.Empty(t, ag.workflowCommPromptSupplement(),
		"the supplement itself is empty when no context is attached")
}

// TestWorkflowCommPrompt_EmptyContextRendersNothing proves an attached but
// empty context still renders nothing: a spawned agent that subscribed to no
// topics and has no peers has nothing true to say.
func TestWorkflowCommPrompt_EmptyContextRendersNothing(t *testing.T) {
	ag := NewAgent(nil, nil)
	ag.SetWorkflowCommunicationContext(&WorkflowCommunicationContext{WorkflowName: "wf"})

	assert.Empty(t, ag.workflowCommPromptSupplement(),
		"a context with no topics and no peers renders no block")
}

// TestWorkflowCommPrompt_PeersRenderWithReplyRule is the coordinator/worker
// case: the peer list is named, and the reply rule is present. The reply rule
// is load-bearing — an agent's turn text is delivered nowhere, so a received
// message travels back only if the agent calls send_message.
func TestWorkflowCommPrompt_PeersRenderWithReplyRule(t *testing.T) {
	ag := NewAgent(nil, nil)
	ag.SetWorkflowCommunicationContext(&WorkflowCommunicationContext{
		WorkflowName:    "audit",
		AvailableAgents: []string{"audit", "audit:analyst"},
	})

	prompt := ag.getSystemPrompt(context.Background())

	assert.Contains(t, prompt, "audit:analyst", "the peer list is named in the prompt")
	assert.Contains(t, prompt, "send_message", "the mechanism is named")
	assert.Contains(t, prompt, "When a message arrives, send your answer back to its sender",
		"the reply rule must be present whenever peers are reachable")
	assert.Contains(t, prompt, "[MESSAGE FROM agent]",
		"the block names the label the server actually injects")
	assert.Equal(t, 1, strings.Count(prompt, "WORKFLOW COMMUNICATION (DIRECT MESSAGING)"),
		"the block is rendered exactly once")
}

// TestWorkflowCommPrompt_TopicsOnlyOmitReplyRule proves a pub-sub-only
// participant (a spawned agent that auto-subscribed) gets the broadcast lines
// and nothing about direct messaging — it has no peer addresses to reply to.
func TestWorkflowCommPrompt_TopicsOnlyOmitReplyRule(t *testing.T) {
	ag := NewAgent(nil, nil)
	ag.SetWorkflowCommunicationContext(&WorkflowCommunicationContext{
		SubscribedTopics: []string{"audit-events"},
	})

	block := ag.workflowCommPromptSupplement()

	require.Contains(t, block, "audit-events")
	assert.Contains(t, block, "publish(")
	assert.NotContains(t, block, "DIRECT MESSAGING",
		"no peers means no direct-messaging section")
	assert.NotContains(t, block, "send your answer back",
		"the reply rule belongs with the addresses it needs")
}

// TestWorkflowCommPrompt_SitsAtTheEndAndIsStable proves placement and
// byte-stability: the block closes ROM (after the skill menu), and it renders
// identically on repeated builds, so it cannot destabilise the ROM slot.
func TestWorkflowCommPrompt_SitsAtTheEndAndIsStable(t *testing.T) {
	ag := NewAgent(nil, nil)
	ag.SetWorkflowCommunicationContext(&WorkflowCommunicationContext{
		AvailableAgents: []string{"audit"},
	})

	first := ag.getSystemPrompt(context.Background())
	second := ag.getSystemPrompt(context.Background())

	assert.Equal(t, first, second, "the prompt is byte-identical across builds")
	assert.True(t, strings.HasSuffix(strings.TrimRight(first, "\n"),
		"→ Do NOT poll - you will be notified automatically"),
		"the team block closes the prompt")
}
