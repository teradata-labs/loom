// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package hygiene

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/task"
)

// fakeMutator records every transition and update so tests can assert
// what the enforcer did. Goroutine-safe.
type fakeMutator struct {
	mu          sync.Mutex
	transitions []transitionCall
	updates     []*task.Task
}

type transitionCall struct {
	taskID    string
	newStatus loomv1.TaskStatus
}

func (f *fakeMutator) TransitionTask(_ context.Context, taskID string, newStatus loomv1.TaskStatus) (*task.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitions = append(f.transitions, transitionCall{taskID: taskID, newStatus: newStatus})
	return &task.Task{ID: taskID, Status: newStatus}, nil
}

func (f *fakeMutator) UpdateTask(_ context.Context, t *task.Task, _ []string) (*task.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Snapshot the task so later mutations don't race with assertions.
	clone := *t
	f.updates = append(f.updates, &clone)
	return &clone, nil
}

// fakeSpawner records every HITL request the enforcer would have spawned.
type fakeSpawner struct {
	mu    sync.Mutex
	calls []hitlCall
	err   error
}

type hitlCall struct {
	sessionID string
	agentID   string
	question  string
	taskID    string
}

func (f *fakeSpawner) SpawnHITL(_ context.Context, sessionID, agentID, question string, t *task.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, hitlCall{
		sessionID: sessionID,
		agentID:   agentID,
		question:  question,
		taskID:    t.ID,
	})
	return nil
}

func sampleReport(policy loomv1.HygienePolicy) *Report {
	return &Report{
		SessionID: "s1",
		Policy:    policy,
		Violations: []Violation{
			{SkillName: "sql", Kind: ViolationOpenUnstarted, Task: &task.Task{ID: "t-open", Title: "design index"}},
			{SkillName: "sql", Kind: ViolationInProgressOrphan, Task: &task.Task{ID: "t-ip", Title: "tune query"}},
			{SkillName: "sql", Kind: ViolationBlockedNoHITL, Task: &task.Task{ID: "t-blk", Title: "patch schema"}, Reason: "missing DBA cred"},
		},
	}
}

func TestEnforcer_NilOrEmptyReport_NoOp(t *testing.T) {
	t.Parallel()
	mut := &fakeMutator{}
	e := NewEnforcer(mut)
	out, err := e.Enforce(context.Background(), nil, 0, 2)
	require.NoError(t, err)
	assert.Equal(t, 0, out.ViolationsFound)
	assert.Empty(t, mut.transitions)

	out, err = e.Enforce(context.Background(), &Report{SessionID: "s1"}, 0, 2)
	require.NoError(t, err)
	assert.Equal(t, 0, out.ViolationsFound)
	assert.False(t, out.ShouldRetry)
}

func TestEnforcer_RequireFix_InjectsAndAsksToRetry(t *testing.T) {
	t.Parallel()
	mut := &fakeMutator{}
	e := NewEnforcer(mut)

	report := sampleReport(loomv1.HygienePolicy_HYGIENE_POLICY_REQUIRE_FIX)
	out, err := e.Enforce(context.Background(), report, 0, 2)
	require.NoError(t, err)
	assert.Equal(t, 3, out.ViolationsFound)
	assert.True(t, out.ShouldRetry)
	assert.NotEmpty(t, out.InjectionMessage, "REQUIRE_FIX must produce an injection message")
	assert.Contains(t, out.InjectionMessage, "design index")
	assert.Contains(t, out.InjectionMessage, "tune query")
	assert.Contains(t, out.InjectionMessage, "patch schema")
	assert.Empty(t, mut.transitions, "REQUIRE_FIX must not mutate task state itself")
}

func TestEnforcer_RequireFix_FallsThroughToAutoFix_AtCap(t *testing.T) {
	t.Parallel()
	mut := &fakeMutator{}
	spawner := &fakeSpawner{}
	e := NewEnforcer(mut, WithHITLSpawner(spawner), WithAgentID("agent-1"))

	report := sampleReport(loomv1.HygienePolicy_HYGIENE_POLICY_REQUIRE_FIX)
	out, err := e.Enforce(context.Background(), report, 2, 2)
	require.NoError(t, err)
	assert.NotEmpty(t, out.FallthroughReason, "at-cap REQUIRE_FIX must record fallthrough reason")
	assert.False(t, out.ShouldRetry, "fallthrough must not request another retry")

	// AUTO_FIX should have transitioned OPEN->DEFERRED and IN_PROGRESS->OPEN
	// and spawned exactly one HITL for the BLOCKED task.
	assert.Equal(t, 2, out.Resolved, "two tasks transitioned")
	assert.Equal(t, 1, out.HITLSpawned, "one BLOCKED task surfaced as HITL")

	// Assert the specific transitions happened.
	transitionsByID := map[string]loomv1.TaskStatus{}
	for _, c := range mut.transitions {
		transitionsByID[c.taskID] = c.newStatus
	}
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_DEFERRED, transitionsByID["t-open"])
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_OPEN, transitionsByID["t-ip"])

	require.Len(t, spawner.calls, 1)
	assert.Equal(t, "t-blk", spawner.calls[0].taskID)
	assert.Equal(t, "agent-1", spawner.calls[0].agentID)
	assert.Contains(t, spawner.calls[0].question, "missing DBA cred")
}

