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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"go.uber.org/zap"
)

// ManageSkillsTool is the model-facing pull interface for skills: it loads a
// skill body into the conversation and lists the library annotated with the
// skills active for the calling session. Both the load's tool-wiring and the
// list's active annotation read the orchestrator's per-session active set, so
// "which skills are loaded" has a single source and is never re-derived from the
// conversation.
//
// There is no unload action: a load appends to the active set and required-tool
// wiring stays until the session ends.
type ManageSkillsTool struct {
	orch              *skills.Orchestrator
	library           *skills.Library
	permissionChecker *shuttle.PermissionChecker

	// enforceRequiredSkillTools registers the required tools of the session's
	// active skills (definition + per-session advertisement). Invoked right after a load so
	// the just-loaded skill's required tools are wired for this session; their
	// same-turn appearance in the advertised set rides on the loop re-deriving
	// its tool projection before each provider call.
	enforceRequiredSkillTools func(sessionID string)

	// Per-mutation debug carrier, set by the agent after construction. When its
	// switch is on, each load emits a debug log; nil or off is a no-op.
	ctxDebug *contextDebug
}

// NewManageSkillsTool constructs the manage_skills builtin. enforceTools is the
// agent's required-tool enforcement bound to this agent; it may be nil, in which
// case a load activates the skill but does not wire its required tools.
func NewManageSkillsTool(
	orch *skills.Orchestrator,
	library *skills.Library,
	permissionChecker *shuttle.PermissionChecker,
	enforceTools func(sessionID string),
) *ManageSkillsTool {
	return &ManageSkillsTool{
		orch:                      orch,
		library:                   library,
		permissionChecker:         permissionChecker,
		enforceRequiredSkillTools: enforceTools,
	}
}

// Name returns the tool name.
func (t *ManageSkillsTool) Name() string {
	return "manage_skills"
}

// Description returns the tool description for the LLM.
func (t *ManageSkillsTool) Description() string {
	return `Load a skill into the current session, or list the available skills.

Actions:
- list: Return every skill in the library, each annotated with whether it is
  active for this session.
- load: Load a skill by name. The skill's instructions are added to the
  conversation and its required tools become available this turn. Loading a skill
  does not remove any already-loaded skill. High-risk skills require approval
  unless approval is disabled.

Input:
- action: "load" or "list"
- name: skill name (required for "load")`
}

// InputSchema returns the JSON schema for the tool input.
func (t *ManageSkillsTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"action": {
				Type:        "string",
				Description: "The operation to perform: 'load' a skill by name, or 'list' the library.",
				Enum:        []interface{}{"load", "list"},
			},
			"name": {
				Type:        "string",
				Description: "Name of the skill to load. Required when action is 'load'.",
			},
		},
		Required: []string{"action"},
	}
}

// Execute dispatches on the action.
func (t *ManageSkillsTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	sessionID := session.SessionIDFromContext(ctx)

	action, _ := params["action"].(string)
	switch action {
	case "load":
		name, _ := params["name"].(string)
		return t.load(sessionID, name)
	case "list":
		return t.list(sessionID)
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: fmt.Sprintf("action must be 'load' or 'list', got %q", action),
			},
		}, nil
	}
}

