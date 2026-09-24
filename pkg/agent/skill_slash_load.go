package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	skillbinding "github.com/teradata-labs/loom/pkg/skills/binding"
)

// loadSkillFromSlashCommand activates the skill a user's leading slash command
// names, before the model sees the turn. It is the user's half of the skill
// contract: the model pulls skills through manage_skills, and the user pushes
// one with /command — the only route that reaches a MANUAL skill.
//
// It writes the same three rows a model-issued load writes, in the same order:
//
//	assistant  tool_calls=[manage_skills{action:load,name}]
//	tool       "Skill loaded: <name>"        (paired by tool_use_id)
//	user       the skill body
//
// That shape is not cosmetic. Restore replay rebuilds a session's active skills
// by looking for exactly that assistant/tool pair (Memory.reFireOnRestore), so
// writing anything else would make a slash-loaded skill vanish on reload while
// its tools stayed advertised. Rendering, folding and the cloud's tool timeline
// read the same rows, so a slash load is indistinguishable downstream from the
// model's own load — only its origin differs.
//
// Best-effort by construction: any miss (no orchestrator, no library, skills
// disabled, unknown command, skill not bound to this agent) leaves the turn
// exactly as it was, with the "/cmd …" text reaching the model as ordinary
// user input. A refused load (high-risk approval) still records the pair so the
// model can tell the user why nothing happened; only the body is withheld.
func (a *Agent) loadSkillFromSlashCommand(ctx context.Context, session *Session, userMessage string) {
	if session == nil || a.skillOrchestrator == nil {
		return
	}
	lib := a.skillOrchestrator.GetLibrary()
	if lib == nil {
		return
	}

	skillsConfig := a.config.SkillsConfig
	if skillsConfig == nil {
		skillsConfig = skills.DefaultSkillsConfig()
	}
	if !skillsConfig.Enabled {
		return
	}

	cmd, _ := skills.ParseSlashCommand(userMessage)
	if cmd == "" {
		return
	}
	skill, ok := lib.FindBySlashCommand(cmd)
	if !ok || skill == nil {
		return
	}

	// Bound-skill check, the same gate the menu applies: a library can hold more
	// than this agent is configured to use, and a slash command must not reach
	// past that binding.
	if !a.skillBoundToAgent(lib, skillsConfig, skill.Name) {
		return
	}

	tool := a.newManageSkillsTool()
	if tool == nil {
		return
	}
	result, err := tool.load(ctx, session.ID, skill.Name, loadByUser)
	if err != nil || result == nil {
		zap.L().Warn("slash-command skill load failed",
			zap.String("session_id", session.ID),
			zap.String("command", cmd),
			zap.String("skill", skill.Name),
			zap.Error(err))
		return
	}

	callID := "slash_" + uuid.New().String()
	a.appendMessage(ctx, session, Message{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID:   callID,
			Name: "manage_skills",
			Input: map[string]interface{}{
				"action": "load",
				"name":   skill.Name,
			},
		}},
		AgentID:   a.GetID(),
		Timestamp: time.Now(),
	}, false)

	a.appendMessage(ctx, session, Message{
		Role:       "tool",
		Content:    slashLoadResultText(result, skill.Name),
		ToolUseID:  callID,
		ToolResult: result,
		AgentID:    a.GetID(),
		Timestamp:  time.Now(),
	}, false)

	if !result.Success {
		zap.L().Info("slash-command skill load refused",
			zap.String("session_id", session.ID),
			zap.String("skill", skill.Name),
			zap.String("reason", result.Error.Code))
		return
	}

	// The body rides under the user-instruction slot, mirroring the sidecar the
	// tool loop appends after a model-issued load (see agent.go's text_body
	// handling) — instructions are the user's word to the model, not tool data.
	if body, ok := result.Metadata["text_body"].(string); ok && body != "" {
		a.appendMessage(ctx, session, Message{
			Role:      "user",
			Content:   body,
			AgentID:   a.GetID(),
			Timestamp: time.Now(),
		}, false)
	}
}

// skillBoundToAgent reports whether name resolves through this agent's skill
// bindings. Resolution failure is treated as unbound: a binding set we cannot
// read is not licence to activate anything.
func (a *Agent) skillBoundToAgent(lib *skills.Library, cfg *skills.SkillsConfig, name string) bool {
	resolved, err := skillbinding.NewResolver(lib).Resolve(cfg)
	if err != nil {
		return false
	}
	for _, rb := range resolved {
		if rb.Skill != nil && rb.Skill.Name == name {
			return true
		}
	}
	return false
}

// slashLoadResultText is the tool row's content. On success it is the tool's own
// confirmation verbatim ("Skill loaded: <name>"), which restore replay matches
// on; on refusal it is the reason, so the conversation carries why the command
// did nothing.
func slashLoadResultText(result *shuttle.Result, name string) string {
	if result.Success {
		if s, ok := result.Data.(string); ok && s != "" {
			return s
		}
		return "Skill loaded: " + name
	}
	if result.Error != nil && result.Error.Message != "" {
		return result.Error.Message
	}
	return fmt.Sprintf("skill %q was not loaded", name)
}
