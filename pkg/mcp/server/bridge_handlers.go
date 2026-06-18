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

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/orchestration"
	"github.com/teradata-labs/loom/pkg/skills"
	"google.golang.org/protobuf/encoding/protojson"
)

// ============================================================================
// Tool handlers - Core Agent
// ============================================================================

// handleWeave calls Weave and returns MCP text: a routing preamble (see weaveRoutingPreamble), a newline,
// then one JSON object for WeaveResponse (protojson, camelCase field names such as agentId and sessionId).
func (b *LoomBridge) handleWeave(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	explicitAgent := weaveArgsHaveExplicitAgentID(args)
	prepared := prepareWeaveMCPArgsForProtoJSON(args)
	req := &loomv1.WeaveRequest{}
	if len(prepared) > 0 {
		argsJSON, err := json.Marshal(prepared)
		if err != nil {
			return nil, fmt.Errorf("marshal args: %w", err)
		}
		if err := protojson.Unmarshal(argsJSON, req); err != nil {
			return nil, fmt.Errorf("unmarshal args to proto: %w", err)
		}
	}
	rpcCtx, cancel := context.WithTimeout(ctx, WeaveRequestTimeout)
	defer cancel()
	resp, err := b.client.Weave(rpcCtx, req)
	if err != nil {
		return nil, err
	}
	respJSON, err := protojson.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	preamble := b.weaveRoutingPreamble(ctx, explicitAgent, resp.GetAgentId())
	body := string(respJSON)
	if preamble != "" {
		body = preamble + "\n" + body
	}
	return &protocol.CallToolResult{
		Content: []protocol.Content{{Type: "text", Text: body}},
	}, nil
}

// handleWeaveStream runs loom_weave over the StreamWeave RPC, forwarding each
// progress update (0-100) as an MCP progress notification via emit, then returns
// the final answer assembled from the terminal completion event. Unlike
// handleWeave it issues no second unary call: StreamWeave's COMPLETED event
// already carries the full result (see MultiAgentServer.StreamWeave).
//
// The routing preamble that handleWeave prepends is intentionally omitted here:
// the stream does not expose the resolved agent id, and emitting a preamble with
// an empty id would be misleading. The result is the agent's answer text.
func (b *LoomBridge) handleWeaveStream(ctx context.Context, args map[string]interface{}, emit ProgressEmitter) (*protocol.CallToolResult, error) {
	prepared := prepareWeaveMCPArgsForProtoJSON(args)
	req := &loomv1.WeaveRequest{}
	if len(prepared) > 0 {
		argsJSON, err := json.Marshal(prepared)
		if err != nil {
			return nil, fmt.Errorf("marshal args: %w", err)
		}
		if err := protojson.Unmarshal(argsJSON, req); err != nil {
			return nil, fmt.Errorf("unmarshal args to proto: %w", err)
		}
	}

	rpcCtx, cancel := context.WithTimeout(ctx, WeaveRequestTimeout)
	defer cancel()

	stream, err := b.client.StreamWeave(rpcCtx, req)
	if err != nil {
		return nil, err
	}

	var finalContent string
	for {
		prog, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if prog.GetStage() == loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {
			finalContent = prog.GetPartialContent()
			if finalContent == "" && prog.GetPartialResult() != nil {
				finalContent = prog.GetPartialResult().GetDataJson()
			}
			continue
		}
		// Stream the agent's answer to the client as it generates: token-stream
		// events carry the cumulative partial response, forwarded via the MCP
		// `message` field so the client renders the text forming rather than a
		// progress bar. Only token-stream content is forwarded (not stage
		// descriptions) so every message is unambiguously answer text.
		if prog.GetIsTokenStream() {
			if pc := prog.GetPartialContent(); pc != "" {
				_ = emit.EmitMessage(pc)
			}
		}
	}

	return &protocol.CallToolResult{
		Content: []protocol.Content{{Type: "text", Text: finalContent}},
	}, nil
}

