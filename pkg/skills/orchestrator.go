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

package skills

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Orchestrator is the activation engine for skills. It evaluates user messages,
// matches them to skills via slash commands, keywords, or always-on rules,
// and manages active skill lifecycles within sessions.
type Orchestrator struct {
	mu             sync.RWMutex
	library        *Library
	tracer         observability.Tracer
	logger         *zap.Logger
	activeSessions map[string][]*ActiveSkill // sessionID -> active skills
}

// OrchestratorOption configures an Orchestrator during construction.
type OrchestratorOption func(*Orchestrator)

// WithOrchestratorTracer sets the observability tracer for the orchestrator.
func WithOrchestratorTracer(t observability.Tracer) OrchestratorOption {
	return func(o *Orchestrator) {
		if t != nil {
			o.tracer = t
		}
	}
}

// WithOrchestratorLogger sets the zap logger used to surface skill
// activation/deactivation events. When unset, the orchestrator
// uses zap.NewNop() so existing callers see no output change.
func WithOrchestratorLogger(l *zap.Logger) OrchestratorOption {
	return func(o *Orchestrator) {
		if l != nil {
			o.logger = l
		}
	}
}

// NewOrchestrator creates a new skill orchestrator backed by the given library.
func NewOrchestrator(library *Library, opts ...OrchestratorOption) *Orchestrator {
	o := &Orchestrator{
		library:        library,
		tracer:         observability.NewNoOpTracer(),
		logger:         zap.NewNop(),
		activeSessions: make(map[string][]*ActiveSkill),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// SkillsConfig controls skill matching behavior for a session.
type SkillsConfig struct {
	Enabled bool
	// Deprecated: prefer Bindings. Resolver synthesizes EAGER-mode bindings
	// from this list when Bindings is empty.
	EnabledSkills []string
	// Deprecated: prefer Bindings. Resolver excludes these names when
	// synthesizing the implicit binding set.
	DisabledSkills    []string
	MinAutoConfidence float64
	// Deprecated: no longer limits how many skills a session can have
	// loaded. Still read as the number of skill suggestions shown per turn.
	MaxConcurrentSkills int
	// LoadHardCap overrides the built-in limit (skillActiveSafetyCap) on
	// how many different skills one session can load; loading an
	// already-loaded skill does not count. Unset means the built-in limit.
	// Test-only knob — not read from YAML or proto.
	LoadHardCap          int
	SkillsDir            string
	ContextBudgetPercent int

	// Bindings declares the skills attached to this agent. Empty falls back
	// to the legacy EnabledSkills/DisabledSkills filter pair via the resolver
	// shim. When non-empty, Bindings takes full precedence.
	Bindings []SkillBinding

	// RouterEnabled gates the hierarchical PageIndex-style discovery path.
	// nil mirrors proto3 optional default-true; pointer so callers can
	// distinguish "not specified" from "explicitly false".
	RouterEnabled *bool
	// RouterMaxCandidates caps candidates returned per router walk (default 5).
	RouterMaxCandidates int
	// RouterCacheTTLSeconds is the per-session decision cache TTL (default 300).
	RouterCacheTTLSeconds int
	// RouterModelOverride names a specific LLM provider for routing decisions.
	// Empty falls back to AgentConfig.classifier_llm.
	RouterModelOverride string

	// SkillTaskBoardID names the task board for skill-emitted tasks.
	// Empty reuses the agent's primary board.
	SkillTaskBoardID string
	// TasksEnabled is the agent-level master switch for skill task emission.
	// nil mirrors proto3 optional default-true. Per-skill EmitTasks overrides
	// this for individual skills.
	TasksEnabled *bool

	// Hygiene controls end-of-turn task-board hygiene enforcement for
	// skill-emitted tasks. nil falls back to defaults (enabled, REQUIRE_FIX
	// policy, max_retries=2). The agent layer constructs the auditor when
	// both skillOrchestrator and taskManager are present.
	Hygiene *loomv1.HygieneConfig
}

// DefaultSkillsConfig returns a SkillsConfig with sensible defaults.
//
// MaxConcurrentSkills and RouterMaxCandidates are aligned at 3 so the
// router's per-leaf decisions reach the orchestrator without being
// silently trimmed. See pkg/skills/index/router.go's maxCandidates
// comment for the rationale.
func DefaultSkillsConfig() *SkillsConfig {
	return &SkillsConfig{
		Enabled:               true,
		MaxConcurrentSkills:   3,
		MinAutoConfidence:     0.7,
		ContextBudgetPercent:  5,
		RouterMaxCandidates:   3,
		RouterCacheTTLSeconds: 300,
	}
}

// EffectiveRouterEnabled resolves the router-enabled decision, applying
// default-true semantics when RouterEnabled is unset.
func (c *SkillsConfig) EffectiveRouterEnabled() bool {
	if c == nil || c.RouterEnabled == nil {
		return true
	}
	return *c.RouterEnabled
}

// EffectiveTasksEnabled resolves the task-emission master switch, applying
// default-true semantics when TasksEnabled is unset.
func (c *SkillsConfig) EffectiveTasksEnabled() bool {
	if c == nil || c.TasksEnabled == nil {
		return true
	}
	return *c.TasksEnabled
}

// MatchSkills evaluates the user message and returns matching skills.
// It checks slash commands first, then auto-detection for HYBRID/AUTO skills,
// and finally ALWAYS skills. Results are filtered by config and sorted by confidence.
func (o *Orchestrator) MatchSkills(sessionID, userMsg string, config *SkillsConfig) ([]*MatchResult, error) {
	if config == nil {
		config = DefaultSkillsConfig()
	}
	if !config.Enabled {
		return nil, nil
	}

	_, span := o.tracer.StartSpan(context.Background(), "skills.orchestrator.match_skills")
	defer o.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("session.id", sessionID)
		span.SetAttribute("message.length", fmt.Sprintf("%d", len(userMsg)))
	}

	maxConcurrent := config.MaxConcurrentSkills
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}

	minConfidence := config.MinAutoConfidence
	if minConfidence <= 0 {
		minConfidence = 0.7
	}

	// Prime the library's cache before any of the find* calls below.
	// FindBySlashCommand and FindByKeywords both iterate l.skillCache
	// without triggering indexing, so an unprimed library returns no
	// matches even when the requested skill exists on disk. List() walks
	// the search paths once and populates the cache for the rest of the
	// process. Subsequent calls are cache hits.
	_ = o.library.List()

	var results []*MatchResult

	// 1. Check for slash command (highest priority).
	if cmd, rest := ParseSlashCommand(userMsg); cmd != "" {
		if skill, ok := o.library.FindBySlashCommand(cmd); ok {
			if o.isSkillAllowed(skill.Name, config) {
				triggerVal := cmd
				if rest != "" {
					triggerVal = cmd + " " + rest
				}
				results = append(results, &MatchResult{
					Skill:        skill,
					Confidence:   1.0,
					TriggerType:  "slash_command",
					TriggerValue: triggerVal,
				})
			}
		}
	}

	// 2. Auto-detection for AUTO and HYBRID skills.
	if len(results) == 0 || len(results) < maxConcurrent {
		scored := o.library.FindByKeywords(userMsg)
		for _, ss := range scored {
			if len(results) >= maxConcurrent {
				break
			}
			mode := ss.Skill.Trigger.Mode
			if mode != ActivationAuto && mode != ActivationHybrid {
				continue
			}
			if ss.Score < minConfidence {
				continue
			}
			if !o.isSkillAllowed(ss.Skill.Name, config) {
				continue
			}
			// Avoid duplicates from slash command match.
			if containsSkill(results, ss.Skill.Name) {
				continue
			}
			results = append(results, &MatchResult{
				Skill:        ss.Skill,
				Confidence:   ss.Score,
				TriggerType:  "keyword",
				TriggerValue: userMsg,
			})
		}
	}

	// 3. ALWAYS skills: add if not already active and under the limit.
	o.mu.RLock()
	activeNames := activeSkillNames(o.activeSessions[sessionID])
	o.mu.RUnlock()

	for _, skill := range o.library.List() {
		if len(results) >= maxConcurrent {
			break
		}
		if skill.Trigger.Mode != ActivationAlways {
			continue
		}
		if !o.isSkillAllowed(skill.Name, config) {
			continue
		}
		// Skip if already active or already matched.
		if activeNames[skill.Name] || containsSkill(results, skill.Name) {
			continue
		}
		results = append(results, &MatchResult{
			Skill:        skill,
			Confidence:   1.0,
			TriggerType:  "always",
			TriggerValue: "",
		})
	}

	// Sort by confidence descending.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Confidence > results[j].Confidence
	})

	if span != nil {
		span.SetAttribute("match.count", fmt.Sprintf("%d", len(results)))
	}

	o.tracer.RecordMetric("skills.orchestrator.match_skills", 1.0, map[string]string{
		"session":     sessionID,
		"match_count": fmt.Sprintf("%d", len(results)),
	})

	return results, nil
}

