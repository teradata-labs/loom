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
	"fmt"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	skilltasks "github.com/teradata-labs/loom/pkg/skills/tasks"
)

// skillActiveSafetyCap is the hard ceiling on a session's active-skill set
// (O-SKL-3, FR-030). It is a safety limit, not a target to eagerly fill:
// past this many active skills a further manage_skills(load) is rejected
// with an explicit error rather than silently evicting an existing skill.
// The active set only shrinks via explicit unload or session end.
const skillActiveSafetyCap = 20

// ManageSkillsTool provides list/load/unload over the skill orchestrator and
// library. It is the sole activation entry point for skills (Seam 3): the
// per-turn discovery block only ever surfaces a candidate menu, never
// activates. A successful load returns a charter-classed (D-3) tool result
// that carries the skill's SourcePath (Seam 4); a high-risk skill returns an
// explicit gate result instead of activating (Seam 2); a load past the
// safety cap returns an explicit error instead of silently evicting another
// skill (Seam 5).
type ManageSkillsTool struct {
	orchestrator      *skills.Orchestrator
	taskEmitter       *skilltasks.Emitter
	taskBoardConfig   *loomv1.TaskBoardConfig
	config            *Config
	llm               LLMProvider
	agentID           string
	permissionChecker *shuttle.PermissionChecker

	// memory is set by the agent via WithMemory after construction. Used
	// by executeUnload to remove the load message from L1 so walk-L1
	// (ActiveSkillNames) sees the unloaded skill as inactive. Without this
	// wiring, orchestrator says "not active" but L1 still carries the
	// load metadata → ROM catalog filter mistakenly hides the unloaded
	// skill. Optional: when nil, unload only touches the orchestrator
	// (legacy behavior; L1 self-heals only via fold).
	memory *Memory
}

// NewManageSkillsTool creates the manage_skills builtin.
func NewManageSkillsTool(
	orchestrator *skills.Orchestrator,
	taskEmitter *skilltasks.Emitter,
	taskBoardConfig *loomv1.TaskBoardConfig,
	config *Config,
	llm LLMProvider,
	agentID string,
	permissionChecker *shuttle.PermissionChecker,
) *ManageSkillsTool {
	return &ManageSkillsTool{
		orchestrator:      orchestrator,
		taskEmitter:       taskEmitter,
		taskBoardConfig:   taskBoardConfig,
		config:            config,
		llm:               llm,
		agentID:           agentID,
		permissionChecker: permissionChecker,
	}
}

// WithMemory wires the agent's memory subsystem so executeUnload can
// remove the load message from L1 (keeping walk-L1 aligned with the
// orchestrator's active-set). See the memory field's comment.
func (t *ManageSkillsTool) WithMemory(m *Memory) *ManageSkillsTool {
	t.memory = m
	return t
}

// Name returns the tool name.
func (t *ManageSkillsTool) Name() string { return "manage_skills" }

// Backend returns the backend type this tool requires (empty = agnostic).
func (t *ManageSkillsTool) Backend() string { return "" }

// Description returns the tool description for the LLM.
func (t *ManageSkillsTool) Description() string {
	return `Manage the session's active skill set: list available skills, load one to activate it, or unload one to deactivate it.

Three actions available:
1. list - List all available skills, with which ones are active this session
2. load - Activate a skill by name so its instructions and tool preferences apply
3. unload - Deactivate a skill by name

A load may come back as a gate result (the skill is high-risk and needs approval before it can activate) or as a capacity error (the session already has too many active skills — unload one first). Neither is a failure to retry blindly; read the result and act on it (ask the user for approval, or free capacity with unload).

Loading a skill does not happen automatically — discovery only ever surfaces a candidate menu. Use this tool to actually activate one.`
}

// InputSchema returns the JSON schema for tool parameters.
func (t *ManageSkillsTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"action": {
				Type:        "string",
				Description: "Action to perform: 'list', 'load', or 'unload'",
			},
			"name": {
				Type:        "string",
				Description: "(load/unload only) Skill name",
			},
		},
		Required: []string{"action"},
	}
}