// buildPromptForWeaver wraps a loom_build request into an instruction for the
// builder agent: design + CREATE + SAVE the artifact, then report how to run it.
// This is the *query* the edge sends to the weaver (not a stored agent prompt),
// shaped so the weaver's reply gives the MCP client runnable instructions.
func buildPromptForWeaver(intent, kind, name string, toolsHint []string) string {
	what := "agent or workflow"
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "agent":
		what = "agent"
	case "workflow":
		what = "workflow"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "BUILD REQUEST (from an external MCP client). Design and CREATE a Loom %s, then SAVE it so it can be run later (persist an agent via agent_management; save a workflow as YAML/pattern). Build it now — do not ask for confirmation; if the request is ambiguous, make reasonable choices and note them.\n\n", what)
	fmt.Fprintf(&sb, "What to build:\n%s\n", strings.TrimSpace(intent))
	if n := strings.TrimSpace(name); n != "" {
		fmt.Fprintf(&sb, "\nPreferred name: %s\n", n)
	}
	if len(toolsHint) > 0 {
		fmt.Fprintf(&sb, "\nCapabilities it should use: %s\n", strings.Join(toolsHint, ", "))
	}
	sb.WriteString("\nWhen finished, report concisely:\n- What you created: the agent_id(s) and/or workflow name + saved path.\n- A one-line description of what it does.\n- Exactly how to run it from MCP: e.g. loom_execute_workflow with the workflow_yaml, or loom_weave with agent_id=<id>.")
	return sb.String()
}

// buildWeaveRequestFromArgs constructs the WeaveRequest for loom_build. The agent
// is PINNED to the configured builder (the weaver) — a client cannot override it,
// which is the whole point: authoring always goes to the expert. session_id flows
// through so a client can refine a previous build.
func (b *LoomBridge) buildWeaveRequestFromArgs(args map[string]interface{}) (*loomv1.WeaveRequest, error) {
	intent, _ := args["intent"].(string)
	if strings.TrimSpace(intent) == "" {
		return nil, fmt.Errorf("intent is required")
	}
	kind, _ := args["kind"].(string)
	name, _ := args["name"].(string)
	var toolsHint []string
	if raw, ok := args["tools_hint"].([]interface{}); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				toolsHint = append(toolsHint, strings.TrimSpace(s))
			}
		}
	}
	req := &loomv1.WeaveRequest{
		Query:   buildPromptForWeaver(intent, kind, name, toolsHint),
		AgentId: b.builderAgentOrDefault(),
	}
	if sid, ok := args["session_id"].(string); ok && strings.TrimSpace(sid) != "" {
		req.SessionId = strings.TrimSpace(sid)
	}
	return req, nil
}

// handleBuild is the synchronous fallback for loom_build (non-streaming clients).
// It delegates authoring to the builder agent via the unary Weave RPC.
func (b *LoomBridge) handleBuild(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	req, err := b.buildWeaveRequestFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	rpcCtx, cancel := context.WithTimeout(ctx, WeaveRequestTimeout)
	defer cancel()
	resp, err := b.client.Weave(rpcCtx, req)
	if err != nil {
		return nil, err
	}
	respJSON, err := protojson.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	body := fmt.Sprintf("built_by: %s\n%s", b.builderAgentOrDefault(), string(respJSON))
	return &protocol.CallToolResult{
		Content: []protocol.Content{{Type: "text", Text: body}},
	}, nil
}

// handleBuildStream is the streaming path for loom_build: same builder-pinned
// delegation as handleBuild, but over StreamWeave so the client sees the weaver's
// authoring progress as it forms.
func (b *LoomBridge) handleBuildStream(ctx context.Context, args map[string]interface{}, emit ProgressEmitter) (*protocol.CallToolResult, error) {
	req, err := b.buildWeaveRequestFromArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	rpcCtx, cancel := context.WithTimeout(ctx, WeaveRequestTimeout)
	defer cancel()
	stream, err := b.client.StreamWeave(rpcCtx, req)
	if err != nil {
		return nil, err
	}
	var finalContent string
	for {
		prog, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if prog.GetStage() == loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {
			finalContent = prog.GetPartialContent()
			if finalContent == "" && prog.GetPartialResult() != nil {
				finalContent = prog.GetPartialResult().GetDataJson()
			}
			continue
		}
		if prog.GetIsTokenStream() {
			if pc := prog.GetPartialContent(); pc != "" {
				_ = emit.EmitMessage(pc)
			}
		}
	}
	return &protocol.CallToolResult{
		Content: []protocol.Content{{Type: "text", Text: finalContent}},
	}, nil
}