// ActivateSkill activates a skill for a session, appending it to the active
// set (or replacing the existing entry when the skill is already active).
// There is no implicit eviction: the active set only shrinks via an explicit
// DeactivateSkill call or session end (O-SKL-3). Callers that need a ceiling
// on the active set — e.g. the manage_skills(load) builtin's safety-cap
// check — must enforce it themselves before calling ActivateSkill; this
// method always honors the request.
func (o *Orchestrator) ActivateSkill(sessionID string, skill *Skill, triggerType, triggerValue string, confidence float64) *ActiveSkill {
	_, span := o.tracer.StartSpan(context.Background(), "skills.orchestrator.activate_skill")
	defer o.tracer.EndSpan(span)

	active := &ActiveSkill{
		Skill:        skill,
		TriggerType:  triggerType,
		TriggerValue: triggerValue,
		Confidence:   confidence,
		ActivatedAt:  time.Now(),
		SessionID:    sessionID,
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	sessions := o.activeSessions[sessionID]

	// Check if this skill is already active -- replace it.
	for i, existing := range sessions {
		if existing.Skill.Name == skill.Name {
			sessions[i] = active
			o.activeSessions[sessionID] = sessions
			if span != nil {
				span.SetAttribute("skill.name", skill.Name)
				span.SetAttribute("action", "replaced")
			}
			o.logger.Info("skill replaced",
				zap.String("skill", skill.Name),
				zap.String("session", sessionID),
				zap.String("trigger", triggerType),
				zap.String("trigger_value", triggerValue),
				zap.Float64("confidence", confidence),
				zap.Int("active_count", len(sessions)),
			)
			return active
		}
	}

	sessions = append(sessions, active)
	o.activeSessions[sessionID] = sessions

	if span != nil {
		span.SetAttribute("skill.name", skill.Name)
		span.SetAttribute("session.id", sessionID)
		span.SetAttribute("active.count", fmt.Sprintf("%d", len(sessions)))
		span.SetAttribute("action", "activated")
	}

	o.tracer.RecordMetric("skills.orchestrator.activate_skill", 1.0, map[string]string{
		"skill":   skill.Name,
		"session": sessionID,
		"trigger": triggerType,
	})

	o.logger.Info("skill activated",
		zap.String("skill", skill.Name),
		zap.String("session", sessionID),
		zap.String("trigger", triggerType),
		zap.String("trigger_value", triggerValue),
		zap.Float64("confidence", confidence),
		zap.Int("active_count", len(sessions)),
		zap.Bool("sticky", skill.Sticky),
	)

	return active
}

// DeactivateSkill removes a skill from a session.
func (o *Orchestrator) DeactivateSkill(sessionID, skillName string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	sessions := o.activeSessions[sessionID]
	for i, active := range sessions {
		if active.Skill.Name == skillName {
			activeFor := time.Since(active.ActivatedAt)
			// Remove by shifting.
			o.activeSessions[sessionID] = append(sessions[:i], sessions[i+1:]...)
			o.tracer.RecordMetric("skills.orchestrator.deactivate_skill", 1.0, map[string]string{
				"skill":   skillName,
				"session": sessionID,
			})
			o.logger.Info("skill deactivated",
				zap.String("skill", skillName),
				zap.String("session", sessionID),
				zap.Duration("active_for", activeFor),
				zap.Int("active_count", len(o.activeSessions[sessionID])),
			)
			return
		}
	}
}

// GetActiveSkills returns all active skills for a session.
func (o *Orchestrator) GetActiveSkills(sessionID string) []*ActiveSkill {
	o.mu.RLock()
	defer o.mu.RUnlock()

	src := o.activeSessions[sessionID]
	if len(src) == 0 {
		return nil
	}
	// Return a copy to avoid races on the caller side.
	out := make([]*ActiveSkill, len(src))
	copy(out, src)
	return out
}

// CleanupSession removes all state for a session.
func (o *Orchestrator) CleanupSession(sessionID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.activeSessions, sessionID)
}