// Execute routes to the requested action.
func (t *ManageSkillsTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	sessionID := session.SessionIDFromContext(ctx)
	if sessionID == "" {
		return &shuttle.Result{
			Success: false,
			Error:   &shuttle.Error{Code: "MISSING_SESSION_ID", Message: "session ID not found in context"},
		}, nil
	}

	action, _ := input["action"].(string)
	name, _ := input["name"].(string)

	switch action {
	case "list":
		return t.executeList(sessionID)
	case "load":
		return t.executeLoad(ctx, sessionID, name)
	case "unload":
		return t.executeUnload(sessionID, name)
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_ACTION",
				Message: fmt.Sprintf("unknown action %q; must be list, load, or unload", action),
			},
		}, nil
	}
}

// executeList returns every skill the library knows about, annotated with
// whether it is active for the requesting session.
func (t *ManageSkillsTool) executeList(sessionID string) (*shuttle.Result, error) {
	summaries := t.orchestrator.GetLibrary().ListAll()
	activeNames := skillNameSet(t.orchestrator.GetActiveSkills(sessionID))

	items := make([]map[string]interface{}, 0, len(summaries))
	for _, s := range summaries {
		items = append(items, map[string]interface{}{
			"name":        s.Name,
			"title":       s.Title,
			"description": s.Description,
			"domain":      s.Domain,
			"version":     s.Version,
			"commands":    s.Commands,
			"active":      activeNames[s.Name],
		})
	}

	data := map[string]interface{}{
		"action":       "list",
		"count":        len(items),
		"active_count": len(activeNames),
		"skills":       items,
	}
	return jsonResult(data)
}

// executeLoad activates a skill by name. High-risk skills return an
// explicit gate result instead of activating (Seam 2); a load that would
// push the active set past skillActiveSafetyCap returns an explicit error
// instead of evicting another skill (Seam 5). A genuinely new activation
// (not a re-load of an already-active skill) emits the skill's tasks, the
// same one-shot-per-activation step the deleted per-turn Phase D used to
// perform for discovery-driven activations.
func (t *ManageSkillsTool) executeLoad(ctx context.Context, sessionID, name string) (*shuttle.Result, error) {
	if name == "" {
		return &shuttle.Result{
			Success: false,
			Error:   &shuttle.Error{Code: "INVALID_PARAMETER", Message: "name is required for load"},
		}, nil
	}

	skill, err := t.orchestrator.GetLibrary().Load(name)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error:   &shuttle.Error{Code: "SKILL_NOT_FOUND", Message: fmt.Sprintf("skill not found: %s", name)},
		}, nil
	}

	if skill.IsHighRisk() && t.permissionChecker != nil && !t.permissionChecker.IsYOLOMode() {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "HIGH_RISK_APPROVAL_REQUIRED",
				Message:    fmt.Sprintf("skill %q is risk level %s and requires explicit human approval before activation", name, skill.RiskLevel),
				Suggestion: "Ask the user to approve loading this skill before retrying manage_skills(action=\"load\").",
				Details: map[string]interface{}{
					"skill":      name,
					"risk_level": skill.RiskLevel,
				},
			},
		}, nil
	}

	wasActive := skillNameSet(t.orchestrator.GetActiveSkills(sessionID))[name]
	if !wasActive {
		// Cap resolution: honor operator-set max_concurrent_skills from
		// agent config when >0, else fall back to the runaway-loop backstop.
		// The YAML field previously wired only the discovery menu bound and
		// left the active-set uncapped by operator intent, silently
		// repurposing an existing config knob.
		cap := skillActiveSafetyCap
		if t.config != nil && t.config.SkillsConfig != nil && t.config.SkillsConfig.MaxConcurrentSkills > 0 {
			cap = t.config.SkillsConfig.MaxConcurrentSkills
		}
		activeCount := len(t.orchestrator.GetActiveSkills(sessionID))
		if activeCount >= cap {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:       "ACTIVE_SKILL_CAP_EXCEEDED",
					Message:    fmt.Sprintf("session already has %d active skills (safety cap %d); no skill was evicted", activeCount, cap),
					Suggestion: "Call manage_skills(action=\"unload\", name=\"<skill>\") to free capacity before loading another.",
				},
			}, nil
		}
	}

	active := t.orchestrator.ActivateSkill(sessionID, skill, "manual", name, 1.0)

	if !wasActive && t.taskEmitter != nil {
		t.emitActivationTasks(ctx, sessionID, skill)
	}

	// v5 LLD (Part D, lines 140/106): the load result carries the skill's
	// BODY into context — not a receipt. Exempt from ref-wrapping at
	// admission (executor.go). Classified as narrative (see toolResultClass)
	// so fold's LLM compressor summarizes the body into residue when
	// pressure hits red — the pre-fix charter classification pinned skill
	// bodies forever and accumulated across load/unload cycles the LLM
	// effectively never triggers.
	//
	// Data is the RAW skill body (markdown, verbatim). formatToolResult renders
	// Data via fmt.Sprintf("%v", ...) — a string prints verbatim, matching the
	// Claude Agent SDK's Skill() tool_result shape. A prior attempt wrapped the
	// body inside a map with an "instructions" field, which %v-dumped as Go
	// map syntax ("map[action:load activated_at:... instructions:...]"), losing
	// the checklist structure and reading as a status blob rather than the plan.
	//
	// Operational metadata (action/skill/source_path/activated_at/etc.) lives
	// in Result.Metadata — visible to programmatic consumers and observability,
	// invisible to the model.
	return &shuttle.Result{
		Success: true,
		Data:    skill.FormatForLLM(),
		Metadata: map[string]interface{}{
			"action":         "load",
			"status":         "activated",
			"skill":          skill.Name,
			"title":          skill.Title,
			"source_path":    skill.SourcePath,
			"risk_level":     skill.RiskLevel,
			"trigger_type":   active.TriggerType,
			"activated_at":   active.ActivatedAt,
			"already_active": wasActive,
			"active_count":   len(t.orchestrator.GetActiveSkills(sessionID)),
		},
	}, nil
}