// handleWorkflowStream runs loom_execute_workflow over the StreamWorkflow RPC,
// forwarding each WorkflowProgress as a status message (current agent + stage)
// and returning the agents' final outputs as the result. This is the reliable
// multi-agent path: the workflow orchestrator wires agents and message queues
// itself, so it does not depend on an LLM correctly threading ephemeral agent
// IDs through send_message.
//
// The workflow may be supplied as `workflow_yaml` (a YAML workflow spec — far
// easier for a model to produce than a protojson pattern oneof) or as an inline
// `pattern` on the request. workflow_yaml wins when both are present.
func (b *LoomBridge) handleWorkflowStream(ctx context.Context, args map[string]interface{}, emit ProgressEmitter) (*protocol.CallToolResult, error) {
	// workflow_yaml is a convenience arg, not an ExecuteWorkflowRequest field;
	// pull it out before protojson so the rest unmarshals cleanly.
	yamlSpec, _ := args["workflow_yaml"].(string)
	rest := make(map[string]interface{}, len(args))
	for k, v := range args {
		if k == "workflow_yaml" {
			continue
		}
		rest[k] = v
	}

	req := &loomv1.ExecuteWorkflowRequest{}
	if len(rest) > 0 {
		argsJSON, err := json.Marshal(rest)
		if err != nil {
			return nil, fmt.Errorf("marshal workflow args: %w", err)
		}
		if err := protojson.Unmarshal(argsJSON, req); err != nil {
			return nil, fmt.Errorf("unmarshal workflow args to proto: %w", err)
		}
	}

	if strings.TrimSpace(yamlSpec) != "" {
		pattern, err := orchestration.LoadWorkflowFromYAMLBytes([]byte(yamlSpec))
		if err != nil {
			return errorResult(fmt.Sprintf("invalid workflow_yaml: %v", err)), nil
		}
		req.Pattern = pattern
	}
	if req.GetPattern() == nil {
		return errorResult("a workflow is required: pass workflow_yaml (a YAML workflow spec) or an inline pattern"), nil
	}

	rpcCtx, cancel := context.WithTimeout(ctx, WeaveRequestTimeout)
	defer cancel()

	stream, err := b.client.StreamWorkflow(rpcCtx, req)
	if err != nil {
		return nil, err
	}

	// Keep the latest output per agent (events may repeat/refine partial results),
	// preserving first-seen order so the final reads as the pipeline ran.
	latest := make(map[string]string)
	var order []string
	for {
		prog, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if line := workflowProgressLine(prog); line != "" {
			_ = emit.EmitMessage(line)
		}
		for _, r := range prog.GetPartialResults() {
			if _, seen := latest[r.GetAgentId()]; !seen {
				order = append(order, r.GetAgentId())
			}
			latest[r.GetAgentId()] = r.GetOutput()
		}
	}

	var sb strings.Builder
	for _, id := range order {
		if out := strings.TrimSpace(latest[id]); out != "" {
			fmt.Fprintf(&sb, "## %s\n%s\n\n", id, out)
		}
	}
	final := strings.TrimSpace(sb.String())
	if final == "" {
		final = "Workflow completed (no agent output was captured)."
	}
	return &protocol.CallToolResult{
		Content: []protocol.Content{{Type: "text", Text: final}},
	}, nil
}

