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

// Package apprentice distills observed work into candidate automation.
//
// Where the weaver authors from intent ("build me a research workflow"), the
// apprentice authors from evidence: it reads work that actually happened and
// recovers a reusable form of it.
//
// This file implements the first and cheapest capability — the exact inverse
// of pkg/skills/tasks. That package materializes a skill's SkillTaskTemplate
// onto a task board; Distill recovers a SkillTaskTemplate from a board that
// work ran on. The task board is the trace spine because it already carries
// ordering, dependencies, and per-step objectives; richer trace sources
// (tool_executions, spans) fill in detail in later phases.
//
// Being an exact inverse makes the pair self-checking: emit a shipped skill's
// template, distill it back, and diff structurally. That round trip is the
// correctness oracle for the whole feature and needs no LLM. See
// docs/plan-apprentice.md.
//
// Distill is deterministic and contains no LLM call. Generalization — turning
// concrete literals into typed parameters — is a separate, LLM-driven step and
// is deliberately not in this file, so everything here is ordinarily testable.
package apprentice

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/skills"
	skilltasks "github.com/teradata-labs/loom/pkg/skills/tasks"
	"github.com/teradata-labs/loom/pkg/task"
)

const (
	// stepKeyDelimiter is the trailing segment of the emitter's
	// SkillIdempotencyKey, "skill:<name>|sess:<session>|step:<index>".
	// Distill parses the index back out of it rather than duplicating the
	// key format; see pkg/skills/tasks.
	stepKeyDelimiter = "|step:"

	// metadataStepKey is the emitter's redundant step-index channel on
	// Task.Metadata. Used as a fallback when the idempotency key is absent
	// or malformed.
	metadataStepKey = "skill_step"

	// listLimit bounds one board read. Boards larger than this produce a
	// truncation warning rather than a silently partial template.
	listLimit = 500
)

// Reader is the narrow read surface Distill needs over a task board. It is an
// interface so callers can supply a fake in tests without a database, and so
// Distill stays independent of how the board is stored.
type Reader interface {
	ListTasks(ctx context.Context, opts task.ListTasksOpts) ([]*task.Task, int, error)
	GetDependencies(ctx context.Context, taskID string) ([]*task.TaskDependency, error)
}

// managerReader adapts *task.Manager to Reader. Dependency reads go through
// the manager's store because Manager intentionally exposes no dependency
// getter of its own.
type managerReader struct{ m *task.Manager }

// NewManagerReader returns a Reader backed by a live task.Manager.
func NewManagerReader(m *task.Manager) Reader { return &managerReader{m: m} }

func (r *managerReader) ListTasks(ctx context.Context, opts task.ListTasksOpts) ([]*task.Task, int, error) {
	return r.m.ListTasks(ctx, opts)
}

func (r *managerReader) GetDependencies(ctx context.Context, taskID string) ([]*task.TaskDependency, error) {
	return r.m.Store().GetDependencies(ctx, taskID)
}

// OrderSource records how Distill recovered step order. Which path ran is
// worth surfacing: keyed recovery is exact, topological recovery is inferred
// and therefore the one that matters for real work.
type OrderSource string

const (
	// OrderIdempotencyKey means every task carried a skill step index, so
	// the recovered order is exactly the authored order. Only possible on
	// boards a skill emitted.
	OrderIdempotencyKey OrderSource = "idempotency_key"

	// OrderTopological means order was inferred from the dependency DAG,
	// tie-broken by creation time. This is the path real, non-skill-emitted
	// work takes.
	OrderTopological OrderSource = "topological"
)

// Options configures a distillation.
type Options struct {
	// BoardID scopes the read to one board. Empty reads across boards,
	// which is rarely what a caller wants.
	BoardID string

	// IgnoreKeys forces topological recovery even when skill step indices
	// are present. The round-trip oracle sets this to exercise the
	// inference path against a board whose true order is known.
	IgnoreKeys bool
}

