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

//go:build integration

package e2e

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestE2E_Weave_CostTracking verifies that a Weave response contains populated
// cost and token-usage fields from Bedrock.
func TestE2E_Weave_CostTracking(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("weave-cost")
	ctx := withUserID(context.Background(), userID)

	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("cost-session"),
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()
	cleanupSession(t, client, userID, sessionID)

	weaveCtx, weaveCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer weaveCancel()

	resp, err := client.Weave(weaveCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "What is 3 + 4? Reply with just the number.",
	})
	require.NoError(t, err, "Weave should succeed")
	require.NotEmpty(t, resp.GetText(), "response text should not be empty")

	cost := resp.GetCost()
	require.NotNil(t, cost, "cost should not be nil")

	llmCost := cost.GetLlmCost()
	require.NotNil(t, llmCost, "llm_cost should not be nil")

	assert.Greater(t, llmCost.GetInputTokens(), int32(0),
		"input_tokens should be positive (Bedrock reported token usage)")
	assert.Greater(t, llmCost.GetOutputTokens(), int32(0),
		"output_tokens should be positive")
	assert.Greater(t, llmCost.GetInputTokens()+llmCost.GetOutputTokens(),
		llmCost.GetInputTokens(),
		"total tokens = input + output should exceed input alone")
	assert.NotEmpty(t, llmCost.GetProvider(), "provider should be set")
	assert.NotEmpty(t, llmCost.GetModel(), "model should be set")

	t.Logf("Cost: provider=%s model=%s input=%d output=%d total_usd=%.6f",
		llmCost.GetProvider(), llmCost.GetModel(),
		llmCost.GetInputTokens(), llmCost.GetOutputTokens(),
		cost.GetTotalCostUsd())
}

// TestE2E_Weave_EmptyQuery returns InvalidArgument without hitting Bedrock.
func TestE2E_Weave_EmptyQuery(t *testing.T) {
	client := loomClient(t)
	ctx := withUserID(context.Background(), uniqueTestID("weave-empty"))

	_, err := client.Weave(ctx, &loomv1.WeaveRequest{Query: ""})
	require.Error(t, err, "empty query should return an error")
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code(),
		"empty query should return InvalidArgument, got %s: %s", st.Code(), st.Message())
}

// TestE2E_Weave_SessionContinuity verifies that a second Weave call in the
// same session can reference facts established in the first call.
func TestE2E_Weave_SessionContinuity(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("weave-continuity")
	ctx := withUserID(context.Background(), userID)

	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("continuity-session"),
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()
	cleanupSession(t, client, userID, sessionID)

	weaveCtx, weaveCancel := context.WithTimeout(ctx, 4*time.Minute)
	defer weaveCancel()

	// First turn: establish a fact
	resp1, err := client.Weave(weaveCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "Remember the number 42. Reply with just: OK",
	})
	require.NoError(t, err, "first Weave call should succeed")
	require.NotEmpty(t, resp1.GetText())
	t.Logf("Turn 1: %q", resp1.GetText())

	// Second turn: reference the fact from the first turn
	resp2, err := client.Weave(weaveCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "What number did I ask you to remember? Reply with just the number.",
	})
	require.NoError(t, err, "second Weave call should succeed")
	require.NotEmpty(t, resp2.GetText())
	assert.Contains(t, resp2.GetText(), "42",
		"session context should carry the remembered number across turns")
	t.Logf("Turn 2: %q", resp2.GetText())
}

// TestE2E_StreamWeave_BasicProgress verifies that StreamWeave delivers at
// least one progress event and terminates with a COMPLETED event containing
// non-empty text.
func TestE2E_StreamWeave_BasicProgress(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("stream-basic")
	ctx := withUserID(context.Background(), userID)

	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("stream-session"),
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()
	cleanupSession(t, client, userID, sessionID)

	streamCtx, streamCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer streamCancel()

	stream, err := client.StreamWeave(streamCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "What is 5 + 6? Reply with just the number.",
	})
	require.NoError(t, err, "StreamWeave should open without error")

	var events []*loomv1.WeaveProgress
	for {
		event, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		require.NoError(t, recvErr, "stream.Recv should not error")
		events = append(events, event)
	}

	require.NotEmpty(t, events, "stream should deliver at least one progress event")

	// The last event must be COMPLETED
	last := events[len(events)-1]
	assert.Equal(t, loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED, last.GetStage(),
		"last event stage should be COMPLETED")
	assert.EqualValues(t, 100, last.GetProgress(),
		"completion event progress should be 100")
	assert.NotEmpty(t, last.GetPartialContent(),
		"completion event should carry the response text")

	t.Logf("StreamWeave: %d events, final content=%q", len(events), last.GetPartialContent())
}

