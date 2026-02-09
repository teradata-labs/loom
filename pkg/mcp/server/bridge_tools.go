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
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// buildToolHandlers builds the mapping from tool name to handler function.
// Called once during construction; the result is cached on the struct.
func (b *LoomBridge) buildToolHandlers() map[string]toolHandler {
	return map[string]toolHandler{
		// Core Agent
		"loom_weave": b.handleWeave,

		// Patterns
		"loom_load_patterns":  b.handleLoadPatterns,
		"loom_list_patterns":  b.handleListPatterns,
		"loom_get_pattern":    b.handleGetPattern,
		"loom_create_pattern": b.handleCreatePattern,

		// Sessions
		"loom_create_session":           b.handleCreateSession,
		"loom_get_session":              b.handleGetSession,
		"loom_list_sessions":            b.handleListSessions,
		"loom_delete_session":           b.handleDeleteSession,
		"loom_get_conversation_history": b.handleGetConversationHistory,
		"loom_answer_clarification":     b.handleAnswerClarification,

		// Tools & Observability
		"loom_register_tool":           b.handleRegisterTool,
		"loom_list_tools":              b.handleListTools,
		"loom_get_health":              b.handleGetHealth,
		"loom_get_trace":               b.handleGetTrace,
		"loom_request_tool_permission": b.handleRequestToolPermission,

		// Agent Management
		"loom_create_agent": b.handleCreateAgent,
		"loom_list_agents":  b.handleListAgents,
		"loom_get_agent":    b.handleGetAgent,
		"loom_start_agent":  b.handleStartAgent,
		"loom_stop_agent":   b.handleStopAgent,
		"loom_delete_agent": b.handleDeleteAgent,
		"loom_reload_agent": b.handleReloadAgent,

		// Models
		"loom_switch_model": b.handleSwitchModel,
		"loom_list_models":  b.handleListModels,

		// Workflow Orchestration
		"loom_execute_workflow":           b.handleExecuteWorkflow,
		"loom_get_workflow_execution":     b.handleGetWorkflowExecution,
		"loom_list_workflow_executions":   b.handleListWorkflowExecutions,
		"loom_schedule_workflow":          b.handleScheduleWorkflow,
		"loom_update_scheduled_workflow":  b.handleUpdateScheduledWorkflow,
		"loom_get_scheduled_workflow":     b.handleGetScheduledWorkflow,
		"loom_list_scheduled_workflows":   b.handleListScheduledWorkflows,
		"loom_delete_scheduled_workflow":  b.handleDeleteScheduledWorkflow,
		"loom_trigger_scheduled_workflow": b.handleTriggerScheduledWorkflow,
		"loom_pause_schedule":             b.handlePauseSchedule,
		"loom_resume_schedule":            b.handleResumeSchedule,
		"loom_get_schedule_history":       b.handleGetScheduleHistory,

		// Artifacts
		"loom_list_artifacts":       b.handleListArtifacts,
		"loom_get_artifact":         b.handleGetArtifact,
		"loom_upload_artifact":      b.handleUploadArtifact,
		"loom_delete_artifact":      b.handleDeleteArtifact,
		"loom_search_artifacts":     b.handleSearchArtifacts,
		"loom_get_artifact_content": b.handleGetArtifactContent,
		"loom_get_artifact_stats":   b.handleGetArtifactStats,
	}
}

// ============================================================================
// Tool annotation helpers
// ============================================================================

// boolP returns a pointer to a bool value. Used for optional annotation fields.
func boolP(b bool) *bool { return &b }

// readOnlyAnnotation returns annotations for tools that only read data.
// readOnlyHint=true, destructiveHint=false, idempotentHint=true.
func readOnlyAnnotation() *protocol.ToolAnnotations {
	return &protocol.ToolAnnotations{
		ReadOnlyHint:    boolP(true),
		DestructiveHint: boolP(false),
		IdempotentHint:  boolP(true),
	}
}