// workflowProgressLine renders a WorkflowProgress as a one-line status for the
// client: "<current-agent>: <message>" (either part may be empty).
func workflowProgressLine(p *loomv1.WorkflowProgress) string {
	agent := p.GetCurrentAgentId()
	msg := p.GetMessage()
	switch {
	case agent != "" && msg != "":
		return agent + ": " + msg
	case agent != "":
		return agent
	default:
		return msg
	}
}

// ============================================================================
// Tool handlers - Patterns
// ============================================================================

func (b *LoomBridge) handleLoadPatterns(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.LoadPatternsRequest { return &loomv1.LoadPatternsRequest{} },
		b.client.LoadPatterns,
	)
}

func (b *LoomBridge) handleListPatterns(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ListPatternsRequest { return &loomv1.ListPatternsRequest{} },
		b.client.ListPatterns,
	)
}

func (b *LoomBridge) handleGetPattern(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.GetPatternRequest { return &loomv1.GetPatternRequest{} },
		b.client.GetPattern,
	)
}

func (b *LoomBridge) handleCreatePattern(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.CreatePatternRequest { return &loomv1.CreatePatternRequest{} },
		b.client.CreatePattern,
	)
}

// ============================================================================
// Tool handlers - Sessions
// ============================================================================

func (b *LoomBridge) handleCreateSession(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.CreateSessionRequest { return &loomv1.CreateSessionRequest{} },
		b.client.CreateSession,
	)
}

func (b *LoomBridge) handleGetSession(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.GetSessionRequest { return &loomv1.GetSessionRequest{} },
		b.client.GetSession,
	)
}

func (b *LoomBridge) handleListSessions(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ListSessionsRequest { return &loomv1.ListSessionsRequest{} },
		b.client.ListSessions,
	)
}

func (b *LoomBridge) handleDeleteSession(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.DeleteSessionRequest { return &loomv1.DeleteSessionRequest{} },
		b.client.DeleteSession,
	)
}

func (b *LoomBridge) handleGetConversationHistory(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.GetConversationHistoryRequest { return &loomv1.GetConversationHistoryRequest{} },
		b.client.GetConversationHistory,
	)
}

func (b *LoomBridge) handleAnswerClarification(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.AnswerClarificationRequest { return &loomv1.AnswerClarificationRequest{} },
		b.client.AnswerClarificationQuestion,
	)
}

// ============================================================================
// Tool handlers - Tools & Observability
// ============================================================================

func (b *LoomBridge) handleRegisterTool(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	// RegisterToolRequest expects a nested "tool" field with ToolDefinition.
	// Wrap flat MCP args (name, description, input_schema_json) into the nested structure.
	wrapped := map[string]interface{}{
		"tool": args,
	}
	return callGRPC(ctx, b.requestTimeout, wrapped,
		func() *loomv1.RegisterToolRequest { return &loomv1.RegisterToolRequest{} },
		b.client.RegisterTool,
	)
}

func (b *LoomBridge) handleListTools(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ListToolsRequest { return &loomv1.ListToolsRequest{} },
		b.client.ListTools,
	)
}

func (b *LoomBridge) handleGetHealth(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.GetHealthRequest { return &loomv1.GetHealthRequest{} },
		b.client.GetHealth,
	)
}

func (b *LoomBridge) handleGetTrace(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.GetTraceRequest { return &loomv1.GetTraceRequest{} },
		b.client.GetTrace,
	)
}

func (b *LoomBridge) handleRequestToolPermission(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ToolPermissionRequest { return &loomv1.ToolPermissionRequest{} },
		b.client.RequestToolPermission,
	)
}

// ============================================================================
// Tool handlers - Agent Management
// ============================================================================

func (b *LoomBridge) handleCreateAgent(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.CreateAgentRequest { return &loomv1.CreateAgentRequest{} },
		b.client.CreateAgentFromConfig,
	)
}

func (b *LoomBridge) handleListAgents(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ListAgentsRequest { return &loomv1.ListAgentsRequest{} },
		b.client.ListAgents,
	)
}

func (b *LoomBridge) handleGetAgent(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.GetAgentRequest { return &loomv1.GetAgentRequest{} },
		b.client.GetAgent,
	)
}

