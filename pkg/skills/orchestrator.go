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

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/observability"
)

// StickinessChecker is consulted during eviction to decide whether an
// active skill should be treated as sticky for the current activation
// attempt, regardless of its Skill.Sticky flag. The agent layer typically
// installs a checker that returns true when the skill has open tasks on
// the kanban board, so eviction never abandons in-flight work.
type StickinessChecker func(skillName, sessionID string) bool

// Orchestrator is the activation engine for skills. It evaluates user messages,
// matches them to skills via slash commands, keywords, or always-on rules,
// and manages active skill lifecycles within sessions.
type Orchestrator struct {
	mu             sync.RWMutex
	library        *Library
	tracer         observability.Tracer
	logger         *zap.Logger
	activeSessions map[string][]*ActiveSkill // sessionID -> active skills

	// maxConcurrentSkills caps the active set during eviction. <=0 means
	// "use the legacy default of 3". Set via WithMaxConcurrentSkills, or
	// at runtime via SetMaxConcurrentSkills (the agent layer pulls it
	// from SkillsConfig once known).
	maxConcurrentSkills int
	// stickinessChecker (optional) is consulted during eviction. nil means
	// only Skill.Sticky is honored, preserving v1.2.0 behavior.
	stickinessChecker StickinessChecker
	// onSkillEviction (optional) is called after a skill is evicted from
	// the active set. Used by the agent layer to boost graph memory salience
	// for entities related to the evicted skill.
	onSkillEviction func(sessionID string, skill *Skill, activeFor time.Duration)
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
// activation/deactivation/eviction events. When unset, the orchestrator
// uses zap.NewNop() so existing callers see no output change.
func WithOrchestratorLogger(l *zap.Logger) OrchestratorOption {
	return func(o *Orchestrator) {
		if l != nil {
			o.logger = l
		}
	}
}

// WithMaxConcurrentSkills caps the active-skill set this orchestrator
// allows per session. The eviction routine consults this; 0 falls back
// to the legacy default of 3.
func WithMaxConcurrentSkills(n int) OrchestratorOption {
	return func(o *Orchestrator) {
		if n > 0 {
			o.maxConcurrentSkills = n
		}
	}
}

// WithStickinessChecker installs a callback that the eviction routine
// consults to decide whether a candidate-for-eviction is sticky. Used by
// the agent layer to keep skills active while they have open tasks.
func WithStickinessChecker(checker StickinessChecker) OrchestratorOption {
	return func(o *Orchestrator) {
		o.stickinessChecker = checker
	}
}

// SetMaxConcurrentSkills adjusts the eviction cap at runtime. Safe for
// concurrent use with ActivateSkill.
func (o *Orchestrator) SetMaxConcurrentSkills(n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.maxConcurrentSkills = n
}

// SetStickinessChecker installs a stickiness checker at runtime. Safe for
// concurrent use with ActivateSkill.
func (o *Orchestrator) SetStickinessChecker(checker StickinessChecker) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stickinessChecker = checker
}

// WithOnSkillEviction installs a callback that fires after a skill is evicted
// from the active set. The callback receives the session ID, the evicted skill,
// and how long the skill was active. Used by the agent layer to boost graph
// memory salience for entities related to the evicted skill.
func WithOnSkillEviction(fn func(sessionID string, skill *Skill, activeFor time.Duration)) OrchestratorOption {
	return func(o *Orchestrator) {
		o.onSkillEviction = fn
	}
}

// SetOnSkillEviction installs an eviction callback at runtime. Safe for
// concurrent use with ActivateSkill.
func (o *Orchestrator) SetOnSkillEviction(fn func(sessionID string, skill *Skill, activeFor time.Duration)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.onSkillEviction = fn
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
	DisabledSkills       []string
	MinAutoConfidence    float64
	MaxConcurrentSkills  int
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
}