// Result is a recovered template plus the provenance a reviewer needs to
// judge it.
type Result struct {
	// Template is the recovered decomposition, never nil on success.
	Template *skills.SkillTaskTemplate

	// OrderSource records how step order was determined.
	OrderSource OrderSource

	// TaskIDs maps recovered step index to the source task ID, so a
	// candidate can be traced back to the work it came from.
	TaskIDs []string

	// Warnings describe lossy or ambiguous recovery. Non-fatal by design:
	// a caller shows them to a reviewer rather than discarding the
	// candidate.
	Warnings []string
}

// Distill recovers a SkillTaskTemplate from the tasks on a board.
//
// It never fails on a merely awkward board — an empty board, a dependency
// cycle, or a truncating read all produce a Result with warnings rather than
// an error. Errors are reserved for a broken Reader.
func Distill(ctx context.Context, r Reader, opts Options) (*Result, error) {
	if r == nil {
		return nil, fmt.Errorf("apprentice: Distill requires a non-nil Reader")
	}

	tasks, total, err := r.ListTasks(ctx, task.ListTasksOpts{
		BoardID: opts.BoardID,
		Limit:   listLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("apprentice: list tasks: %w", err)
	}

	res := &Result{Template: &skills.SkillTaskTemplate{}}
	if total > len(tasks) {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"board has %d tasks but only %d were read; recovered template is partial", total, len(tasks)))
	}
	if len(tasks) == 0 {
		res.OrderSource = OrderTopological
		res.Warnings = append(res.Warnings, "board has no tasks; recovered template is empty")
		return res, nil
	}

	// Read every dependency edge once. Both the ordering pass and the
	// depends_on pass need them, and re-reading per pass would double the
	// query count for no benefit.
	deps := make(map[string][]*task.TaskDependency, len(tasks))
	for _, t := range tasks {
		d, err := r.GetDependencies(ctx, t.ID)
		if err != nil {
			return nil, fmt.Errorf("apprentice: get dependencies for %s: %w", t.ID, err)
		}
		deps[t.ID] = d
	}

	ordered, source, warns := orderTasks(tasks, deps, opts.IgnoreKeys)
	res.OrderSource = source
	res.Warnings = append(res.Warnings, warns...)

	// position lets the depends_on pass translate task IDs into step indices.
	position := make(map[string]int32, len(ordered))
	for i, t := range ordered {
		position[t.ID] = int32(i)
	}

	res.Template.Steps = make([]skills.SkillTaskStep, 0, len(ordered))
	res.TaskIDs = make([]string, 0, len(ordered))
	for i, t := range ordered {
		step := skills.SkillTaskStep{
			Title:              t.Title,
			Objective:          t.Objective,
			AcceptanceCriteria: t.AcceptanceCriteria,
			Category:           formatCategory(t.Category),
			Priority:           formatPriority(t.Priority),
			EstimatedEffort:    t.EstimatedEffort,
		}
		if len(t.Tags) > 0 {
			step.Tags = append([]string{}, t.Tags...)
		}

		// Edge semantics: GetDependencies(X) returns edges where X depends
		// on Y, so each ToTaskID becomes a depends_on index. Edges pointing
		// off the board cannot be expressed as a step index and are dropped
		// with a warning rather than silently.
		var dependsOn []int32
		for _, edge := range deps[t.ID] {
			pos, ok := position[edge.ToTaskID]
			if !ok {
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"step %d (%s) depends on task %s which is not on this board; edge dropped",
					i, t.ID, edge.ToTaskID))
				continue
			}
			if pos == int32(i) {
				continue // self-dependency carries no ordering information
			}
			dependsOn = append(dependsOn, pos)
		}
		if len(dependsOn) > 0 {
			sort.Slice(dependsOn, func(a, b int) bool { return dependsOn[a] < dependsOn[b] })
			step.DependsOn = dedupeInt32(dependsOn)
		}

		res.Template.Steps = append(res.Template.Steps, step)
		res.TaskIDs = append(res.TaskIDs, t.ID)
	}

	// A template longer than the emitter's cap would be truncated on
	// re-emission, so the recovered form is only faithful if the caller
	// raises MaxTasks. Warn instead of guessing a value.
	if len(res.Template.Steps) > skilltasks.DefaultMaxTasks {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"recovered %d steps but the emitter default cap is %d; set max_tasks or the template will be truncated on re-emission",
			len(res.Template.Steps), skilltasks.DefaultMaxTasks))
	}

	return res, nil
}