func (b *LoomBridge) handleStartAgent(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.StartAgentRequest { return &loomv1.StartAgentRequest{} },
		b.client.StartAgent,
	)
}

func (b *LoomBridge) handleStopAgent(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.StopAgentRequest { return &loomv1.StopAgentRequest{} },
		b.client.StopAgent,
	)
}

func (b *LoomBridge) handleDeleteAgent(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.DeleteAgentRequest { return &loomv1.DeleteAgentRequest{} },
		b.client.DeleteAgent,
	)
}

func (b *LoomBridge) handleReloadAgent(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ReloadAgentRequest { return &loomv1.ReloadAgentRequest{} },
		b.client.ReloadAgent,
	)
}

// ============================================================================
// Tool handlers - Models
// ============================================================================

func (b *LoomBridge) handleSwitchModel(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.SwitchModelRequest { return &loomv1.SwitchModelRequest{} },
		b.client.SwitchModel,
	)
}

func (b *LoomBridge) handleListModels(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ListAvailableModelsRequest { return &loomv1.ListAvailableModelsRequest{} },
		b.client.ListAvailableModels,
	)
}

// ============================================================================
// Tool handlers - Workflow Orchestration
// ============================================================================

func (b *LoomBridge) handleExecuteWorkflow(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ExecuteWorkflowRequest { return &loomv1.ExecuteWorkflowRequest{} },
		b.client.ExecuteWorkflow,
	)
}

func (b *LoomBridge) handleGetWorkflowExecution(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.GetWorkflowExecutionRequest { return &loomv1.GetWorkflowExecutionRequest{} },
		b.client.GetWorkflowExecution,
	)
}

func (b *LoomBridge) handleListWorkflowExecutions(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ListWorkflowExecutionsRequest { return &loomv1.ListWorkflowExecutionsRequest{} },
		b.client.ListWorkflowExecutions,
	)
}

func (b *LoomBridge) handleScheduleWorkflow(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ScheduleWorkflowRequest { return &loomv1.ScheduleWorkflowRequest{} },
		b.client.ScheduleWorkflow,
	)
}

func (b *LoomBridge) handleUpdateScheduledWorkflow(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.UpdateScheduledWorkflowRequest { return &loomv1.UpdateScheduledWorkflowRequest{} },
		b.client.UpdateScheduledWorkflow,
	)
}

func (b *LoomBridge) handleGetScheduledWorkflow(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.GetScheduledWorkflowRequest { return &loomv1.GetScheduledWorkflowRequest{} },
		b.client.GetScheduledWorkflow,
	)
}

func (b *LoomBridge) handleListScheduledWorkflows(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ListScheduledWorkflowsRequest { return &loomv1.ListScheduledWorkflowsRequest{} },
		b.client.ListScheduledWorkflows,
	)
}

func (b *LoomBridge) handleDeleteScheduledWorkflow(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.DeleteScheduledWorkflowRequest { return &loomv1.DeleteScheduledWorkflowRequest{} },
		b.client.DeleteScheduledWorkflow,
	)
}

func (b *LoomBridge) handleTriggerScheduledWorkflow(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.TriggerScheduledWorkflowRequest { return &loomv1.TriggerScheduledWorkflowRequest{} },
		b.client.TriggerScheduledWorkflow,
	)
}

func (b *LoomBridge) handlePauseSchedule(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.PauseScheduleRequest { return &loomv1.PauseScheduleRequest{} },
		b.client.PauseSchedule,
	)
}

func (b *LoomBridge) handleResumeSchedule(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ResumeScheduleRequest { return &loomv1.ResumeScheduleRequest{} },
		b.client.ResumeSchedule,
	)
}

func (b *LoomBridge) handleGetScheduleHistory(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.GetScheduleHistoryRequest { return &loomv1.GetScheduleHistoryRequest{} },
		b.client.GetScheduleHistory,
	)
}

// ============================================================================
// Tool handlers - UI Apps
// ============================================================================

