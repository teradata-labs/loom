// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package tasks

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	sqlitestore "github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/types"
)

func newTestManager(t *testing.T) *task.Manager {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mig, err := sqlitestore.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, mig.MigrateUp(context.Background()))

	store := sqlitestore.NewTaskStore(db, observability.NewNoOpTracer())
	return task.NewManager(store, nil, nil, nil)
}

func TestEmit_TasksDisabled_NoOp(t *testing.T) {
	m := newTestManager(t)
	e := NewEmitter(m, nil)

	skill := &skills.Skill{Name: "x", Title: "X", Prompt: skills.SkillPrompt{Instructions: "do x"}}
	res, err := e.EmitForActivation(context.Background(), EmitRequest{
		Skill:             skill,
		SessionID:         "s",
		AgentID:           "a",
		AgentTasksEnabled: false, // master switch off
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Empty(t, res.Tasks)
	assert.Equal(t, 0, res.CreatedCount)
	assert.Equal(t, "none", res.Source)
}

func TestEmit_PerSkillEmitTasksFalse_NoOp(t *testing.T) {
	m := newTestManager(t)
	e := NewEmitter(m, nil)

	disable := false
	skill := &skills.Skill{
		Name:      "x",
		EmitTasks: &disable, // explicit opt-out per-skill
	}
	res, err := e.EmitForActivation(context.Background(), EmitRequest{
		Skill:             skill,
		AgentTasksEnabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "none", res.Source)
	assert.Empty(t, res.Tasks)
}

func TestEmit_TemplatePath_MaterializesStepsWithDeps(t *testing.T) {
	m := newTestManager(t)
	e := NewEmitter(m, nil)

	skill := &skills.Skill{
		Name:  "sql-opt",
		Title: "SQL Optimization",
		TaskTemplate: &skills.SkillTaskTemplate{
			Steps: []skills.SkillTaskStep{
				{ID: "analyze", Title: "Analyze", Objective: "find issues", Category: "analysis", Priority: "P1"},
				{ID: "fix", Title: "Fix", Objective: "apply changes", Priority: "P1", DependsOnIDs: []string{"analyze"}},
			},
		},
	}
	ctx := context.Background()
	res, err := e.EmitForActivation(ctx, EmitRequest{
		Skill:             skill,
		SessionID:         "s1",
		AgentID:           "agent",
		AgentTasksEnabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, "template", res.Source)
	require.Len(t, res.Tasks, 2)
	assert.Equal(t, 2, res.CreatedCount)

	// Step keys are stable across re-emits.
	for i, tk := range res.Tasks {
		assert.NotEmpty(t, tk.SkillIdempotencyKey)
		assert.Contains(t, tk.SkillIdempotencyKey, "skill:sql-opt")
		assert.Contains(t, tk.SkillIdempotencyKey, "sess:s1")
		_ = i
	}

	// Re-emit must not create duplicates.
	res2, err := e.EmitForActivation(ctx, EmitRequest{
		Skill:             skill,
		SessionID:         "s1",
		AgentID:           "agent",
		AgentTasksEnabled: true,
	})
	require.NoError(t, err)
	require.Len(t, res2.Tasks, 2, "idempotent re-emit must return same task count")
	assert.Equal(t, 0, res2.CreatedCount, "no new rows created on re-emit")
	assert.Equal(t, res.Tasks[0].ID, res2.Tasks[0].ID,
		"re-emit must return the same task IDs")

	// Dep edge present: step 1 depends on step 0.
	deps, err := m.Store().GetDependencies(ctx, res.Tasks[1].ID)
	require.NoError(t, err)
	require.NotEmpty(t, deps)
	assert.Equal(t, res.Tasks[0].ID, deps[0].ToTaskID)
}

func TestEmit_TemplatePath_MaxTasksCap(t *testing.T) {
	m := newTestManager(t)
	e := NewEmitter(m, nil)

	steps := make([]skills.SkillTaskStep, 12)
	for i := range steps {
		steps[i] = skills.SkillTaskStep{Title: "step", Objective: "do thing"}
	}
	skill := &skills.Skill{
		Name: "x",
		TaskTemplate: &skills.SkillTaskTemplate{
			Steps:    steps,
			MaxTasks: 3,
		},
	}
	res, err := e.EmitForActivation(context.Background(), EmitRequest{
		Skill:             skill,
		SessionID:         "s",
		AgentID:           "a",
		AgentTasksEnabled: true,
	})
	require.NoError(t, err)
	assert.Len(t, res.Tasks, 3, "MaxTasks must cap output")
}

func TestEmit_DecomposerFallback(t *testing.T) {
	m := newTestManager(t)
	dec := task.NewDecomposer(m, nil, nil)
	e := NewEmitter(m, dec)

	scripted := newScriptedLLM([]string{`{
		"tasks": [
			{"index": 0, "title": "Step A", "description": "do a", "objective": "out a", "acceptance_criteria": "a done", "category": "research", "priority": "P2", "estimated_effort": "10m", "depends_on": [], "tags": []},
			{"index": 1, "title": "Step B", "description": "do b", "objective": "out b", "acceptance_criteria": "b done", "category": "implementation", "priority": "P2", "estimated_effort": "15m", "depends_on": [0], "tags": []}
		],
		"reasoning": "two steps"
	}`})

	skill := &skills.Skill{
		Name:   "research-skill",
		Title:  "Research",
		Prompt: skills.SkillPrompt{Instructions: "investigate the topic and report findings"},
	}
	ctx := context.Background()
	res, err := e.EmitForActivation(ctx, EmitRequest{
		Skill:             skill,
		SessionID:         "s",
		AgentID:           "a",
		LLM:               scripted,
		AgentTasksEnabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, "decomposer", res.Source)
	require.Len(t, res.Tasks, 2)
	assert.Equal(t, 2, res.CreatedCount)

	// Re-emit short-circuits via the marker — same scripted LLM gives the
	// same payload, but we should not re-run decomposition.
	startCalls := scripted.calls
	res2, err := e.EmitForActivation(ctx, EmitRequest{
		Skill:             skill,
		SessionID:         "s",
		AgentID:           "a",
		LLM:               scripted,
		AgentTasksEnabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "decomposer", res2.Source)
	assert.Equal(t, startCalls, scripted.calls,
		"re-emit must not call the LLM again — marker should short-circuit")
	assert.Len(t, res2.Tasks, 2)
}

func TestEmit_DecomposerWithoutLLM_NoOp(t *testing.T) {
	m := newTestManager(t)
	dec := task.NewDecomposer(m, nil, nil)
	e := NewEmitter(m, dec)

	skill := &skills.Skill{
		Name:   "needs-llm",
		Prompt: skills.SkillPrompt{Instructions: "do something"},
		// No TaskTemplate.
	}
	res, err := e.EmitForActivation(context.Background(), EmitRequest{
		Skill:             skill,
		AgentTasksEnabled: true,
		// Note: no LLM passed.
	})
	require.NoError(t, err)
	assert.Equal(t, "none", res.Source)
	assert.Empty(t, res.Tasks)
}

func TestEmit_ConcurrentActivation_Idempotent(t *testing.T) {
	m := newTestManager(t)
	e := NewEmitter(m, nil)

	skill := &skills.Skill{
		Name: "concur",
		TaskTemplate: &skills.SkillTaskTemplate{
			Steps: []skills.SkillTaskStep{{Title: "only step"}},
		},
	}
	req := EmitRequest{
		Skill:             skill,
		SessionID:         "race-sess",
		AgentID:           "agent",
		AgentTasksEnabled: true,
	}

	const N = 8
	var wg sync.WaitGroup
	results := make(chan *EmitResult, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, _ := e.EmitForActivation(context.Background(), req)
			if res != nil {
				results <- res
			}
		}()
	}
	wg.Wait()
	close(results)

	totalCreated := 0
	for r := range results {
		totalCreated += r.CreatedCount
	}
	assert.LessOrEqual(t, totalCreated, 1,
		"concurrent activations of the same (skill, session) must yield at most 1 new task")
}

// =============================================================================
// LLM stub
// =============================================================================

type scriptedLLM struct {
	mu       sync.Mutex
	calls    int
	scripted []string
}

func newScriptedLLM(responses []string) *scriptedLLM { return &scriptedLLM{scripted: responses} }

func (s *scriptedLLM) Chat(_ context.Context, _ []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls >= len(s.scripted) {
		s.calls++
		if len(s.scripted) > 0 {
			return &types.LLMResponse{Content: s.scripted[len(s.scripted)-1]}, nil
		}
		return &types.LLMResponse{}, nil
	}
	resp := s.scripted[s.calls]
	s.calls++
	return &types.LLMResponse{Content: resp}, nil
}

func (s *scriptedLLM) Name() string  { return "scripted" }
func (s *scriptedLLM) Model() string { return "scripted-model" }

var _ types.LLMProvider = (*scriptedLLM)(nil)
var _ loomv1.TaskStatus // keep import

// helper kept compatible across go versions

// TestEmit_AutoCreatesReferencedBoard guards against the silent FK-failure
// path that turned Phase D into a no-op when SkillsConfig.SkillTaskBoardID
// (or MemoryConfig.TaskBoard.DefaultBoardID) named a board that hadn't been
// pre-created. The emitter now ensures the board exists before any
// CreateTask call, so emission lands instead of silently dying inside a
// `FOREIGN KEY constraint failed` swallowed by a Nop logger.
func TestEmit_AutoCreatesReferencedBoard(t *testing.T) {
	m := newTestManager(t)
	e := NewEmitter(m, nil)
	ctx := context.Background()

	skill := &skills.Skill{
		Name:  "release-audit",
		Title: "Release Audit",
		TaskTemplate: &skills.SkillTaskTemplate{
			Steps: []skills.SkillTaskStep{
				{ID: "step-zero", Title: "step zero", Category: "review"},
				{ID: "step-one", Title: "step one", Category: "review", DependsOnIDs: []string{"step-zero"}},
			},
		},
	}

	// Sanity: the board does not exist yet.
	_, err := m.GetBoard(ctx, "ephemeral-board")
	require.Error(t, err, "board must not exist before emission")

	res, err := e.EmitForActivation(ctx, EmitRequest{
		Skill:             skill,
		SessionID:         "sess-1",
		AgentID:           "agent-1",
		BoardID:           "ephemeral-board",
		AgentTasksEnabled: true,
	})
	require.NoError(t, err, "emission must succeed instead of FK-failing")
	require.NotNil(t, res)
	assert.Len(t, res.Tasks, 2, "both template steps must materialize")
	assert.Equal(t, 2, res.CreatedCount)
	assert.Equal(t, "template", res.Source)

	// Verify the board was auto-created with a name that points at the skill.
	board, err := m.GetBoard(ctx, "ephemeral-board")
	require.NoError(t, err, "ensureBoard must persist the board")
	require.NotNil(t, board)
	assert.Equal(t, "ephemeral-board", board.ID)
	assert.Contains(t, board.Name, "release-audit",
		"auto-created board name must reference the originating skill")

	// And the tasks actually landed on that board.
	for _, tk := range res.Tasks {
		assert.Equal(t, "ephemeral-board", tk.BoardID, "task must attach to the ensured board")
	}
}

// TestEmit_PreexistingBoardNotOverwritten confirms ensureBoard is idempotent:
// when the named board already exists (e.g. because an operator pre-created
// it via TaskService.CreateBoard with a curated name), emission must use the
// existing board rather than overwriting it.
func TestEmit_PreexistingBoardNotOverwritten(t *testing.T) {
	m := newTestManager(t)
	e := NewEmitter(m, nil)
	ctx := context.Background()

	preCreated, err := m.CreateBoard(ctx, &task.TaskBoard{
		ID:   "curated-board",
		Name: "Operator-curated name",
	})
	require.NoError(t, err)
	require.Equal(t, "curated-board", preCreated.ID)

	skill := &skills.Skill{
		Name:  "release-audit",
		Title: "Release Audit",
		TaskTemplate: &skills.SkillTaskTemplate{
			Steps: []skills.SkillTaskStep{{Title: "only step"}},
		},
	}
	_, err = e.EmitForActivation(ctx, EmitRequest{
		Skill:             skill,
		SessionID:         "s",
		AgentID:           "a",
		BoardID:           "curated-board",
		AgentTasksEnabled: true,
	})
	require.NoError(t, err)

	got, err := m.GetBoard(ctx, "curated-board")
	require.NoError(t, err)
	assert.Equal(t, "Operator-curated name", got.Name,
		"emitter must not overwrite an existing board's curated name")
}
