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
	"fmt"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/skills"
)

// ============================================================================
// Tool handlers - Core Agent
// ============================================================================

func (b *LoomBridge) handleWeave(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return callGRPC(ctx, WeaveRequestTimeout, args,
		func() *loomv1.WeaveRequest { return &loomv1.WeaveRequest{} },
		b.client.Weave,
	)
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