// GetLibrary returns the underlying skill library.
func (o *Orchestrator) GetLibrary() *Library {
	return o.library
}

// ParseSlashCommand extracts the slash command from a message.
// Returns command and remaining message, or empty strings if not a slash command.
// Example: "/review this code" -> ("/review", "this code")
func ParseSlashCommand(msg string) (cmd string, rest string) {
	msg = strings.TrimSpace(msg)
	if len(msg) == 0 || msg[0] != '/' {
		return "", ""
	}

	// Find first unicode space
	i := strings.IndexFunc(msg, unicode.IsSpace)
	if i == -1 {
		cmd = strings.ToLower(msg)
	} else {
		cmd = strings.ToLower(msg[:i])
		rest = strings.TrimSpace(msg[i:])
	}

	// Rejects just "/" or instances where a space immediately followed the slash (e.g. "/ help")
	if len(cmd) <= 1 {
		return "", ""
	}
	return cmd, rest
}

// isSkillAllowed checks whether a skill passes the enabled/disabled filter.
func (o *Orchestrator) isSkillAllowed(name string, config *SkillsConfig) bool {
	// If EnabledSkills is set, only those skills are allowed.
	if len(config.EnabledSkills) > 0 {
		for _, e := range config.EnabledSkills {
			if e == name {
				return true
			}
		}
		return false
	}
	// Otherwise check disabled list.
	for _, d := range config.DisabledSkills {
		if d == name {
			return false
		}
	}
	return true
}

// containsSkill checks if results already include a skill by name.
func containsSkill(results []*MatchResult, name string) bool {
	for _, r := range results {
		if r.Skill.Name == name {
			return true
		}
	}
	return false
}

// activeSkillNames returns a set of skill names currently active.
func activeSkillNames(active []*ActiveSkill) map[string]bool {
	names := make(map[string]bool, len(active))
	for _, a := range active {
		names[a.Skill.Name] = true
	}
	return names
}