// orderTasks recovers step order, preferring exact skill step indices and
// falling back to a topological sort of the dependency DAG.
func orderTasks(tasks []*task.Task, deps map[string][]*task.TaskDependency, ignoreKeys bool) ([]*task.Task, OrderSource, []string) {
	if !ignoreKeys {
		if ordered, ok := orderByStepIndex(tasks); ok {
			return ordered, OrderIdempotencyKey, nil
		}
	}
	ordered, warns := orderTopologically(tasks, deps)
	return ordered, OrderTopological, warns
}

// orderByStepIndex sorts by the skill step index carried on every task. It
// reports false unless every task has an index and all indices are distinct —
// a partial set would silently interleave keyed and unkeyed tasks.
func orderByStepIndex(tasks []*task.Task) ([]*task.Task, bool) {
	type indexed struct {
		t   *task.Task
		idx int
	}
	out := make([]indexed, 0, len(tasks))
	seen := make(map[int]bool, len(tasks))
	for _, t := range tasks {
		idx, ok := stepIndexOf(t)
		if !ok || seen[idx] {
			return nil, false
		}
		seen[idx] = true
		out = append(out, indexed{t: t, idx: idx})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].idx < out[b].idx })

	ordered := make([]*task.Task, 0, len(out))
	for _, i := range out {
		ordered = append(ordered, i.t)
	}
	return ordered, true
}

// stepIndexOf extracts a skill step index from a task, preferring the
// idempotency key and falling back to the metadata channel the emitter
// writes alongside it.
func stepIndexOf(t *task.Task) (int, bool) {
	if i := strings.LastIndex(t.SkillIdempotencyKey, stepKeyDelimiter); i >= 0 {
		raw := t.SkillIdempotencyKey[i+len(stepKeyDelimiter):]
		if idx, err := strconv.Atoi(raw); err == nil && idx >= 0 {
			return idx, true
		}
	}
	if raw, ok := t.Metadata[metadataStepKey]; ok {
		if idx, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && idx >= 0 {
			return idx, true
		}
	}
	return 0, false
}

// orderTopologically runs Kahn's algorithm over the dependency DAG.
//
// Ties are broken by creation time, then by the order the board read returned,
// so output is deterministic. Task.ID is deliberately not the tie-breaker: it
// is a content hash, so ordering by it is stable but arbitrary, and two
// equally-ready steps would come back in hash order rather than anything a
// reader could predict.
//
// A tie is not merely a formatting concern. Whenever more than one task is
// ready at once the DAG does not constrain their relative order, so the
// authored order is not recoverable — only a valid one is. That case is
// reported as a warning, because a reviewer judging a candidate needs to know
// which step ordering is evidence and which is a guess. Exact authored order
// survives only via step indices; see OrderIdempotencyKey.
//
// Manager.AddDependency rejects cycles, so a cycle here means the board was
// written by something else. Rather than loop forever or drop the tasks, the
// remaining nodes are appended in deterministic order with a warning.
func orderTopologically(tasks []*task.Task, deps map[string][]*task.TaskDependency) ([]*task.Task, []string) {
	byID := make(map[string]*task.Task, len(tasks))
	listPos := make(map[string]int, len(tasks))
	for i, t := range tasks {
		byID[t.ID] = t
		listPos[t.ID] = i
	}

	sortReady := func(ts []*task.Task) {
		sort.Slice(ts, func(a, b int) bool {
			if !ts[a].CreatedAt.Equal(ts[b].CreatedAt) {
				return ts[a].CreatedAt.Before(ts[b].CreatedAt)
			}
			return listPos[ts[a].ID] < listPos[ts[b].ID]
		})
	}

	// indegree counts only dependencies that are present on this board;
	// an edge pointing elsewhere cannot gate ordering.
	indegree := make(map[string]int, len(tasks))
	dependents := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		for _, edge := range deps[t.ID] {
			if edge.ToTaskID == t.ID {
				continue // self-edge
			}
			if _, onBoard := byID[edge.ToTaskID]; !onBoard {
				continue
			}
			indegree[t.ID]++
			dependents[edge.ToTaskID] = append(dependents[edge.ToTaskID], t.ID)
		}
	}

	ready := make([]*task.Task, 0, len(tasks))
	for _, t := range tasks {
		if indegree[t.ID] == 0 {
			ready = append(ready, t)
		}
	}
	sortReady(ready)

	ordered := make([]*task.Task, 0, len(tasks))
	placed := make(map[string]bool, len(tasks))
	unconstrained := 0
	for len(ready) > 0 {
		if len(ready) > 1 {
			unconstrained++
		}
		next := ready[0]
		ready = ready[1:]
		ordered = append(ordered, next)
		placed[next.ID] = true

		var freed []*task.Task
		for _, depID := range dependents[next.ID] {
			indegree[depID]--
			if indegree[depID] == 0 {
				if t, ok := byID[depID]; ok {
					freed = append(freed, t)
				}
			}
		}
		if len(freed) > 0 {
			ready = append(ready, freed...)
			sortReady(ready)
		}
	}

	var warnings []string
	if unconstrained > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"the dependency graph left step order unconstrained at %d point(s); the recovered order is one valid ordering, not necessarily the order the work happened in",
			unconstrained))
	}

	if len(ordered) == len(tasks) {
		return ordered, warnings
	}

	// Cycle: append whatever is left, deterministically.
	var stuck []*task.Task
	for _, t := range tasks {
		if !placed[t.ID] {
			stuck = append(stuck, t)
		}
	}
	sortReady(stuck)
	ordered = append(ordered, stuck...)

	stuckIDs := make([]string, 0, len(stuck))
	for _, t := range stuck {
		stuckIDs = append(stuckIDs, t.ID)
	}
	return ordered, append(warnings, fmt.Sprintf(
		"dependency cycle among %d task(s) (%s); their relative order is creation-time only",
		len(stuck), strings.Join(stuckIDs, ", ")))
}