func (b *LoomBridge) handleCreateUIApp(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	result, err := callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.CreateUIAppRequest { return &loomv1.CreateUIAppRequest{} },
		b.client.CreateUIApp,
	)
	if err == nil && b.mcpServer != nil {
		b.mcpServer.NotifyResourceListChanged()
	}
	return result, err
}

func (b *LoomBridge) handleUpdateUIApp(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	result, err := callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.UpdateUIAppRequest { return &loomv1.UpdateUIAppRequest{} },
		b.client.UpdateUIApp,
	)
	if err == nil && b.mcpServer != nil {
		b.mcpServer.NotifyResourceListChanged()
	}
	return result, err
}

func (b *LoomBridge) handleDeleteUIApp(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	result, err := callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.DeleteUIAppRequest { return &loomv1.DeleteUIAppRequest{} },
		b.client.DeleteUIApp,
	)
	if err == nil && b.mcpServer != nil {
		b.mcpServer.NotifyResourceListChanged()
	}
	return result, err
}

func (b *LoomBridge) handleListComponentTypes(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ListComponentTypesRequest { return &loomv1.ListComponentTypesRequest{} },
		b.client.ListComponentTypes,
	)
}

// ============================================================================
// Tool handlers - Artifacts
// ============================================================================

func (b *LoomBridge) handleListArtifacts(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.ListArtifactsRequest { return &loomv1.ListArtifactsRequest{} },
		b.client.ListArtifacts,
	)
}

func (b *LoomBridge) handleGetArtifact(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.GetArtifactRequest { return &loomv1.GetArtifactRequest{} },
		b.client.GetArtifact,
	)
}

func (b *LoomBridge) handleUploadArtifact(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	// UploadArtifactRequest.content is a bytes field. Proto JSON expects base64.
	// MCP clients send plain text, so we base64-encode it for protojson compatibility.
	if content, ok := args["content"].(string); ok {
		args["content"] = base64.StdEncoding.EncodeToString([]byte(content))
	}
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.UploadArtifactRequest { return &loomv1.UploadArtifactRequest{} },
		b.client.UploadArtifact,
	)
}

func (b *LoomBridge) handleDeleteArtifact(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.DeleteArtifactRequest { return &loomv1.DeleteArtifactRequest{} },
		b.client.DeleteArtifact,
	)
}

func (b *LoomBridge) handleSearchArtifacts(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.SearchArtifactsRequest { return &loomv1.SearchArtifactsRequest{} },
		b.client.SearchArtifacts,
	)
}

func (b *LoomBridge) handleGetArtifactContent(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.GetArtifactContentRequest { return &loomv1.GetArtifactContentRequest{} },
		b.client.GetArtifactContent,
	)
}

func (b *LoomBridge) handleGetArtifactStats(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, b.requestTimeout, args,
		func() *loomv1.GetArtifactStatsRequest { return &loomv1.GetArtifactStatsRequest{} },
		b.client.GetArtifactStats,
	)
}

// ============================================================================
// Tool handlers - Skills
// ============================================================================

// errorResult returns a CallToolResult with IsError=true and the given message.
func errorResult(msg string) *protocol.CallToolResult {
	return &protocol.CallToolResult{
		Content: []protocol.Content{{Type: "text", Text: msg}},
		IsError: true,
	}
}

// jsonResult marshals v to JSON and returns it as a successful CallToolResult.
func jsonResult(v interface{}) (*protocol.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}
	return &protocol.CallToolResult{
		Content: []protocol.Content{{Type: "text", Text: string(data)}},
	}, nil
}

func (b *LoomBridge) handleListSkills(_ context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	if b.skillOrchestrator == nil {
		return errorResult("skills not configured"), nil
	}

	domain, _ := args["domain"].(string)

	var skillList []*skills.Skill
	if domain != "" {
		skillList = b.skillOrchestrator.GetLibrary().ListByDomain(domain)
	} else {
		skillList = b.skillOrchestrator.GetLibrary().List()
	}

	summaries := make([]skills.SkillSummary, 0, len(skillList))
	for _, s := range skillList {
		summaries = append(summaries, s.Summary())
	}

	return jsonResult(summaries)
}

