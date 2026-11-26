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
	"sort"
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

	// emitSkillTasks materializes the loaded skill's task_template onto the
	// agent's task board. Set by the agent after construction; nil is a no-op.
	// Invoked only for a skill that was not already active for this session.
	//
	// The agent's implementation (Agent.emitSkillTasksAsync) returns before the
	// writes happen and reports its own failures, so load neither waits for the
	// board to fill nor fails when it does not.
	emitSkillTasks func(ctx context.Context, sessionID string, skill *skills.Skill)
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
		return t.load(ctx, sessionID, name, loadByModel)
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

// loadOrigin distinguishes who asked for a load. The two differ on one rule:
// a MANUAL skill is the user's to invoke, so the model may not load it while
// the harness — acting on the user's slash command — may.
type loadOrigin int

const (
	// loadByModel is a manage_skills(load) call the model made itself.
	loadByModel loadOrigin = iota
	// loadByUser is a load the harness performs on the user's behalf, i.e. the
	// slash command the user typed (see Agent.loadSkillFromSlashCommand).
	loadByUser
)

// load activates a skill for the session and returns its body plus a structured
// activation marker. High-risk skills are gated on approval. ctx carries the
// values the detached task emit needs (session, and any tenant identity the
// storage layer reads); load itself performs no context-bound I/O.
//
// origin decides whether the MANUAL trigger mode blocks this load: MANUAL means
// "only the user activates this skill", so a model-issued load is refused and
// pointed at the skill's slash command. An already-active MANUAL skill loads
// again freely — the user already activated it this session, so re-reading its
// body is not a way around the rule.
func (t *ManageSkillsTool) load(ctx context.Context, sessionID, name string, origin loadOrigin) (*shuttle.Result, error) {
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

	// One read of the pre-activation set serves three decisions: the MANUAL gate
	// below, the debug delta further down, and the was-it-already-active check
	// that gates task emission.
	beforeSet := t.orch.GetActiveSkills(sessionID)
	activeBefore := len(beforeSet)
	wasActive := false
	for _, as := range beforeSet {
		if as != nil && as.Skill != nil && as.Skill.Name == name {
			wasActive = true
			break
		}
	}

	// MANUAL gate: the skill's author reserved activation for the user, so the
	// model cannot pull it into the conversation. The refusal names the slash
	// command because that is the one thing that does activate it, and the model
	// can relay it to the user. Not an error the model should retry: a MANUAL
	// skill stays out of the menu and out of list(), so reaching here at all
	// means the model guessed the name.
	if origin == loadByModel && isManualSkill(skill) && !wasActive {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "manual_skill",
				Message: manualSkillRefusal(skill),
			},
			Metadata: map[string]interface{}{
				"skill":     name,
				"activated": false,
			},
		}, nil
	}

	activationSource := "manual_load"
	if origin == loadByUser {
		activationSource = "slash_command"
	}
	active := t.orch.ActivatePinned(sessionID, skill, activationSource, name, 1.0)

	// Wire the skill's required tools for this session. The loop re-projects the
	// advertised tool set per provider call, so they surface this turn.
	if t.enforceRequiredSkillTools != nil {
		t.enforceRequiredSkillTools(sessionID)
	}

	// Materialize the skill's task_template, once per (skill, session).
	//
	// The per-name check above is what decides that, and the active-set LENGTH
	// cannot substitute for it: ActivatePinned replaces a same-name activation
	// in place, so re-loading an already-active skill leaves the count
	// unchanged, while loading any different skill raises it. Only the name
	// comparison separates a genuinely new activation from a repeat.
	if !wasActive && t.emitSkillTasks != nil {
		t.emitSkillTasks(ctx, sessionID, skill)
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

// list returns the library annotated with which skills are active for this
// session, rendered as JSON.
//
// The library answers "what skills exist" from two stores and neither is the
// whole truth: ListAll indexes the search paths and the embedded FS, while
// Register — which is how an embedder injects database-backed, marketplace and
// admin-draft skills, and how the cloud builds every session library — writes
// to the skill cache that List reads. Listing from the index alone reported
// none of a cloud session's skills; listing from the cache alone would drop the
// on-disk ones until something loaded them. This reads both and merges by name.
//
// MANUAL skills are omitted: the model cannot load them (see load's gate), so
// listing them would only advertise a name every load call refuses. The one
// exception is a MANUAL skill the user already activated by slash command —
// it is part of this session's state, so the model's picture of what is active
// stays complete.
func (t *ManageSkillsTool) list(sessionID string) (*shuttle.Result, error) {
	activeSet := make(map[string]bool)
	for _, as := range t.orch.GetActiveSkills(sessionID) {
		if as != nil && as.Skill != nil {
			activeSet[as.Skill.Name] = true
		}
	}

	entries := make([]skillListEntry, 0)
	activeCount := 0
	seen := make(map[string]bool)
	add := func(s *skills.Skill) {
		if s == nil || s.Name == "" || seen[s.Name] {
			return
		}
		seen[s.Name] = true
		isActive := activeSet[s.Name]
		if isManualSkill(s) && !isActive {
			return
		}
		if isActive {
			activeCount++
		}
		entries = append(entries, skillListEntry{SkillSummary: s.Summary(), Active: isActive})
	}

	for _, s := range t.library.List() {
		add(s)
	}
	// Load resolves an indexed skill from its source and caches it, so the
	// trigger mode this filter needs is available for index-only entries too.
	for _, summary := range t.library.ListAll() {
		if seen[summary.Name] {
			continue
		}
		if s, err := t.library.Load(summary.Name); err == nil {
			add(s)
		}
	}

	// List walks a map, so its order varies per call. The rendered list is part
	// of the model's context: an unstable order would churn the prompt cache and
	// make two identical sessions read differently.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

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