// DefaultSkillsConfig returns a SkillsConfig with sensible defaults.
func DefaultSkillsConfig() *SkillsConfig {
	return &SkillsConfig{
		Enabled:               true,
		MaxConcurrentSkills:   3,
		MinAutoConfidence:     0.7,
		ContextBudgetPercent:  5,
		RouterMaxCandidates:   5,
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
				results = append(results, &MatchResult{
					Skill:        skill,
					Confidence:   1.0,
					TriggerType:  "slash_command",
					TriggerValue: cmd + " " + rest,
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

// ActivateSkill activates a skill for a session. If the session already has
// MaxConcurrentSkills active, the lowest-confidence non-sticky skill is
// evicted. Sticky-ness is determined by:
//  1. Skill.Sticky == true (explicit author intent), OR
//  2. The configured StickinessChecker returning true for the skill —
//     used by the agent layer to keep skills active while they have open
//     tasks on the board.
//
// When every active skill is sticky, the cap is allowed to overflow for
// this turn rather than evicting load-bearing skills out from under
// in-flight work. This matches the design's "sticky-while-open-tasks"
// guarantee from the skills overhaul.
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

	maxConcurrent := o.maxConcurrentSkills
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	if len(sessions) > maxConcurrent {
		// Find lowest-confidence skill among the evictable (non-sticky) set.
		// Skip the just-appended active record (last index): we never
		// evict the skill we're about to activate.
		minIdx := -1
		for i := 0; i < len(sessions)-1; i++ {
			as := sessions[i]
			if as == nil || as.Skill == nil {
				continue
			}
			if as.Skill.Sticky {
				continue
			}
			if o.stickinessChecker != nil && o.stickinessChecker(as.Skill.Name, sessionID) {
				continue
			}
			if minIdx == -1 || sessions[i].Confidence < sessions[minIdx].Confidence {
				minIdx = i
			}
		}
		if minIdx >= 0 {
			// Capture evicted skill metadata before removal.
			evicted := sessions[minIdx]
			// Remove it (swap with last, truncate).
			sessions[minIdx] = sessions[len(sessions)-1]
			sessions = sessions[:len(sessions)-1]
			if span != nil {
				span.SetAttribute("eviction", "evicted")
			}
			if evicted != nil && evicted.Skill != nil {
				o.logger.Info("skill evicted",
					zap.String("skill", evicted.Skill.Name),
					zap.String("session", sessionID),
					zap.Float64("confidence", evicted.Confidence),
					zap.Duration("active_for", time.Since(evicted.ActivatedAt)),
					zap.String("reason", "max_concurrent_exceeded"),
					zap.String("evicted_for", skill.Name),
				)
			}
			// Fire the eviction callback (outside critical path, async).
			if o.onSkillEviction != nil && evicted != nil && evicted.Skill != nil {
				fn := o.onSkillEviction
				activeFor := time.Since(evicted.ActivatedAt)
				go fn(sessionID, evicted.Skill, activeFor)
			}
		} else {
			if span != nil {
				// Every existing skill is sticky; the cap overflows for this
				// turn rather than abandoning load-bearing work.
				span.SetAttribute("eviction", "overflow_all_sticky")
			}
			o.logger.Info("skill cap overflow (all sticky)",
				zap.String("skill", skill.Name),
				zap.String("session", sessionID),
				zap.Int("active_count", len(sessions)),
				zap.Int("max_concurrent", maxConcurrent),
			)
		}
	}

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

// FormatActiveSkillsForLLM combines all active skill prompts within the given
// token budget. Skills are included in FIFO order (activation time). If a skill's
// formatted prompt exceeds the remaining budget it is skipped.
// Token estimation: 1 token ~ 4 characters.
func (o *Orchestrator) FormatActiveSkillsForLLM(sessionID string, maxTokens int) string {
	active := o.GetActiveSkills(sessionID)
	if len(active) == 0 {
		return ""
	}

	// Sort by activation time (FIFO).
	sort.Slice(active, func(i, j int) bool {
		return active[i].ActivatedAt.Before(active[j].ActivatedAt)
	})

	const charsPerToken = 4
	maxChars := maxTokens * charsPerToken
	usedChars := 0

	var sb strings.Builder
	included := 0

	for _, a := range active {
		formatted := a.Skill.FormatForLLM()
		fLen := len(formatted)

		// Add separator overhead.
		separatorLen := 0
		if included > 0 {
			separatorLen = len("\n---\n")
		}

		if usedChars+fLen+separatorLen > maxChars {
			continue // skip this skill, it doesn't fit
		}

		if included > 0 {
			sb.WriteString("\n---\n")
		}
		sb.WriteString(formatted)
		usedChars += fLen + separatorLen
		included++
	}

	return sb.String()
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
	parts := strings.SplitN(msg, " ", 2)
	cmd = strings.ToLower(parts[0])
	if len(parts) > 1 {
		rest = parts[1]
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