// emitActivationTasks materializes tasks for a freshly-activated skill,
// mirroring the request shape the deleted per-turn Phase D block used.
func (t *ManageSkillsTool) emitActivationTasks(ctx context.Context, sessionID string, skill *skills.Skill) {
	skillsConfig := t.config.SkillsConfig
	if skillsConfig == nil {
		skillsConfig = skills.DefaultSkillsConfig()
	}

	boardID := skillsConfig.SkillTaskBoardID
	if boardID == "" && t.taskBoardConfig != nil {
		boardID = t.taskBoardConfig.DefaultBoardId
	}

	_, err := t.taskEmitter.EmitForActivation(ctx, skilltasks.EmitRequest{
		Skill:             skill,
		SessionID:         sessionID,
		AgentID:           t.agentID,
		BoardID:           boardID,
		LLM:               t.llm,
		AgentTasksEnabled: skillsConfig.EffectiveTasksEnabled(),
	})
	if err != nil {
		zap.L().Warn("skill task emission failed",
			zap.String("skill", skill.Name),
			zap.Error(err))
	}
}

// executeUnload deactivates a skill by name. Unloading a skill that is not
// active is a no-op success (idempotent), not an error.
func (t *ManageSkillsTool) executeUnload(sessionID, name string) (*shuttle.Result, error) {
	if name == "" {
		return &shuttle.Result{
			Success: false,
			Error:   &shuttle.Error{Code: "INVALID_PARAMETER", Message: "name is required for unload"},
		}, nil
	}

	wasActive := skillNameSet(t.orchestrator.GetActiveSkills(sessionID))[name]
	t.orchestrator.DeactivateSkill(sessionID, name)

	// Remove the load message from L1 too, keeping walk-L1 aligned with
	// the orchestrator's active-set. Without this, ActiveSkillNames still
	// sees load metadata → ROM catalog filter mistakenly hides the just-
	// unloaded skill on the next turn (and the LLM has no way to reload it
	// via the catalog UI). See ManageSkillsTool.memory doc.
	removedFromL1 := false
	if wasActive && t.memory != nil {
		if session, ok := t.memory.GetSession(sessionID); ok && session.SegmentedMem != nil {
			if sm, ok := session.SegmentedMem.(*SegmentedMemory); ok {
				removedFromL1 = sm.RemoveSkillLoadMessage(name)
			}
		}
	}

	data := map[string]interface{}{
		"action":          "unload",
		"status":          "deactivated",
		"skill":           name,
		"was_active":      wasActive,
		"removed_from_l1": removedFromL1,
		"active_count":    len(t.orchestrator.GetActiveSkills(sessionID)),
	}
	return jsonResult(data)
}

// Ensure ManageSkillsTool implements shuttle.Tool.
var _ shuttle.Tool = (*ManageSkillsTool)(nil)
