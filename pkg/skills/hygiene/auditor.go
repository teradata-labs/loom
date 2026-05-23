// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package hygiene

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/task"
)

// DefaultMaxRetries is the cap on REQUIRE_FIX retries per turn when
// HygieneConfig.max_retries <= 0. Two retries is enough for the agent to
// notice the message and act; further loops typically indicate a stuck
// LLM rather than a hygiene gap, so we fall through to AUTO_FIX.
const DefaultMaxRetries = 2

// SkillRunLister is the subset of task.Manager the auditor needs. Defined
// as an interface so tests can substitute a stub without standing up a
// real store.
type SkillRunLister interface {
	ListBySkillRun(ctx context.Context, skillName, sessionID string) ([]*task.Task, error)
}

// ActiveSkillSource exposes the orchestrator's active-skill view. Defined
// as an interface so tests don't need a real Orchestrator.
type ActiveSkillSource interface {
	GetActiveSkills(sessionID string) []*skills.ActiveSkill
}

// Auditor inventories the task board for currently-active skills and
// classifies any incoherent state as a Violation. Goroutine-safe; one
// instance per agent is normal.
type Auditor struct {
	lister      SkillRunLister
	activeSrc   ActiveSkillSource
	tracer      observability.Tracer
	logger      *zap.Logger
	defaultPol  loomv1.HygienePolicy
	defaultMaxR int
}

// Option configures an Auditor at construction.
type Option func(*Auditor)

// WithTracer wires an observability tracer. Audit creates one span per
// invocation and emits a violation-count event.
func WithTracer(t observability.Tracer) Option {
	return func(a *Auditor) {
		if t != nil {
			a.tracer = t
		}
	}
}

// WithLogger wires a zap logger. Defaults to zap.NewNop().
func WithLogger(l *zap.Logger) Option {
	return func(a *Auditor) {
		if l != nil {
			a.logger = l
		}
	}
}

// WithDefaultPolicy sets the fallback policy used when HygieneConfig is
// nil or has POLICY_UNSPECIFIED. Defaults to REQUIRE_FIX.
func WithDefaultPolicy(p loomv1.HygienePolicy) Option {
	return func(a *Auditor) {
		if p != loomv1.HygienePolicy_HYGIENE_POLICY_UNSPECIFIED {
			a.defaultPol = p
		}
	}
}

// WithDefaultMaxRetries sets the fallback retry cap used when
// HygieneConfig.max_retries <= 0. Defaults to DefaultMaxRetries.
func WithDefaultMaxRetries(n int) Option {
	return func(a *Auditor) {
		if n > 0 {
			a.defaultMaxR = n
		}
	}
}