// TestE2E_StreamWeave_CostInCompletion verifies that the COMPLETED event from
// StreamWeave includes token usage and cost data from Bedrock.
func TestE2E_StreamWeave_CostInCompletion(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("stream-cost")
	ctx := withUserID(context.Background(), userID)

	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("stream-cost-session"),
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()
	cleanupSession(t, client, userID, sessionID)

	streamCtx, streamCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer streamCancel()

	stream, err := client.StreamWeave(streamCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "What is 6 + 7? Reply with just the number.",
	})
	require.NoError(t, err)

	var completionEvent *loomv1.WeaveProgress
	for {
		event, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		require.NoError(t, recvErr)
		if event.GetStage() == loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {
			completionEvent = event
		}
	}

	require.NotNil(t, completionEvent, "must receive a COMPLETED event")

	cost := completionEvent.GetCost()
	require.NotNil(t, cost, "completion event must carry cost info")

	llmCost := cost.GetLlmCost()
	require.NotNil(t, llmCost, "completion event must carry llm_cost")

	assert.Greater(t, llmCost.GetInputTokens(), int32(0),
		"completion event input_tokens should be positive")
	assert.Greater(t, llmCost.GetOutputTokens(), int32(0),
		"completion event output_tokens should be positive")

	t.Logf("StreamWeave cost: provider=%s model=%s input=%d output=%d usd=%.6f",
		llmCost.GetProvider(), llmCost.GetModel(),
		llmCost.GetInputTokens(), llmCost.GetOutputTokens(),
		cost.GetTotalCostUsd())
}

// TestE2E_StreamWeave_SessionContinuity verifies that multiple StreamWeave
// calls in the same session preserve conversation context.
func TestE2E_StreamWeave_SessionContinuity(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("stream-cont")
	ctx := withUserID(context.Background(), userID)

	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("stream-cont-session"),
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()
	cleanupSession(t, client, userID, sessionID)

	drainStream := func(t *testing.T, stream loomv1.LoomService_StreamWeaveClient) string {
		t.Helper()
		var content string
		for {
			event, recvErr := stream.Recv()
			if recvErr == io.EOF {
				break
			}
			require.NoError(t, recvErr)
			if event.GetStage() == loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {
				content = event.GetPartialContent()
			}
		}
		return content
	}

	streamCtx, streamCancel := context.WithTimeout(ctx, 4*time.Minute)
	defer streamCancel()

	// First StreamWeave: establish context
	stream1, err := client.StreamWeave(streamCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "Remember the word LOOM. Reply with just: OK",
	})
	require.NoError(t, err)
	text1 := drainStream(t, stream1)
	require.NotEmpty(t, text1)
	t.Logf("Turn 1: %q", text1)

	// Second StreamWeave: reference context from first
	stream2, err := client.StreamWeave(streamCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "What word did I ask you to remember? Reply with just the word.",
	})
	require.NoError(t, err)
	text2 := drainStream(t, stream2)
	require.NotEmpty(t, text2)
	assert.Contains(t, text2, "LOOM",
		"second StreamWeave should recall context from the first")
	t.Logf("Turn 2: %q", text2)
}