func dedupeInt32(in []int32) []int32 {
	out := in[:0:0]
	for i, v := range in {
		if i > 0 && v == in[i-1] {
			continue
		}
		out = append(out, v)
	}
	return out
}

// formatCategory is the inverse of task.ParseCategory. Values it emits
// re-parse to the same enum, which is what the round-trip oracle asserts.
//
// OTHER and UNSPECIFIED both map to the empty string: ParseCategory("")
// yields OTHER, so omitting the field is faithful and keeps generated
// templates free of noise a reviewer would have to read past.
func formatCategory(c loomv1.TaskCategory) string {
	switch c {
	case loomv1.TaskCategory_TASK_CATEGORY_RESEARCH:
		return "research"
	case loomv1.TaskCategory_TASK_CATEGORY_ANALYSIS:
		return "analysis"
	case loomv1.TaskCategory_TASK_CATEGORY_IMPLEMENTATION:
		return "implementation"
	case loomv1.TaskCategory_TASK_CATEGORY_REVIEW:
		return "review"
	case loomv1.TaskCategory_TASK_CATEGORY_WRITING:
		return "writing"
	case loomv1.TaskCategory_TASK_CATEGORY_DECISION:
		return "decision"
	case loomv1.TaskCategory_TASK_CATEGORY_INVESTIGATION:
		return "investigation"
	case loomv1.TaskCategory_TASK_CATEGORY_PLANNING:
		return "planning"
	default:
		return ""
	}
}

// formatPriority is the inverse of task.ParsePriority, emitting the Pn form.
//
// MEDIUM maps to "P2" rather than "" even though ParsePriority("") also
// yields MEDIUM. A recovered step states its priority explicitly; collapsing
// the default would make an authored P2 and an omitted field indistinguishable
// in the candidate a reviewer reads.
func formatPriority(p loomv1.TaskPriority) string {
	switch p {
	case loomv1.TaskPriority_TASK_PRIORITY_CRITICAL:
		return "P0"
	case loomv1.TaskPriority_TASK_PRIORITY_HIGH:
		return "P1"
	case loomv1.TaskPriority_TASK_PRIORITY_MEDIUM:
		return "P2"
	case loomv1.TaskPriority_TASK_PRIORITY_LOW:
		return "P3"
	case loomv1.TaskPriority_TASK_PRIORITY_BACKLOG:
		return "P4"
	default:
		return ""
	}
}