// NewAuditor constructs an Auditor. lister and activeSrc must be non-nil.
func NewAuditor(lister SkillRunLister, activeSrc ActiveSkillSource, opts ...Option) *Auditor {
	if lister == nil {
		panic("hygiene.NewAuditor: lister must not be nil")
	}
	if activeSrc == nil {
		panic("hygiene.NewAuditor: activeSrc must not be nil")
	}
	a := &Auditor{
		lister:      lister,
		activeSrc:   activeSrc,
		tracer:      observability.NewNoOpTracer(),
		logger:      zap.NewNop(),
		defaultPol:  loomv1.HygienePolicy_HYGIENE_POLICY_REQUIRE_FIX,
		defaultMaxR: DefaultMaxRetries,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// ResolvePolicy returns the effective policy for the given config. nil or
// UNSPECIFIED falls back to the auditor's default. Exposed so the agent
// hook can decide before calling Audit (e.g., short-circuit on
// !enabled).
func (a *Auditor) ResolvePolicy(cfg *loomv1.HygieneConfig) loomv1.HygienePolicy {
	if cfg == nil || cfg.Policy == loomv1.HygienePolicy_HYGIENE_POLICY_UNSPECIFIED {
		return a.defaultPol
	}
	return cfg.Policy
}

// ResolveMaxRetries returns the effective retry cap for the given config.
func (a *Auditor) ResolveMaxRetries(cfg *loomv1.HygieneConfig) int {
	if cfg == nil || cfg.MaxRetries <= 0 {
		return a.defaultMaxR
	}
	return int(cfg.MaxRetries)
}

// Enabled reports whether the auditor should run for the given config.
// Nil config defaults to enabled (proto3 optional "not specified" -> true).
func (a *Auditor) Enabled(cfg *loomv1.HygieneConfig) bool {
	if cfg == nil {
		return true
	}
	if cfg.Enabled == nil {
		return true
	}
	return *cfg.Enabled
}

// Audit inspects every currently-active skill for the session and returns
// a Report of any violations. An empty Report (HasViolations() == false)
// means the board is clean. Errors from the underlying lister are
// surfaced; the caller should treat them as non-fatal (log and skip
// hygiene rather than block the turn).
func (a *Auditor) Audit(ctx context.Context, sessionID string, cfg *loomv1.HygieneConfig) (*Report, error) {
	ctx, span := a.tracer.StartSpan(ctx, "skills.hygiene.audit")
	defer a.tracer.EndSpan(span)

	report := &Report{
		SessionID: sessionID,
		Policy:    a.ResolvePolicy(cfg),
	}

	active := a.activeSrc.GetActiveSkills(sessionID)
	if len(active) == 0 {
		span.SetAttribute("hygiene.active_skills", 0)
		return report, nil
	}

	start := time.Now()
	for _, as := range active {
		if as == nil || as.Skill == nil {
			continue
		}
		tasks, err := a.lister.ListBySkillRun(ctx, as.Skill.Name, sessionID)
		if err != nil {
			return nil, fmt.Errorf("list tasks for skill %q: %w", as.Skill.Name, err)
		}
		for _, t := range tasks {
			if t == nil {
				continue
			}
			kind, ok := classify(t)
			if !ok {
				continue
			}
			report.Violations = append(report.Violations, Violation{
				SkillName: as.Skill.Name,
				Kind:      kind,
				Task:      t,
				Reason:    extractReason(t),
			})
		}
	}

	counts := report.CountByKind()
	span.SetAttribute("hygiene.active_skills", int64(len(active)))
	span.SetAttribute("hygiene.violations_total", int64(len(report.Violations)))
	span.SetAttribute("hygiene.duration_ms", time.Since(start).Milliseconds())
	if report.HasViolations() {
		span.AddEvent("hygiene.violation_found", map[string]any{
			"session_id":         sessionID,
			"in_progress_orphan": counts[ViolationInProgressOrphan.String()],
			"blocked_no_hitl":    counts[ViolationBlockedNoHITL.String()],
			"open_unstarted":     counts[ViolationOpenUnstarted.String()],
			"policy":             report.Policy.String(),
		})
		a.logger.Info("hygiene violations found",
			zap.String("session", sessionID),
			zap.Int("count", len(report.Violations)),
			zap.Any("by_kind", counts),
			zap.String("policy", report.Policy.String()),
		)
	}

	return report, nil
}

// classify maps a task's current status to a ViolationKind. Returns
// (_, false) when the task is in a healthy terminal/non-violating state.
//
// Decision rules:
//   - IN_PROGRESS  -> always a violation (orphan claim).
//   - BLOCKED      -> always a violation (no HITL was surfaced from inside
//     the hygiene-relevant lifecycle; the auditor can't see the chat, so
//     it conservatively treats every BLOCKED as needing surfacing).
//   - OPEN         -> violation only when never claimed (ClaimedAt == nil).
//     OPEN tasks that were claimed-and-released are not the same failure
//     mode; the agent at least tried.
//   - DONE / DEFERRED / CANCELLED -> healthy.
func classify(t *task.Task) (ViolationKind, bool) {
	switch t.Status {
	case loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS:
		return ViolationInProgressOrphan, true
	case loomv1.TaskStatus_TASK_STATUS_BLOCKED:
		return ViolationBlockedNoHITL, true
	case loomv1.TaskStatus_TASK_STATUS_OPEN:
		if t.ClaimedAt == nil {
			return ViolationOpenUnstarted, true
		}
		return ViolationUnknown, false
	default:
		return ViolationUnknown, false
	}
}

// extractReason picks the most informative human-readable text the agent
// already wrote about why this task is in its current state. Falls back
// to an empty string when no notes were captured.
func extractReason(t *task.Task) string {
	if t.CloseReason != "" {
		return t.CloseReason
	}
	if t.Notes != "" {
		return t.Notes
	}
	return ""
}