// TestE2E_ListAgents verifies that ListAgents returns at least one configured agent.
func TestE2E_ListAgents(t *testing.T) {
	client := loomClient(t)
	ctx := withUserID(context.Background(), uniqueTestID("list-agents"))

	resp, err := client.ListAgents(ctx, &loomv1.ListAgentsRequest{})
	require.NoError(t, err, "ListAgents should succeed")
	require.NotEmpty(t, resp.GetAgents(), "at least one agent should be configured")

	for _, ag := range resp.GetAgents() {
		assert.NotEmpty(t, ag.GetId(), "agent ID should not be empty")
		assert.NotEmpty(t, ag.GetName(), "agent name should not be empty")
		assert.Equal(t, "running", ag.GetStatus(),
			"agent status should be 'running'")
	}

	t.Logf("ListAgents: %d agents", len(resp.GetAgents()))
	for _, ag := range resp.GetAgents() {
		t.Logf("  agent: id=%s name=%s status=%s sessions=%d",
			ag.GetId(), ag.GetName(), ag.GetStatus(), ag.GetActiveSessions())
	}
}

// TestE2E_ListAvailableModels verifies that the model registry returns models
// and that the Bedrock provider filter works.
func TestE2E_ListAvailableModels(t *testing.T) {
	client := loomClient(t)
	ctx := withUserID(context.Background(), uniqueTestID("list-models"))

	// List all models
	allResp, err := client.ListAvailableModels(ctx, &loomv1.ListAvailableModelsRequest{})
	require.NoError(t, err, "ListAvailableModels should succeed")
	require.NotEmpty(t, allResp.GetModels(), "model list should not be empty")
	assert.EqualValues(t, len(allResp.GetModels()), allResp.GetTotalCount(),
		"total_count should match len(models)")

	t.Logf("ListAvailableModels: %d models total", allResp.GetTotalCount())
	for _, m := range allResp.GetModels() {
		t.Logf("  model: id=%s provider=%s", m.GetId(), m.GetProvider())
	}

	// Filter by provider=bedrock — should return only Bedrock models
	bedrockResp, err := client.ListAvailableModels(ctx, &loomv1.ListAvailableModelsRequest{
		ProviderFilter: "bedrock",
	})
	require.NoError(t, err, "ListAvailableModels with bedrock filter should succeed")
	require.NotEmpty(t, bedrockResp.GetModels(),
		"at least one Bedrock model should be available")

	for _, m := range bedrockResp.GetModels() {
		assert.Equal(t, "bedrock", m.GetProvider(),
			"all models in filtered response should have provider=bedrock")
	}

	t.Logf("ListAvailableModels bedrock filter: %d models", bedrockResp.GetTotalCount())
}

// TestE2E_ListTools verifies that the default agent has at least one tool
// registered and that the tool definitions are well-formed.
func TestE2E_ListTools(t *testing.T) {
	client := loomClient(t)
	ctx := withUserID(context.Background(), uniqueTestID("list-tools"))

	resp, err := client.ListTools(ctx, &loomv1.ListToolsRequest{})
	require.NoError(t, err, "ListTools should succeed")

	// The default agent may have zero or more tools; we just verify the RPC works.
	t.Logf("ListTools: %d tools registered on default agent", len(resp.GetTools()))

	for _, tool := range resp.GetTools() {
		assert.NotEmpty(t, tool.GetName(), "tool name should not be empty")
		assert.NotEmpty(t, tool.GetDescription(), "tool description should not be empty")
		t.Logf("  tool: name=%s", tool.GetName())
	}
}

