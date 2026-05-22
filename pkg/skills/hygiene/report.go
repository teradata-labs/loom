// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package hygiene

import (
	"fmt"
	"sort"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/task"
)

// ViolationKind classifies the way a task left the board in an incoherent
// state. See package doc for full definitions.
type ViolationKind int

const (
	// ViolationUnknown is the zero value and must never appear in a Report.
	ViolationUnknown ViolationKind = iota
	// ViolationInProgressOrphan: task is IN_PROGRESS but the agent never
	// closed or blocked it before the turn ended.
	ViolationInProgressOrphan
	// ViolationBlockedNoHITL: task is BLOCKED but no HITL request was
	// surfaced to the user.
	ViolationBlockedNoHITL
	// ViolationOpenUnstarted: task is OPEN and was never claimed/started.
	ViolationOpenUnstarted
)

// String renders the kind for log/event/message use. Stable strings —
// observability dashboards depend on these values.
func (k ViolationKind) String() string {
	switch k {
	case ViolationInProgressOrphan:
		return "in_progress_orphan"
	case ViolationBlockedNoHITL:
		return "blocked_no_hitl"
	case ViolationOpenUnstarted:
		return "open_unstarted"
	default:
		return "unknown"
	}
}

// Violation is a single offending (task, kind) pair tied to the skill run
// that produced it.
type Violation struct {
	SkillName string
	Kind      ViolationKind
	Task      *task.Task
	// Reason mirrors the task's CloseReason / Notes when present, so the
	// formatted message can echo the agent's own explanation back to it.
	Reason string
}

// Report is the output of one Audit pass.
type Report struct {
	SessionID  string
	Violations []Violation
	// Policy that produced this report. Auditor copies the resolved policy
	// here so the enforcer can act without re-resolving.
	Policy loomv1.HygienePolicy
}

// HasViolations is true when at least one violation was found.
func (r *Report) HasViolations() bool {
	return len(r.Violations) > 0
}

// CountByKind returns a map from violation-kind string to count. Useful
// for observability events and response metadata. Unknown kinds are
// omitted; the zero-violation case returns a non-nil empty map.
func (r *Report) CountByKind() map[string]int {
	out := map[string]int{
		ViolationInProgressOrphan.String(): 0,
		ViolationBlockedNoHITL.String():    0,
		ViolationOpenUnstarted.String():    0,
	}
	for _, v := range r.Violations {
		out[v.Kind.String()]++
	}
	return out
}

// FormatToolMessage renders the violations as a synthetic user message the
// agent will see at the start of the next turn under REQUIRE_FIX policy.
// The message is direct and task-oriented, listing every violation with a
// clear instruction for resolution. Grouped by skill, then by kind.
func (r *Report) FormatToolMessage() string {
	if !r.HasViolations() {
		return ""
	}

	// Group by skill -> kind -> tasks for deterministic output.
	bySkill := map[string]map[ViolationKind][]Violation{}
	skills := []string{}
	for _, v := range r.Violations {
		if _, ok := bySkill[v.SkillName]; !ok {
			bySkill[v.SkillName] = map[ViolationKind][]Violation{}
			skills = append(skills, v.SkillName)
		}
		bySkill[v.SkillName][v.Kind] = append(bySkill[v.SkillName][v.Kind], v)
	}
	sort.Strings(skills)

	var b strings.Builder
	b.WriteString("Task-board hygiene check found violations from your skill run that must be resolved before this turn ends.\n\n")

	for _, skill := range skills {
		fmt.Fprintf(&b, "Skill: %s\n", skill)
		// Stable kind order: IN_PROGRESS, BLOCKED, OPEN.
		for _, kind := range []ViolationKind{ViolationInProgressOrphan, ViolationBlockedNoHITL, ViolationOpenUnstarted} {
			items := bySkill[skill][kind]
			if len(items) == 0 {
				continue
			}
			fmt.Fprintf(&b, "  %s (%d):\n", kindHeader(kind), len(items))
			for _, v := range items {
				fmt.Fprintf(&b, "    - %q (id=%s)", taskLabel(v.Task), v.Task.ID)
				if v.Reason != "" {
					fmt.Fprintf(&b, " — reason: %s", v.Reason)
				}
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "    Action required: %s\n", kindAction(kind))
		}
	}

	b.WriteString("\nResolve each violation now, then return your final response. Do not silently end the turn.")
	return b.String()
}

func kindHeader(k ViolationKind) string {
	switch k {
	case ViolationInProgressOrphan:
		return "IN_PROGRESS but not closed"
	case ViolationBlockedNoHITL:
		return "BLOCKED without surfacing a question"
	case ViolationOpenUnstarted:
		return "OPEN and never started"
	default:
		return "unknown"
	}
}

func kindAction(k ViolationKind) string {
	switch k {
	case ViolationInProgressOrphan:
		return "Move each task to DONE if work is complete, or to BLOCKED with a clear reason if you cannot proceed."
	case ViolationBlockedNoHITL:
		return "Surface the blocking question to the user in your response, or transition the task off BLOCKED if no longer blocked."
	case ViolationOpenUnstarted:
		return "Start each task now, or transition it to DEFERRED with a reason note, or explain in your response why these tasks remain unstarted."
	default:
		return ""
	}
}

func taskLabel(t *task.Task) string {
	if t.Title != "" {
		return t.Title
	}
	if t.Objective != "" {
		return t.Objective
	}
	return t.ID
}
