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

// Package tasks materializes the tasks an active skill produces. When the
// skill declares an authored SkillTaskTemplate, the emitter writes those
// steps directly via task.Manager. Otherwise it falls back to
// task.Decomposer which uses an LLM to break the skill prompt into tasks.
//
// All emitted tasks carry a SkillIdempotencyKey of the form
// "skill:<name>|sess:<sessionID>|step:<index>" so concurrent activations
// of the same skill on the same session don't create duplicate boards.
// See pkg/storage/{sqlite,postgres}/migrations/000008|000013.
package tasks

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/types"
)

// DefaultMaxTasks caps how many tasks one skill activation can emit. Both
// authored templates and decomposer fallback respect this cap.
const DefaultMaxTasks = 8

// Emitter converts skill activations into task records on the agent's
// task board. Goroutine-safe.
type Emitter struct {
	manager    *task.Manager
	decomposer *task.Decomposer
	tracer     observability.Tracer
	logger     *zap.Logger
	maxTasks   int
}

// EmitterOption configures an Emitter during construction.
type EmitterOption func(*Emitter)

// WithEmitterTracer attaches an observability tracer.
func WithEmitterTracer(t observability.Tracer) EmitterOption {
	return func(e *Emitter) {
		if t != nil {
			e.tracer = t
		}
	}
}

