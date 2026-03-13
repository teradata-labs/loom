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

	"github.com/teradata-labs/loom/pkg/observability"
)

// Orchestrator is the activation engine for skills. It evaluates user messages,
// matches them to skills via slash commands, keywords, or always-on rules,
// and manages active skill lifecycles within sessions.
type Orchestrator struct {
	mu             sync.RWMutex
	library        *Library
	tracer         observability.Tracer
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

// NewOrchestrator creates a new skill orchestrator backed by the given library.
func NewOrchestrator(library *Library, opts ...OrchestratorOption) *Orchestrator {
	o := &Orchestrator{
		library:        library,
		tracer:         observability.NewNoOpTracer(),
		activeSessions: make(map[string][]*ActiveSkill),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// SkillsConfig controls skill matching behavior for a session.
type SkillsConfig struct {
	Enabled              bool
	EnabledSkills        []string
	DisabledSkills       []string
	MinAutoConfidence    float64
	MaxConcurrentSkills  int
	SkillsDir            string
	ContextBudgetPercent int
}

// DefaultSkillsConfig returns a SkillsConfig with sensible defaults.
func DefaultSkillsConfig() *SkillsConfig {
	return &SkillsConfig{
		Enabled:              true,
		MaxConcurrentSkills:  3,
		MinAutoConfidence:    0.7,
		ContextBudgetPercent: 5,
	}
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

	var results []*MatchResult

	// 1. Check for slash command (highest priority).
	if cmd, rest := parseSlashCommand(userMsg); cmd != "" {
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
// MaxConcurrentSkills active, the lowest-confidence skill is evicted.
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
			return active
		}
	}

	sessions = append(sessions, active)

	// Evict lowest confidence if over default limit (3).
	const defaultMaxConcurrent = 3
	if len(sessions) > defaultMaxConcurrent {
		// Find lowest confidence.
		minIdx := 0
		for i := 1; i < len(sessions); i++ {
			if sessions[i].Confidence < sessions[minIdx].Confidence {
				minIdx = i
			}
		}
		// Remove it (swap with last, truncate).
		sessions[minIdx] = sessions[len(sessions)-1]
		sessions = sessions[:len(sessions)-1]
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

	return active
}

// DeactivateSkill removes a skill from a session.
func (o *Orchestrator) DeactivateSkill(sessionID, skillName string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	sessions := o.activeSessions[sessionID]
	for i, active := range sessions {
		if active.Skill.Name == skillName {
			// Remove by shifting.
			o.activeSessions[sessionID] = append(sessions[:i], sessions[i+1:]...)
			o.tracer.RecordMetric("skills.orchestrator.deactivate_skill", 1.0, map[string]string{
				"skill":   skillName,
				"session": sessionID,
			})
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

// parseSlashCommand extracts the slash command from a message.
// Returns command and remaining message, or empty strings if not a slash command.
// Example: "/review this code" -> ("/review", "this code")
func parseSlashCommand(msg string) (cmd string, rest string) {
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