func TestEnforcer_AutoFix_MutatesAndSpawnsHITL(t *testing.T) {
	t.Parallel()
	mut := &fakeMutator{}
	spawner := &fakeSpawner{}
	e := NewEnforcer(mut, WithHITLSpawner(spawner))

	report := sampleReport(loomv1.HygienePolicy_HYGIENE_POLICY_AUTO_FIX)
	out, err := e.Enforce(context.Background(), report, 0, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, out.Resolved)
	assert.Equal(t, 1, out.HITLSpawned)
	assert.False(t, out.ShouldRetry)
	assert.Empty(t, out.InjectionMessage)

	// Each mutated task got a hygiene note appended via UpdateTask.
	assert.Len(t, mut.updates, 2, "AUTO_FIX must annotate every transitioned task")
	for _, upd := range mut.updates {
		assert.Contains(t, upd.Notes, "[hygiene]", "auto-fix notes must be prefixed with [hygiene] for audit clarity")
	}
}

func TestEnforcer_AutoFix_BlockedWithoutSpawner_LogsAndContinues(t *testing.T) {
	t.Parallel()
	mut := &fakeMutator{}
	// No HITL spawner wired.
	e := NewEnforcer(mut)

	report := &Report{
		SessionID: "s1",
		Policy:    loomv1.HygienePolicy_HYGIENE_POLICY_AUTO_FIX,
		Violations: []Violation{
			{SkillName: "sql", Kind: ViolationBlockedNoHITL, Task: &task.Task{ID: "t-blk", Title: "x"}},
		},
	}
	out, err := e.Enforce(context.Background(), report, 0, 2)
	require.NoError(t, err, "missing spawner must not be fatal under AUTO_FIX")
	assert.Equal(t, 0, out.HITLSpawned)
	assert.Empty(t, mut.transitions, "BLOCKED tasks aren't transitioned by AUTO_FIX")
}

func TestEnforcer_WarnOnly_NoMutation(t *testing.T) {
	t.Parallel()
	mut := &fakeMutator{}
	spawner := &fakeSpawner{}
	e := NewEnforcer(mut, WithHITLSpawner(spawner))

	report := sampleReport(loomv1.HygienePolicy_HYGIENE_POLICY_WARN_ONLY)
	out, err := e.Enforce(context.Background(), report, 0, 2)
	require.NoError(t, err)
	assert.Equal(t, 3, out.ViolationsFound)
	assert.Equal(t, 0, out.Resolved)
	assert.Equal(t, 0, out.HITLSpawned)
	assert.False(t, out.ShouldRetry)
	assert.Empty(t, mut.transitions, "WARN_ONLY must not mutate state")
	assert.Empty(t, spawner.calls, "WARN_ONLY must not spawn HITL")
}

func TestReport_FormatToolMessage(t *testing.T) {
	t.Parallel()
	// Empty report yields empty message.
	empty := &Report{}
	assert.Empty(t, empty.FormatToolMessage())

	// Multi-skill report sorts skills alphabetically; each section
	// includes the three required action lines.
	r := &Report{
		Violations: []Violation{
			{SkillName: "zsk", Kind: ViolationOpenUnstarted, Task: &task.Task{ID: "z1", Title: "z-title"}},
			{SkillName: "ask", Kind: ViolationInProgressOrphan, Task: &task.Task{ID: "a1", Title: "a-title"}},
		},
	}
	msg := r.FormatToolMessage()
	assert.Contains(t, msg, "Skill: ask")
	assert.Contains(t, msg, "Skill: zsk")
	// "ask" must appear before "zsk".
	askIdx := assertContainsBefore(t, msg, "Skill: ask", "Skill: zsk")
	assert.GreaterOrEqual(t, askIdx, 0)
	assert.Contains(t, msg, "Action required")
	assert.Contains(t, msg, "Do not silently end the turn")
}

// assertContainsBefore returns the index of `first` in `s` and fails the
// test if either substring is missing or if `first` does not appear before
// `second`.
func assertContainsBefore(t *testing.T, s, first, second string) int {
	t.Helper()
	i := indexOf(s, first)
	j := indexOf(s, second)
	require.NotEqual(t, -1, i, "missing substring %q", first)
	require.NotEqual(t, -1, j, "missing substring %q", second)
	assert.Less(t, i, j, "expected %q to appear before %q", first, second)
	return i
}

// indexOf is a tiny strings.Index without importing strings into the test
// file twice — keeps the test focused on the subject matter.
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
