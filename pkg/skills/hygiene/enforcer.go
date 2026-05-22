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
	"github.com/teradata-labs/loom/pkg/task"
)

// TaskMutator is the subset of task.Manager the enforcer needs to repair
// task state under AUTO_FIX. Defined as an interface for test substitution.
type TaskMutator interface {
	TransitionTask(ctx context.Context, taskID string, newStatus loomv1.TaskStatus) (*task.Task, error)
	UpdateTask(ctx context.Context, t *task.Task, fields []string) (*task.Task, error)
}

// HITLSpawner spawns a human-in-the-loop request for a task whose blocking
// condition was never surfaced. Optional; when nil, BLOCKED tasks under
// AUTO_FIX are logged but not turned into HITL requests. The agent layer
// supplies an adapter over shuttle.ContactHumanTool.
type HITLSpawner interface {
	SpawnHITL(ctx context.Context, sessionID, agentID, question string, t *task.Task) error
}

// EnforcementOutcome is the structured result of one Enforce pass. It is
// surfaced into the agent Response.Metadata so callers and tests can
// observe what hygiene did this turn.
type EnforcementOutcome struct {
	Policy            loomv1.HygienePolicy
	ViolationsFound   int
	ViolationsByKind  map[string]int
	Resolved          int    // tasks the enforcer mutated under AUTO_FIX
	HITLSpawned       int    // BLOCKED tasks turned into HITL requests
	FallthroughReason string // populated when REQUIRE_FIX fell through to AUTO_FIX
	// InjectionMessage is the synthetic message the caller should append
	// to the conversation under REQUIRE_FIX. Empty when no injection is
	// needed (clean board, or non-REQUIRE_FIX policy).
	InjectionMessage string
	// ShouldRetry is true when the caller should re-run the LLM turn after
	// applying InjectionMessage. False when no further work is needed.
	ShouldRetry bool
}

// Enforcer applies a Report to the task board according to the resolved
// HygienePolicy. Goroutine-safe.
type Enforcer struct {
	tasks   TaskMutator
	hitl    HITLSpawner
	tracer  observability.Tracer
	logger  *zap.Logger
	agentID string
}

// EnforcerOption configures an Enforcer at construction.
type EnforcerOption func(*Enforcer)

// WithEnforcerTracer wires an observability tracer.
func WithEnforcerTracer(t observability.Tracer) EnforcerOption {
	return func(e *Enforcer) {
		if t != nil {
			e.tracer = t
		}
	}
}