// WithEmitterLogger attaches a zap logger.
func WithEmitterLogger(l *zap.Logger) EmitterOption {
	return func(e *Emitter) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithEmitterMaxTasks overrides the default per-activation cap.
func WithEmitterMaxTasks(n int) EmitterOption {
	return func(e *Emitter) {
		if n > 0 {
			e.maxTasks = n
		}
	}
}

// NewEmitter constructs an Emitter. manager must be non-nil. decomposer is
// optional; when nil, the emitter only honors authored task_template steps
// and silently no-ops for skills that would otherwise need decomposition.
func NewEmitter(manager *task.Manager, decomposer *task.Decomposer, opts ...EmitterOption) *Emitter {
	if manager == nil {
		panic("tasks.NewEmitter: manager must not be nil")
	}
	e := &Emitter{
		manager:    manager,
		decomposer: decomposer,
		tracer:     observability.NewNoOpTracer(),
		logger:     zap.NewNop(),
		maxTasks:   DefaultMaxTasks,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// EmitRequest carries the per-activation context for the emitter.
type EmitRequest struct {
	// Skill is the activated skill. Required.
	Skill *skills.Skill
	// SessionID identifies the conversation session emitting the tasks.
	// Used to compose the idempotency key.
	SessionID string
	// AgentID identifies the agent on whose board the tasks land. Recorded
	// as Task.OwnerAgentID.
	AgentID string
	// BoardID is the kanban board to attach tasks to. Empty means the
	// emitter creates tasks without a board (still queryable via owner).
	BoardID string
	// LLM is the model the decomposer fallback uses when no task_template
	// is authored on the skill. Optional; if nil and the skill has no
	// template, the emitter no-ops with a warning.
	LLM types.LLMProvider
	// AgentTasksEnabled is the agent-level master switch for skill task
	// emission. The emitter short-circuits when this is false even if the
	// skill itself opts in.
	AgentTasksEnabled bool
}

// EmitResult summarizes what the emitter did during one activation.
type EmitResult struct {
	// Tasks is every task involved — both newly created and existing
	// (idempotent hits). Caller can use len() to distinguish.
	Tasks []*task.Task
	// CreatedCount is the number of tasks newly inserted; the rest are
	// idempotent hits returning the same row from a prior activation.
	CreatedCount int
	// Source describes which pathway emitted the tasks: "template",
	// "decomposer", or "none" (e.g. emit_tasks=false).
	Source string
}

// EmitForActivation materializes tasks for a freshly-activated skill.
// Idempotent: re-invoking with the same (skill, session) tuple returns the
// existing tasks rather than creating duplicates.
//
// Decision tree:
//  1. AgentTasksEnabled=false OR Skill.EffectiveEmitTasks()=false → no-op.
//  2. Skill has TaskTemplate with steps → materialize each step.
//  3. Otherwise → call decomposer.Decompose with the skill prompt as goal.
//
// Returns (nil, nil) when emission is skipped per (1); the EmitResult
// remains valid (zero CreatedCount, "none" source).
func (e *Emitter) EmitForActivation(ctx context.Context, req EmitRequest) (*EmitResult, error) {
	ctx, span := e.tracer.StartSpan(ctx, "skills.tasks.emit_for_activation")
	defer e.tracer.EndSpan(span)

	if req.Skill == nil {
		return nil, fmt.Errorf("emitter: req.Skill is required")
	}
	if span != nil {
		span.SetAttribute("skill.name", req.Skill.Name)
		span.SetAttribute("session.id", req.SessionID)
	}

	if !req.AgentTasksEnabled || !req.Skill.EffectiveEmitTasks() {
		return &EmitResult{Source: "none"}, nil
	}

	// Ensure the referenced board exists before any CreateTask. The tasks
	// table FK-references task_boards(id); a non-empty BoardID that does
	// not exist in storage triggers `FOREIGN KEY constraint failed` on
	// every step, silently turning Phase D into a no-op (failures landed
	// in zap.L() warns that, until cmd_serve.go wired ReplaceGlobals,
	// went to a Nop logger). Empty BoardID is fine and skips the ensure.
	if err := e.ensureBoard(ctx, req.BoardID, req.Skill.Name); err != nil {
		return nil, fmt.Errorf("emitter: ensure board: %w", err)
	}

	if req.Skill.TaskTemplate != nil && len(req.Skill.TaskTemplate.Steps) > 0 {
		return e.emitTemplate(ctx, req)
	}
	return e.emitDecomposed(ctx, req)
}

// emitTemplate materializes each step of an authored SkillTaskTemplate as
// a task on the board, wiring depends_on edges as TASK_DEPENDENCY_TYPE_BLOCKS.
func (e *Emitter) emitTemplate(ctx context.Context, req EmitRequest) (*EmitResult, error) {
	tmpl := req.Skill.TaskTemplate
	cap := e.cap(tmpl.MaxTasks)
	steps := tmpl.Steps
	if len(steps) > cap {
		e.logger.Debug("skill template exceeds max_tasks cap; truncating",
			zap.String("skill", req.Skill.Name),
			zap.Int("steps", len(steps)),
			zap.Int("cap", cap))
		steps = steps[:cap]
	}

	created := make([]*task.Task, 0, len(steps))
	createdCount := 0
	stepID := make([]string, len(steps))

	for i, step := range steps {
		key := buildIdempotencyKey(req.Skill.Name, req.SessionID, i)
		t := &task.Task{
			Title:               firstNonEmpty(step.Title, fmt.Sprintf("%s step %d", req.Skill.Title, i+1)),
			Objective:           step.Objective,
			AcceptanceCriteria:  step.AcceptanceCriteria,
			Tags:                append([]string{}, step.Tags...),
			Status:              loomv1.TaskStatus_TASK_STATUS_OPEN,
			Priority:            task.ParsePriority(step.Priority),
			Category:            task.ParseCategory(step.Category),
			OwnerAgentID:        req.AgentID,
			BoardID:             req.BoardID,
			EstimatedEffort:     step.EstimatedEffort,
			SkillIdempotencyKey: key,
			Metadata: map[string]string{
				"skill_name":                     req.Skill.Name,
				"skill_session":                  req.SessionID,
				"skill_step":                     fmt.Sprintf("%d", i),
				"skill_emit_via":                 "template",
				task.CreatedBySessionMetadataKey: req.SessionID,
			},
		}
		out, isNew, err := e.manager.CreateTaskIdempotent(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("emitter: create step %d: %w", i, err)
		}
		if isNew {
			createdCount++
		}
		created = append(created, out)
		stepID[i] = out.ID
	}

	// Wire depends_on edges. Skip when the dependency is out of range or
	// when both endpoints already existed (the dep was created previously).
	for i, step := range steps {
		for _, dep := range step.DependsOn {
			if int(dep) < 0 || int(dep) >= len(stepID) {
				e.logger.Debug("skill template dep out of range; skipping",
					zap.String("skill", req.Skill.Name),
					zap.Int("step", i),
					zap.Int32("dep", dep))
				continue
			}
			if dep == int32(i) {
				continue // self-dep
			}
			edge := &task.TaskDependency{
				FromTaskID: stepID[i],
				ToTaskID:   stepID[dep],
				Type:       loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS,
				CreatedBy:  req.AgentID,
				Metadata:   map[string]string{"emitted_by_skill": req.Skill.Name},
			}
			if err := e.manager.AddDependency(ctx, edge); err != nil {
				// Treat duplicate-edge errors as soft failures: an idempotent
				// re-emit of the same template will trip them harmlessly.
				if !isDuplicateDependency(err) {
					e.logger.Warn("skill template dep add failed",
						zap.String("skill", req.Skill.Name),
						zap.Int("step", i),
						zap.Int32("dep", dep),
						zap.Error(err))
				}
			}
		}
	}

	return &EmitResult{Tasks: created, CreatedCount: createdCount, Source: "template"}, nil
}

// emitDecomposed runs the LLM-driven decomposer against the skill prompt.
// Decomposer-emitted tasks are post-processed to attach the same
// SkillIdempotencyKey scheme so re-emission is safe.
func (e *Emitter) emitDecomposed(ctx context.Context, req EmitRequest) (*EmitResult, error) {
	if e.decomposer == nil {
		e.logger.Debug("skill has no template and emitter has no decomposer; skipping",
			zap.String("skill", req.Skill.Name))
		return &EmitResult{Source: "none"}, nil
	}
	if req.LLM == nil {
		e.logger.Debug("skill has no template and request has no LLM; skipping",
			zap.String("skill", req.Skill.Name))
		return &EmitResult{Source: "none"}, nil
	}

	// Idempotency for decomposed tasks: check if any task with our skill
	// origin key already exists; if so, do not re-decompose. This is a
	// "first activation wins" policy — coarser than per-step keying but
	// matches how decomposer output is non-deterministic across runs.
	probeKey := buildIdempotencyKey(req.Skill.Name, req.SessionID, -1)
	if existing, _, err := e.lookupExisting(ctx, probeKey); err == nil && existing != nil {
		// We already decomposed for this (skill, session). Return the
		// previously-emitted tasks scoped by their metadata.
		prior, err := e.collectPriorTasks(ctx, req.Skill.Name, req.SessionID)
		if err != nil {
			return nil, err
		}
		return &EmitResult{Tasks: prior, Source: "decomposer"}, nil
	}

	dreq := &task.DecomposeRequest{
		Goal:      buildDecomposeGoal(req.Skill),
		Context:   req.Skill.Description,
		BoardID:   req.BoardID,
		AgentID:   req.AgentID,
		MaxDepth:  2,
		Strategy:  loomv1.DecomposeStrategy_DECOMPOSE_STRATEGY_FORWARD,
		SessionID: req.SessionID,
	}
	resp, err := e.decomposer.Decompose(ctx, req.LLM, dreq)
	if err != nil {
		return nil, fmt.Errorf("emitter: decompose: %w", err)
	}
	if resp == nil || len(resp.Tasks) == 0 {
		return &EmitResult{Source: "decomposer"}, nil
	}

	cap := e.cap(0)
	tasks := resp.Tasks
	if len(tasks) > cap {
		e.logger.Debug("decomposer exceeded max_tasks cap; truncating",
			zap.String("skill", req.Skill.Name),
			zap.Int("returned", len(tasks)),
			zap.Int("cap", cap))
		tasks = tasks[:cap]
	}

	// Post-process: stamp each task with the per-step idempotency key plus
	// origin metadata. Decomposer.Decompose already created+stored them, so
	// we update each via the manager.
	createdCount := 0
	for i, t := range tasks {
		key := buildIdempotencyKey(req.Skill.Name, req.SessionID, i)
		if t.Metadata == nil {
			t.Metadata = map[string]string{}
		}
		t.Metadata["skill_name"] = req.Skill.Name
		t.Metadata["skill_session"] = req.SessionID
		t.Metadata["skill_step"] = fmt.Sprintf("%d", i)
		t.Metadata["skill_emit_via"] = "decomposer"
		t.Metadata[task.CreatedBySessionMetadataKey] = req.SessionID
		t.SkillIdempotencyKey = key
		if _, err := e.manager.UpdateTask(ctx, t, []string{"metadata", "skill_idempotency_key"}); err != nil {
			e.logger.Warn("emitter: failed to stamp decomposer output",
				zap.String("skill", req.Skill.Name),
				zap.String("task_id", t.ID),
				zap.Error(err))
			continue
		}
		createdCount++
	}

	// Plant the marker that the probe checked for. We do this at the end so
	// a partial emit doesn't trip the early-return guard.
	marker := &task.Task{
		Title:               fmt.Sprintf("%s — emission marker", req.Skill.Title),
		Description:         "Internal marker recording that the skill task emitter has run for this skill on this session. Hidden from board UIs.",
		Status:              loomv1.TaskStatus_TASK_STATUS_DONE,
		OwnerAgentID:        req.AgentID,
		BoardID:             req.BoardID,
		SkillIdempotencyKey: probeKey,
		Metadata: map[string]string{
			"skill_name":                     req.Skill.Name,
			"skill_session":                  req.SessionID,
			"skill_emit_via":                 "decomposer-marker",
			"hidden":                         "true",
			task.CreatedBySessionMetadataKey: req.SessionID,
		},
	}
	if _, _, err := e.manager.CreateTaskIdempotent(ctx, marker); err != nil {
		e.logger.Warn("emitter: marker write failed (idempotency may degrade)",
			zap.String("skill", req.Skill.Name),
			zap.Error(err))
	}

	return &EmitResult{
		Tasks:        tasks,
		CreatedCount: createdCount,
		Source:       "decomposer",
	}, nil
}

// lookupExisting wraps GetTaskByIdempotencyKey + isn't-found semantics for
// the decomposer's marker check.
// ensureBoard makes sure the named board exists before emission begins.
// Empty boardID is a valid "board-less" mode — tasks land with NULL board_id
// and are still queryable via owner. For a non-empty boardID we probe via
// GetBoard; if the board is missing (or the probe fails for any reason) we
// attempt to create it. A concurrent racing emitter that beats us to the
// create is handled by a second GetBoard probe so we don't surface the
// duplicate-key error as a Phase D failure.
//
// The auto-created board carries a generated name pointing at the skill that
// triggered creation, so operators inspecting `task_boards` can see where
// the row came from. Callers (agent.go Phase D) may pre-create the board
// with a curated name to override this default — the ensure is a safety net,
// not the documented public API for board provisioning.
func (e *Emitter) ensureBoard(ctx context.Context, boardID, skillName string) error {
	if boardID == "" {
		return nil
	}
	if _, err := e.manager.GetBoard(ctx, boardID); err == nil {
		return nil
	}
	name := fmt.Sprintf("auto-created for skill %q", skillName)
	if _, err := e.manager.CreateBoard(ctx, &task.TaskBoard{ID: boardID, Name: name}); err != nil {
		// Another goroutine may have created the board between our probe
		// and our create. One more lookup decides which way that race went.
		if _, gerr := e.manager.GetBoard(ctx, boardID); gerr == nil {
			return nil
		}
		return err
	}
	e.logger.Info("emitter: auto-created board for skill task emission",
		zap.String("board_id", boardID),
		zap.String("skill", skillName))
	return nil
}

func (e *Emitter) lookupExisting(ctx context.Context, key string) (*task.Task, bool, error) {
	if key == "" {
		return nil, false, nil
	}
	t, err := e.manager.GetTaskByIdempotencyKey(ctx, key)
	if err != nil {
		return nil, false, err
	}
	return t, t != nil, nil
}

// collectPriorTasks returns prior tasks emitted by the decomposer for this
// (skill, session) tuple, looked up by their per-step idempotency keys.
// The marker task itself is excluded.
func (e *Emitter) collectPriorTasks(ctx context.Context, skill, session string) ([]*task.Task, error) {
	out := []*task.Task{}
	for i := 0; i < e.maxTasks; i++ {
		key := buildIdempotencyKey(skill, session, i)
		t, err := e.manager.GetTaskByIdempotencyKey(ctx, key)
		if err != nil {
			return nil, err
		}
		if t == nil {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// cap chooses the effective task cap for one activation, taking the smaller
// of the template's MaxTasks override and the emitter's configured limit.
// MaxTasks=0 means "use emitter default."
func (e *Emitter) cap(tmplMax int32) int {
	cap := e.maxTasks
	if cap <= 0 {
		cap = DefaultMaxTasks
	}
	if tmplMax > 0 && int(tmplMax) < cap {
		cap = int(tmplMax)
	}
	return cap
}

// buildIdempotencyKey is the canonical formatter for SkillIdempotencyKey.
// Step index of -1 produces the per-(skill, session) marker key used by
// the decomposer fallback to dedupe across runs.
func buildIdempotencyKey(skillName, sessionID string, stepIndex int) string {
	if stepIndex < 0 {
		return fmt.Sprintf("skill:%s|sess:%s|marker", skillName, sessionID)
	}
	return fmt.Sprintf("skill:%s|sess:%s|step:%d", skillName, sessionID, stepIndex)
}

// buildDecomposeGoal turns the skill's prompt into a goal sentence for the
// decomposer. The decomposer's prompt template will append its own
// breakdown instructions; we just provide the high-level objective.
func buildDecomposeGoal(s *skills.Skill) string {
	title := s.Title
	if title == "" {
		title = s.Name
	}
	instructions := strings.TrimSpace(s.Prompt.Instructions)
	if instructions == "" {
		return fmt.Sprintf("Execute the %q skill.", title)
	}
	return fmt.Sprintf("Skill %q: %s", title, truncate(instructions, 800))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func isDuplicateDependency(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE") ||
		strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "already exists")
}
