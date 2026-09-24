package agent

import (
	"fmt"
	"strings"

	"github.com/teradata-labs/loom/pkg/skills"
)

// Trigger mode MANUAL means one thing across this package: the user activates
// the skill, the model never does. Three surfaces enforce it, and all three
// read the helpers here so they cannot drift apart:
//
//   - the system prompt's skill menu (Agent.skillMenuPromptSupplement) omits it,
//   - manage_skills list omits it,
//   - manage_skills load refuses it for a model-issued call.
//
// The user's own route in is the slash command, which the harness executes on
// their behalf (Agent.loadSkillFromSlashCommand) — never the model.

// isManualSkill reports whether a skill is the user's to invoke. A nil skill is
// not manual: callers that hold no skill have nothing to withhold.
func isManualSkill(s *skills.Skill) bool {
	return s != nil && s.Trigger.Mode == skills.ActivationManual
}

// manualSkillRefusal is what the model is told when it tries to load a MANUAL
// skill. It names the slash command when the skill declares one, because that
// is the only thing that activates the skill and the model's useful next move
// is to tell the user about it. A skill with no slash command is unreachable
// this turn, and the message says so rather than inventing a command.
func manualSkillRefusal(s *skills.Skill) string {
	if s == nil {
		return "skill not found"
	}
	for _, cmd := range s.Trigger.SlashCommands {
		if cmd = strings.TrimSpace(cmd); cmd != "" {
			return fmt.Sprintf(
				"skill %q is manual: only the user activates it, by sending %s. It was not loaded. Ask the user to run %s if this skill is what they need.",
				s.Name, cmd, cmd)
		}
	}
	return fmt.Sprintf(
		"skill %q is manual: only the user activates it, and it declares no slash command. It was not loaded.",
		s.Name)
}