// destructiveAnnotation returns annotations for tools that delete data.
// destructiveHint=true, readOnlyHint=false.
func destructiveAnnotation() *protocol.ToolAnnotations {
	return &protocol.ToolAnnotations{
		ReadOnlyHint:    boolP(false),
		DestructiveHint: boolP(true),
	}
}

// mutatingAnnotation returns annotations for tools that create or update data.
// readOnlyHint=false, destructiveHint=false.
func mutatingAnnotation() *protocol.ToolAnnotations {
	return &protocol.ToolAnnotations{
		ReadOnlyHint:    boolP(false),
		DestructiveHint: boolP(false),
	}
}

// ============================================================================
// Tool definitions
// ============================================================================

func (b *LoomBridge) buildToolDefinitions() []protocol.Tool {
	conversationViewerURI := "ui://loom/conversation-viewer"

	// Helper for creating a tool with optional UI link and annotations.
	tool := func(name, desc string, schema map[string]interface{}, uiURI string, visibility []string, ann *protocol.ToolAnnotations) protocol.Tool {
		t := protocol.Tool{
			Name:        name,
			Description: desc,
			InputSchema: schema,
			Annotations: ann,
		}
		if uiURI != "" || len(visibility) > 0 {
			protocol.SetUIToolMeta(&t, &protocol.UIToolMeta{
				ResourceURI: uiURI,
				Visibility:  visibility,
			})
		}
		return t
	}

	mv := []string{"model", "app"} // model+app visibility
	av := []string{"app"}          // app-only visibility

	ro := readOnlyAnnotation()
	del := destructiveAnnotation()
	mut := mutatingAnnotation()

	// loom_weave is mutating and open-world (interacts with external LLMs and tools).
	weaveAnn := mutatingAnnotation()
	weaveAnn.OpenWorldHint = boolP(true)

	return []protocol.Tool{
		// Core Agent
		tool("loom_weave", "Execute a query using Loom's agent. The agent selects patterns, executes tools, and weaves LLM intelligence with domain knowledge.", objectSchema(
			prop("query", "string", "The user query or instruction to execute"),
			prop("session_id", "string", "Session ID for conversation continuity (optional)"),
			prop("agent_id", "string", "Agent ID to use (optional, uses default if empty)"),
		), conversationViewerURI, mv, weaveAnn),

		// Patterns
		tool("loom_load_patterns", "Load pattern definitions from a directory.", objectSchema(
			prop("directory", "string", "Path to directory containing pattern YAML files"),
		), "", mv, mut),
		tool("loom_list_patterns", "List available patterns with optional filtering.", objectSchema(
			prop("category", "string", "Filter by category (optional)"),
			prop("tag", "string", "Filter by tag (optional)"),
		), "", mv, ro),
		tool("loom_get_pattern", "Get a specific pattern by name.", objectSchema(
			reqProp("name", "string", "Pattern name"),
		), "", mv, ro),
		tool("loom_create_pattern", "Create a new pattern at runtime.", objectSchema(
			reqProp("name", "string", "Pattern name"),
			prop("description", "string", "Pattern description"),
			prop("template", "string", "Pattern template content"),
			prop("category", "string", "Pattern category"),
		), "", mv, mut),

		// Sessions
		tool("loom_create_session", "Create a new conversation session.", objectSchema(
			prop("agent_id", "string", "Agent ID for this session (optional)"),
		), conversationViewerURI, mv, mut),
		tool("loom_get_session", "Get session details.", objectSchema(
			reqProp("session_id", "string", "Session ID to retrieve"),
		), conversationViewerURI, mv, ro),
		tool("loom_list_sessions", "List all conversation sessions.", objectSchema(
			prop("agent_id", "string", "Filter by agent ID (optional)"),
		), "", mv, ro),
		tool("loom_delete_session", "Delete a session and its history.", objectSchema(
			reqProp("session_id", "string", "Session ID to delete"),
		), "", mv, del),
		tool("loom_get_conversation_history", "Get conversation history for a session. Used by the conversation viewer UI.", objectSchema(
			reqProp("session_id", "string", "Session ID"),
			prop("agent_id", "string", "Agent ID (optional)"),
		), conversationViewerURI, av, ro),
		tool("loom_answer_clarification", "Answer a clarification question from an agent.", objectSchema(
			reqProp("session_id", "string", "Session ID"),
			reqProp("question_id", "string", "Question ID"),
			reqProp("answer", "string", "Answer to the question"),
		), "", mv, mut),

		// Tools & Observability
		tool("loom_register_tool", "Register a new tool dynamically.", objectSchema(
			reqProp("name", "string", "Tool name"),
			prop("description", "string", "Tool description"),
		), "", mv, mut),
		tool("loom_list_tools", "List all registered tools.", objectSchema(), "", mv, ro),
		tool("loom_get_health", "Get health status of the Loom server.", objectSchema(), "", mv, ro),
		tool("loom_get_trace", "Get execution trace for observability.", objectSchema(
			reqProp("trace_id", "string", "Trace ID to retrieve"),
		), "", mv, ro),
		tool("loom_request_tool_permission", "Request permission to execute a tool.", objectSchema(
			reqProp("tool_name", "string", "Name of tool to request permission for"),
			prop("session_id", "string", "Session context (optional)"),
		), "", mv, mut),

		// Agent Management
		tool("loom_create_agent", "Create an agent from configuration.", objectSchema(
			reqProp("config_path", "string", "Path to agent configuration file"),
		), "", mv, mut),
		tool("loom_list_agents", "List all registered agents.", objectSchema(), "", mv, ro),
		tool("loom_get_agent", "Get agent information.", objectSchema(
			reqProp("agent_id", "string", "Agent ID"),
		), "", mv, ro),
		tool("loom_start_agent", "Start a stopped agent.", objectSchema(
			reqProp("agent_id", "string", "Agent ID"),
		), "", mv, mut),
		tool("loom_stop_agent", "Stop a running agent.", objectSchema(
			reqProp("agent_id", "string", "Agent ID"),
		), "", mv, mut),
		tool("loom_delete_agent", "Delete an agent.", objectSchema(
			reqProp("agent_id", "string", "Agent ID"),
		), "", mv, del),
		tool("loom_reload_agent", "Hot-reload agent configuration without stopping.", objectSchema(
			reqProp("agent_id", "string", "Agent ID"),
		), "", mv, mut),

		// Models
		tool("loom_switch_model", "Switch the LLM model for a session.", objectSchema(
			reqProp("model_id", "string", "Model identifier (e.g. claude-3-5-sonnet)"),
			prop("session_id", "string", "Session ID (optional)"),
		), "", mv, mut),
		tool("loom_list_models", "List all available LLM models.", objectSchema(), "", mv, ro),

		// Workflow Orchestration
		tool("loom_execute_workflow", "Execute a multi-agent workflow.", objectSchema(
			reqProp("workflow_name", "string", "Name of the workflow to execute"),
			prop("input", "string", "Input data for the workflow"),
			prop("parameters", "object", "Additional workflow parameters"),
		), "", mv, mut),
		tool("loom_get_workflow_execution", "Get a workflow execution.", objectSchema(
			reqProp("execution_id", "string", "Workflow execution ID"),
		), "", mv, ro),
		tool("loom_list_workflow_executions", "List workflow executions.", objectSchema(
			prop("workflow_name", "string", "Filter by workflow name (optional)"),
			prop("status", "string", "Filter by status (optional)"),
		), "", mv, ro),
		tool("loom_schedule_workflow", "Create a scheduled workflow.", objectSchema(
			reqProp("workflow_name", "string", "Workflow to schedule"),
			reqProp("cron", "string", "Cron expression for scheduling"),
			prop("input", "string", "Input data"),
		), "", mv, mut),
		tool("loom_update_scheduled_workflow", "Update an existing scheduled workflow.", objectSchema(
			reqProp("schedule_id", "string", "Schedule ID to update"),
			prop("cron", "string", "New cron expression"),
			prop("input", "string", "New input data"),
		), "", mv, mut),
		tool("loom_get_scheduled_workflow", "Get a scheduled workflow by ID.", objectSchema(
			reqProp("schedule_id", "string", "Schedule ID"),
		), "", mv, ro),
		tool("loom_list_scheduled_workflows", "List all scheduled workflows.", objectSchema(), "", mv, ro),
		tool("loom_delete_scheduled_workflow", "Delete a scheduled workflow.", objectSchema(
			reqProp("schedule_id", "string", "Schedule ID to delete"),
		), "", mv, del),
		tool("loom_trigger_scheduled_workflow", "Manually trigger a scheduled workflow.", objectSchema(
			reqProp("schedule_id", "string", "Schedule ID to trigger"),
		), "", mv, mut),
		tool("loom_pause_schedule", "Pause a scheduled workflow.", objectSchema(
			reqProp("schedule_id", "string", "Schedule ID to pause"),
		), "", mv, mut),
		tool("loom_resume_schedule", "Resume a paused scheduled workflow.", objectSchema(
			reqProp("schedule_id", "string", "Schedule ID to resume"),
		), "", mv, mut),
		tool("loom_get_schedule_history", "Get execution history for a schedule.", objectSchema(
			reqProp("schedule_id", "string", "Schedule ID"),
		), "", mv, ro),

		// Artifacts
		tool("loom_list_artifacts", "List artifacts with optional filtering.", objectSchema(
			prop("session_id", "string", "Filter by session (optional)"),
			prop("agent_id", "string", "Filter by agent (optional)"),
			prop("artifact_type", "string", "Filter by type (optional)"),
		), "", mv, ro),
		tool("loom_get_artifact", "Get artifact metadata.", objectSchema(
			reqProp("artifact_id", "string", "Artifact ID"),
		), "", mv, ro),
		tool("loom_upload_artifact", "Upload a file to artifact storage.", objectSchema(
			reqProp("name", "string", "Artifact name"),
			reqProp("content", "string", "Artifact content (text or base64)"),
			prop("mime_type", "string", "MIME type (optional)"),
			prop("session_id", "string", "Associated session (optional)"),
		), "", mv, mut),
		tool("loom_delete_artifact", "Delete an artifact.", objectSchema(
			reqProp("artifact_id", "string", "Artifact ID"),
		), "", mv, del),
		tool("loom_search_artifacts", "Search artifacts by content.", objectSchema(
			reqProp("query", "string", "Search query"),
		), "", mv, ro),
		tool("loom_get_artifact_content", "Read artifact file content.", objectSchema(
			reqProp("artifact_id", "string", "Artifact ID"),
		), "", mv, ro),
		tool("loom_get_artifact_stats", "Get artifact storage statistics.", objectSchema(), "", mv, ro),
	}
}

// ============================================================================
// Schema helpers
// ============================================================================

type schemaProperty struct {
	name     string
	typ      string
	desc     string
	required bool
}

func prop(name, typ, desc string) schemaProperty {
	return schemaProperty{name: name, typ: typ, desc: desc, required: false}
}

func reqProp(name, typ, desc string) schemaProperty {
	return schemaProperty{name: name, typ: typ, desc: desc, required: true}
}

func objectSchema(props ...schemaProperty) map[string]interface{} {
	schema := map[string]interface{}{
		"type": "object",
	}

	if len(props) > 0 {
		properties := make(map[string]interface{})
		var required []string

		for _, p := range props {
			properties[p.name] = map[string]interface{}{
				"type":        p.typ,
				"description": p.desc,
			}
			if p.required {
				required = append(required, p.name)
			}
		}

		schema["properties"] = properties
		if len(required) > 0 {
			schema["required"] = required
		}
	}

	return schema
}