// WithEnforcerLogger wires a zap logger.
func WithEnforcerLogger(l *zap.Logger) EnforcerOption {
	return func(e *Enforcer) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithHITLSpawner wires the HITL adapter used to surface BLOCKED tasks
// when policy is AUTO_FIX (or REQUIRE_FIX fell through). Optional.
func WithHITLSpawner(h HITLSpawner) EnforcerOption {
	return func(e *Enforcer) {
		e.hitl = h
	}
}

// WithAgentID stamps the agent ID into spawned HITL requests so the UI
// can route notifications back to the right session/agent.
func WithAgentID(id string) EnforcerOption {
	return func(e *Enforcer) {
		e.agentID = id
	}
}

// NewEnforcer constructs an Enforcer. tasks must be non-nil.
func NewEnforcer(tasks TaskMutator, opts ...EnforcerOption) *Enforcer {
	if tasks == nil {
		panic("hygiene.NewEnforcer: tasks must not be nil")
	}
	e := &Enforcer{
		tasks:  tasks,
		tracer: observability.NewNoOpTracer(),
		logger: zap.NewNop(),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Enforce applies the policy carried on the Report. retryCount is the
// number of REQUIRE_FIX retries already performed this turn; when it
// equals or exceeds maxRetries the enforcer transparently falls through
// to AUTO_FIX so the loop terminates.
//
// A nil or empty-violations Report returns a clean outcome with no work
// done. Errors are surfaced; the caller should log and treat them as
// non-fatal (don't block the turn on hygiene failure).
func (e *Enforcer) Enforce(ctx context.Context, report *Report, retryCount, maxRetries int) (*EnforcementOutcome, error) {
	ctx, span := e.tracer.StartSpan(ctx, "skills.hygiene.enforce")
	defer e.tracer.EndSpan(span)

	outcome := &EnforcementOutcome{
		Policy:           loomv1.HygienePolicy_HYGIENE_POLICY_UNSPECIFIED,
		ViolationsByKind: map[string]int{},
	}
	if report == nil || !report.HasViolations() {
		return outcome, nil
	}

	outcome.Policy = report.Policy
	outcome.ViolationsFound = len(report.Violations)
	outcome.ViolationsByKind = report.CountByKind()
	span.SetAttribute("hygiene.policy", report.Policy.String())
	span.SetAttribute("hygiene.violations", int64(outcome.ViolationsFound))
	span.SetAttribute("hygiene.retry_count", int64(retryCount))

	policy := report.Policy
	if policy == loomv1.HygienePolicy_HYGIENE_POLICY_REQUIRE_FIX && retryCount >= maxRetries {
		policy = loomv1.HygienePolicy_HYGIENE_POLICY_AUTO_FIX
		outcome.FallthroughReason = fmt.Sprintf("max_retries=%d exhausted", maxRetries)
		span.AddEvent("hygiene.fallthrough_to_autofix", map[string]any{
			"retries":     retryCount,
			"max_retries": maxRetries,
		})
		e.logger.Warn("hygiene REQUIRE_FIX cap reached; falling through to AUTO_FIX",
			zap.String("session", report.SessionID),
			zap.Int("retries", retryCount),
			zap.Int("max_retries", maxRetries),
		)
	}

	switch policy {
	case loomv1.HygienePolicy_HYGIENE_POLICY_REQUIRE_FIX:
		outcome.InjectionMessage = report.FormatToolMessage()
		outcome.ShouldRetry = true
		span.AddEvent("hygiene.fixup_injected", map[string]any{
			"retry":      retryCount + 1,
			"violations": outcome.ViolationsFound,
		})
		e.logger.Info("hygiene injected fixup message",
			zap.String("session", report.SessionID),
			zap.Int("retry", retryCount+1),
			zap.Int("violations", outcome.ViolationsFound),
		)
	case loomv1.HygienePolicy_HYGIENE_POLICY_AUTO_FIX:
		if err := e.autoFix(ctx, report, outcome); err != nil {
			return outcome, err
		}
	case loomv1.HygienePolicy_HYGIENE_POLICY_WARN_ONLY,
		loomv1.HygienePolicy_HYGIENE_POLICY_UNSPECIFIED:
		// Observability events were already emitted during Audit; nothing
		// further to do. The caller surfaces ViolationsByKind in the
		// response metadata.
		e.logger.Warn("hygiene WARN_ONLY: violations left in place",
			zap.String("session", report.SessionID),
			zap.Int("violations", outcome.ViolationsFound),
		)
	}

	return outcome, nil
}

// autoFix machine-repairs the board:
//   - OPEN-unstarted  -> DEFERRED + note explaining the auto-fix
//   - IN_PROGRESS     -> OPEN + note (releases the orphan claim so a fresh
//     run can pick it back up)
//   - BLOCKED         -> spawn HITL when a spawner is wired; otherwise log
func (e *Enforcer) autoFix(ctx context.Context, report *Report, outcome *EnforcementOutcome) error {
	now := time.Now().UTC()
	for _, v := range report.Violations {
		switch v.Kind {
		case ViolationOpenUnstarted:
			note := fmt.Sprintf("auto-fix: deferred at end of turn; skill %q never started this task (%s)",
				v.SkillName, now.Format(time.RFC3339))
			if err := e.applyNote(ctx, v.Task, note); err != nil {
				return err
			}
			if _, err := e.tasks.TransitionTask(ctx, v.Task.ID, loomv1.TaskStatus_TASK_STATUS_DEFERRED); err != nil {
				return fmt.Errorf("auto-fix open->deferred %s: %w", v.Task.ID, err)
			}
			outcome.Resolved++
		case ViolationInProgressOrphan:
			note := fmt.Sprintf("auto-fix: released orphan claim at end of turn; skill %q left IN_PROGRESS (%s)",
				v.SkillName, now.Format(time.RFC3339))
			if err := e.applyNote(ctx, v.Task, note); err != nil {
				return err
			}
			if _, err := e.tasks.TransitionTask(ctx, v.Task.ID, loomv1.TaskStatus_TASK_STATUS_OPEN); err != nil {
				return fmt.Errorf("auto-fix in_progress->open %s: %w", v.Task.ID, err)
			}
			outcome.Resolved++
		case ViolationBlockedNoHITL:
			if e.hitl == nil {
				e.logger.Warn("hygiene BLOCKED task left unhandled: no HITL spawner wired",
					zap.String("session", report.SessionID),
					zap.String("task", v.Task.ID),
					zap.String("skill", v.SkillName),
				)
				continue
			}
			question := blockedQuestion(v)
			if err := e.hitl.SpawnHITL(ctx, report.SessionID, e.agentID, question, v.Task); err != nil {
				return fmt.Errorf("auto-fix spawn HITL for %s: %w", v.Task.ID, err)
			}
			outcome.HITLSpawned++
		}
	}
	return nil
}

// applyNote appends a hygiene note to a task without changing status. The
// agent and the audit log share the same Notes field, so we prefix with
// "[hygiene] " to keep machine-written notes distinguishable from agent
// commentary.
func (e *Enforcer) applyNote(ctx context.Context, t *task.Task, note string) error {
	if t.Notes == "" {
		t.Notes = "[hygiene] " + note
	} else {
		t.Notes = t.Notes + "\n[hygiene] " + note
	}
	_, err := e.tasks.UpdateTask(ctx, t, []string{"notes"})
	if err != nil {
		return fmt.Errorf("apply hygiene note to %s: %w", t.ID, err)
	}
	return nil
}

func blockedQuestion(v Violation) string {
	label := taskLabel(v.Task)
	if v.Reason != "" {
		return fmt.Sprintf("Skill %q has a BLOCKED task (%q) that was never surfaced for human input. The agent left this reason: %s. How should we proceed?",
			v.SkillName, label, v.Reason)
	}
	return fmt.Sprintf("Skill %q has a BLOCKED task (%q) that was never surfaced for human input, and no reason was recorded. How should we proceed?",
		v.SkillName, label)
}