func (b *LoomBridge) handleGetSkill(_ context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	if b.skillOrchestrator == nil {
		return errorResult("skills not configured"), nil
	}

	name, _ := args["name"].(string)
	if name == "" {
		return errorResult("name is required"), nil
	}

	skill, err := b.skillOrchestrator.GetLibrary().Load(name)
	if err != nil {
		return errorResult(fmt.Sprintf("skill not found: %s", name)), nil
	}

	return jsonResult(skill)
}

func (b *LoomBridge) handleCreateSkill(_ context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	if b.skillOrchestrator == nil {
		return errorResult("skills not configured"), nil
	}

	name, _ := args["name"].(string)
	if name == "" {
		return errorResult("name is required"), nil
	}
	domain, _ := args["domain"].(string)
	if domain == "" {
		return errorResult("domain is required"), nil
	}
	instructions, _ := args["instructions"].(string)
	if instructions == "" {
		return errorResult("instructions is required"), nil
	}

	skill := &skills.Skill{
		Name:   name,
		Domain: domain,
		Prompt: skills.SkillPrompt{
			Instructions: instructions,
		},
	}

	// Optional fields.
	if v, ok := args["title"].(string); ok {
		skill.Title = v
	}
	if v, ok := args["description"].(string); ok {
		skill.Description = v
	}
	if v, ok := args["sticky"].(bool); ok {
		skill.Sticky = v
	}
	if v, ok := args["mode"].(string); ok {
		skill.Trigger.Mode = skills.SkillActivationMode(strings.ToUpper(v))
	}
	if v, ok := args["slash_commands"].([]interface{}); ok {
		for _, cmd := range v {
			if s, ok := cmd.(string); ok {
				skill.Trigger.SlashCommands = append(skill.Trigger.SlashCommands, s)
			}
		}
	}
	if v, ok := args["keywords"].([]interface{}); ok {
		for _, kw := range v {
			if s, ok := kw.(string); ok {
				skill.Trigger.Keywords = append(skill.Trigger.Keywords, s)
			}
		}
	}
	if v, ok := args["pattern_refs"].([]interface{}); ok {
		for _, ref := range v {
			if s, ok := ref.(string); ok {
				skill.PatternRefs = append(skill.PatternRefs, s)
			}
		}
	}

	if err := b.skillOrchestrator.GetLibrary().WriteSkill(skill); err != nil {
		return errorResult(fmt.Sprintf("failed to create skill: %v", err)), nil
	}

	return jsonResult(map[string]interface{}{
		"status": "created",
		"skill":  skill.Summary(),
	})
}

func (b *LoomBridge) handleActivateSkill(_ context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	if b.skillOrchestrator == nil {
		return errorResult("skills not configured"), nil
	}

	name, _ := args["name"].(string)
	if name == "" {
		return errorResult("name is required"), nil
	}
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return errorResult("session_id is required"), nil
	}

	skill, err := b.skillOrchestrator.GetLibrary().Load(name)
	if err != nil {
		return errorResult(fmt.Sprintf("skill not found: %s", name)), nil
	}

	active := b.skillOrchestrator.ActivateSkill(sessionID, skill, "api", name, 1.0)

	return jsonResult(map[string]interface{}{
		"status":       "activated",
		"skill":        active.Skill.Name,
		"session_id":   active.SessionID,
		"trigger_type": active.TriggerType,
		"activated_at": active.ActivatedAt,
	})
}

func (b *LoomBridge) handleDeactivateSkill(_ context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	if b.skillOrchestrator == nil {
		return errorResult("skills not configured"), nil
	}

	name, _ := args["name"].(string)
	if name == "" {
		return errorResult("name is required"), nil
	}
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return errorResult("session_id is required"), nil
	}

	b.skillOrchestrator.DeactivateSkill(sessionID, name)

	return jsonResult(map[string]interface{}{
		"status":     "deactivated",
		"skill":      name,
		"session_id": sessionID,
	})
}