// load activates a skill for the session and returns its body plus a structured
// activation marker. High-risk skills are gated on approval.
func (t *ManageSkillsTool) load(sessionID, name string) (*shuttle.Result, error) {
	if name == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: "name is required for action 'load'",
			},
		}, nil
	}

	skill, err := t.library.Load(name)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "not_found",
				Message: fmt.Sprintf("skill %q not found", name),
			},
		}, nil
	}

	// High-risk gate: a HIGH/RESTRICTED skill needs approval when a permission
	// checker is present and not in YOLO mode. A nil checker or YOLO mode
	// proceeds. Blocked loads do not touch the active set.
	if skill.IsHighRisk() && t.permissionChecker != nil && !t.permissionChecker.IsYOLOMode() {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code: "approval_required",
				Message: fmt.Sprintf(
					"skill %q is %s-risk and requires approval; it was not loaded. To bypass: enable YOLO mode (tools.permissions.yolo=true).",
					name, strings.ToUpper(skill.RiskLevel)),
			},
			Metadata: map[string]interface{}{
				"skill":     name,
				"risk":      skill.RiskLevel,
				"activated": false,
			},
		}, nil
	}

	activeBefore := len(t.orch.GetActiveSkills(sessionID))
	active := t.orch.ActivatePinned(sessionID, skill, "manual_load", name, 1.0)

	// Wire the skill's required tools for this session. The loop re-projects the
	// advertised tool set per provider call, so they surface this turn.
	if t.enforceRequiredSkillTools != nil {
		t.enforceRequiredSkillTools(sessionID)
	}

	body := skill.FormatForLLM()

	// Mutation-debug: the skill just loaded into this session's context.
	// No-op unless the context-dump switch is on.
	if t.ctxDebug.on() {
		zap.L().Debug("context mutation: skill load",
			zap.String("session_id", sessionID),
			zap.Int("turn", t.ctxDebug.turn(sessionID)),
			zap.String("name", name),
			zap.Int("body_bytes", len(body)),
			zap.Int("tools_registered", len(skill.Tools.RequiredTools)),
			zap.Int("active_set_delta", len(t.orch.GetActiveSkills(sessionID))-activeBefore))
	}

	// Confirmation is the tool_result payload — short, so the skill's
	// instructional body does NOT ride under the tool_result role.
	confirmation := "Skill loaded: " + name

	// Body goes as a sidecar under the user-instruction slot. The agent's
	// tool-execution loop reads Metadata["text_body"] and, when set, appends a
	// Role="user" Message after every tool_result of the batch has been placed,
	// so tool_use↔tool_result adjacency survives parallel tool calls.
	// This mirrors the Skill-tool shape used by Claude Code (short
	// tool_result + user-role text carrying the SOP).
	// FormatForLLM omits PatternRefs, so surface them here: this is the only
	// place the model receives a reference it can pass to load_pattern.
	var bodySB strings.Builder
	bodySB.WriteString(body)
	if len(skill.PatternRefs) > 0 {
		bodySB.WriteString("\n\nReferenced patterns (load via load_pattern): ")
		bodySB.WriteString(strings.Join(skill.PatternRefs, ", "))
		bodySB.WriteString("\n")
	}

	return &shuttle.Result{
		Success: true,
		Data:    confirmation,
		Metadata: map[string]interface{}{
			"skill":        name,
			"source_path":  name,
			"risk":         skill.RiskLevel,
			"activated_at": active.ActivatedAt.Format(time.RFC3339),
			"text_body":    bodySB.String(),
		},
	}, nil
}

// skillListEntry is one skill in a list() result: its catalog summary plus
// whether it is active for the requesting session.
type skillListEntry struct {
	skills.SkillSummary
	Active bool `json:"active"`
}

// skillListResult is the composite returned by list(): the full library annotated
// with the session's active set.
type skillListResult struct {
	SessionID   string           `json:"session_id"`
	ActiveCount int              `json:"active_count"`
	Skills      []skillListEntry `json:"skills"`
}

// list returns the full library annotated with which skills are active for this
// session, rendered as JSON.
func (t *ManageSkillsTool) list(sessionID string) (*shuttle.Result, error) {
	summaries := t.library.ListAll()

	activeSet := make(map[string]bool)
	for _, as := range t.orch.GetActiveSkills(sessionID) {
		if as != nil && as.Skill != nil {
			activeSet[as.Skill.Name] = true
		}
	}

	entries := make([]skillListEntry, 0, len(summaries))
	activeCount := 0
	for _, s := range summaries {
		isActive := activeSet[s.Name]
		if isActive {
			activeCount++
		}
		entries = append(entries, skillListEntry{SkillSummary: s, Active: isActive})
	}

	composite := skillListResult{
		SessionID:   sessionID,
		ActiveCount: activeCount,
		Skills:      entries,
	}

	rendered, err := json.MarshalIndent(composite, "", "  ")
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "render_failed",
				Message: fmt.Sprintf("failed to render skill list: %v", err),
			},
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data:    string(rendered),
	}, nil
}

// Backend returns the backend type this tool requires. Empty means the tool is
// backend-agnostic.
func (t *ManageSkillsTool) Backend() string {
	return ""
}

// Ensure ManageSkillsTool implements shuttle.Tool.
var _ shuttle.Tool = (*ManageSkillsTool)(nil)