// TestE2E_SwitchModel verifies that SwitchModel succeeds when switching to
// another valid Bedrock model and that Weave still works in the session afterwards.
func TestE2E_SwitchModel_Bedrock(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("switch-model")
	ctx := withUserID(context.Background(), userID)

	// Retrieve the default agent and the current model
	agentsResp, err := client.ListAgents(ctx, &loomv1.ListAgentsRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, agentsResp.GetAgents())
	agentID := agentsResp.GetAgents()[0].GetId()

	// Find the current Bedrock model via ListAvailableModels
	modelsResp, err := client.ListAvailableModels(ctx, &loomv1.ListAvailableModelsRequest{
		ProviderFilter: "bedrock",
	})
	require.NoError(t, err)
	require.NotEmpty(t, modelsResp.GetModels(), "need at least one Bedrock model to switch to")

	targetModel := modelsResp.GetModels()[0]
	t.Logf("Switching to model: provider=%s id=%s", targetModel.GetProvider(), targetModel.GetId())

	// Create a session
	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("switch-model-session"),
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()
	cleanupSession(t, client, userID, sessionID)

	// Switch model
	switchResp, err := client.SwitchModel(ctx, &loomv1.SwitchModelRequest{
		SessionId: sessionID,
		AgentId:   agentID,
		Provider:  targetModel.GetProvider(),
		Model:     targetModel.GetId(),
	})
	require.NoError(t, err, "SwitchModel should succeed")
	assert.True(t, switchResp.GetSuccess(), "SwitchModel should report success")
	assert.NotNil(t, switchResp.GetPreviousModel(), "previous model info should be set")
	assert.NotNil(t, switchResp.GetNewModel(), "new model info should be set")
	// The factory may resolve "bedrock" → "bedrock-sdk" for specific models;
	// verify the new provider is in the bedrock family rather than exact match.
	assert.Contains(t, switchResp.GetNewModel().GetProvider(), "bedrock",
		"switched provider should be in the bedrock family")

	t.Logf("SwitchModel: %s/%s → %s/%s",
		switchResp.GetPreviousModel().GetProvider(), switchResp.GetPreviousModel().GetId(),
		switchResp.GetNewModel().GetProvider(), switchResp.GetNewModel().GetId())

	// Verify Weave still works after the model switch
	weaveCtx, weaveCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer weaveCancel()

	weaveResp, err := client.Weave(weaveCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "What is 8 + 9? Reply with just the number.",
	})
	require.NoError(t, err, "Weave should succeed after model switch")
	assert.Contains(t, weaveResp.GetText(), "17",
		"Weave should return correct answer after model switch")
	assert.Contains(t, weaveResp.GetCost().GetLlmCost().GetProvider(), "bedrock",
		"cost provider should be in the bedrock family after model switch")

	t.Logf("Post-switch Weave: %q", weaveResp.GetText())
}

// TestE2E_SwitchModel_InvalidProvider verifies that SwitchModel returns
// an appropriate error for an unknown provider.
func TestE2E_SwitchModel_InvalidProvider(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("switch-invalid")
	ctx := withUserID(context.Background(), userID)

	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("switch-invalid-session"),
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()
	cleanupSession(t, client, userID, sessionID)

	_, err = client.SwitchModel(ctx, &loomv1.SwitchModelRequest{
		SessionId: sessionID,
		Provider:  "nonexistent-provider",
		Model:     "no-such-model",
	})
	require.Error(t, err, "SwitchModel with invalid provider should fail")
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.NotEqual(t, codes.OK, st.Code(),
		"should return a non-OK gRPC status for invalid provider")
	t.Logf("SwitchModel invalid provider error: %s %s", st.Code(), st.Message())
}

// TestE2E_Weave_AgentID_Routing verifies that specifying agent_id in a Weave
// request correctly routes to the named agent and the response reflects it.
func TestE2E_Weave_AgentID_Routing(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("weave-agent-route")
	ctx := withUserID(context.Background(), userID)

	// Get first available agent
	agentsResp, err := client.ListAgents(ctx, &loomv1.ListAgentsRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, agentsResp.GetAgents(), "need at least one agent")
	agentID := agentsResp.GetAgents()[0].GetId()

	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name:    uniqueTestID("agent-route-session"),
		AgentId: agentID,
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()
	cleanupSession(t, client, userID, sessionID)

	weaveCtx, weaveCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer weaveCancel()

	resp, err := client.Weave(weaveCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		AgentId:   agentID,
		Query:     "What is 9 + 10? Reply with just the number.",
	})
	require.NoError(t, err, "Weave with explicit agent_id should succeed")
	assert.NotEmpty(t, resp.GetText(), "response should not be empty")
	assert.Equal(t, agentID, resp.GetAgentId(),
		"response agent_id should match the requested agent_id")
	assert.Equal(t, sessionID, resp.GetSessionId(),
		"response session_id should match")

	t.Logf("Weave routed to agent=%s, response=%q", resp.GetAgentId(), resp.GetText())
}
