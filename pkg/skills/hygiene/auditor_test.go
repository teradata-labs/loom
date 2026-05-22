// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package hygiene

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/task"
)

// stubLister returns canned tasks per (skillName, sessionID). Tests
// construct one before each call to Audit.
type stubLister struct {
	byKey map[string][]*task.Task
	err   error
}

func (s *stubLister) ListBySkillRun(_ context.Context, skillName, sessionID string) ([]*task.Task, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.byKey[skillName+"|"+sessionID], nil
}

// stubActiveSrc returns canned active skills per session.
type stubActiveSrc struct {
	bySession map[string][]*skills.ActiveSkill
}

func (s *stubActiveSrc) GetActiveSkills(sessionID string) []*skills.ActiveSkill {
	return s.bySession[sessionID]
}

// active is a small helper that builds an ActiveSkill stub with just the
// fields the auditor reads.
func active(name string) *skills.ActiveSkill {
	return &skills.ActiveSkill{Skill: &skills.Skill{Name: name}}
}

// tcase is one row of the table for TestAuditor_Audit.
type tcase struct {
	name           string
	activeSkills   []*skills.ActiveSkill
	tasksBySkill   map[string][]*task.Task
	wantViolations int
	wantByKind     map[string]int
}

func TestAuditor_Audit(t *testing.T) {
	now := time.Now()
	claimed := now.Add(-1 * time.Minute)

	cases := []tcase{
		{
			name:           "no active skills returns clean report",
			activeSkills:   nil,
			tasksBySkill:   nil,
			wantViolations: 0,
			wantByKind: map[string]int{
				ViolationInProgressOrphan.String(): 0,
				ViolationBlockedNoHITL.String():    0,
				ViolationOpenUnstarted.String():    0,
			},
		},
		{
			name:         "all tasks DONE -> clean",
			activeSkills: []*skills.ActiveSkill{active("sql")},
			tasksBySkill: map[string][]*task.Task{
				"sql|s1": {
					{ID: "t1", Status: loomv1.TaskStatus_TASK_STATUS_DONE},
				},
			},
			wantViolations: 0,
		},
		{
			name:         "IN_PROGRESS orphan flagged",
			activeSkills: []*skills.ActiveSkill{active("sql")},
			tasksBySkill: map[string][]*task.Task{
				"sql|s1": {
					{ID: "t1", Title: "tune query", Status: loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS, ClaimedAt: &claimed},
				},
			},
			wantViolations: 1,
			wantByKind: map[string]int{
				ViolationInProgressOrphan.String(): 1,
				ViolationBlockedNoHITL.String():    0,
				ViolationOpenUnstarted.String():    0,
			},
		},
		{
			name:         "BLOCKED with no HITL flagged",
			activeSkills: []*skills.ActiveSkill{active("sql")},
			tasksBySkill: map[string][]*task.Task{
				"sql|s1": {
					{ID: "t1", Title: "tune query", Status: loomv1.TaskStatus_TASK_STATUS_BLOCKED, Notes: "need DBA cred"},
				},
			},
			wantViolations: 1,
			wantByKind: map[string]int{
				ViolationInProgressOrphan.String(): 0,
				ViolationBlockedNoHITL.String():    1,
				ViolationOpenUnstarted.String():    0,
			},
		},
		{
			name:         "OPEN with no ClaimedAt flagged as unstarted",
			activeSkills: []*skills.ActiveSkill{active("sql")},
			tasksBySkill: map[string][]*task.Task{
				"sql|s1": {
					{ID: "t1", Title: "design index", Status: loomv1.TaskStatus_TASK_STATUS_OPEN},
				},
			},
			wantViolations: 1,
			wantByKind: map[string]int{
				ViolationInProgressOrphan.String(): 0,
				ViolationBlockedNoHITL.String():    0,
				ViolationOpenUnstarted.String():    1,
			},
		},
		{
			name:         "OPEN that was claimed-then-released is NOT a violation",
			activeSkills: []*skills.ActiveSkill{active("sql")},
			tasksBySkill: map[string][]*task.Task{
				"sql|s1": {
					{ID: "t1", Status: loomv1.TaskStatus_TASK_STATUS_OPEN, ClaimedAt: &claimed},
				},
			},
			wantViolations: 0,
		},
		{
			name: "mixed: two skills, each contributes violations",
			activeSkills: []*skills.ActiveSkill{
				active("sql"),
				active("docs"),
			},
			tasksBySkill: map[string][]*task.Task{
				"sql|s1": {
					{ID: "t1", Status: loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS, ClaimedAt: &claimed},
					{ID: "t2", Status: loomv1.TaskStatus_TASK_STATUS_DONE},
				},
				"docs|s1": {
					{ID: "t3", Status: loomv1.TaskStatus_TASK_STATUS_OPEN},
					{ID: "t4", Status: loomv1.TaskStatus_TASK_STATUS_BLOCKED},
				},
			},
			wantViolations: 3,
			wantByKind: map[string]int{
				ViolationInProgressOrphan.String(): 1,
				ViolationBlockedNoHITL.String():    1,
				ViolationOpenUnstarted.String():    1,
			},
		},
		{
			name:         "DEFERRED and CANCELLED are healthy terminal states",
			activeSkills: []*skills.ActiveSkill{active("sql")},
			tasksBySkill: map[string][]*task.Task{
				"sql|s1": {
					{ID: "t1", Status: loomv1.TaskStatus_TASK_STATUS_DEFERRED},
					{ID: "t2", Status: loomv1.TaskStatus_TASK_STATUS_CANCELLED},
				},
			},
			wantViolations: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			auditor := NewAuditor(
				&stubLister{byKey: tc.tasksBySkill},
				&stubActiveSrc{bySession: map[string][]*skills.ActiveSkill{"s1": tc.activeSkills}},
			)

			report, err := auditor.Audit(context.Background(), "s1", nil)
			require.NoError(t, err)
			require.NotNil(t, report)
			assert.Equal(t, "s1", report.SessionID)
			assert.Len(t, report.Violations, tc.wantViolations)

			if tc.wantByKind != nil {
				assert.Equal(t, tc.wantByKind, report.CountByKind())
			}
		})
	}
}

func TestAuditor_Audit_ListerError(t *testing.T) {
	t.Parallel()

	listerErr := errors.New("simulated store failure")
	auditor := NewAuditor(
		&stubLister{err: listerErr},
		&stubActiveSrc{bySession: map[string][]*skills.ActiveSkill{"s1": {active("sql")}}},
	)

	_, err := auditor.Audit(context.Background(), "s1", nil)
	require.Error(t, err, "lister errors must propagate so the caller can decide policy")
	assert.ErrorIs(t, err, listerErr)
}

func TestAuditor_ResolvePolicyAndMaxRetries(t *testing.T) {
	t.Parallel()

	auditor := NewAuditor(
		&stubLister{},
		&stubActiveSrc{},
	)

	// nil config -> defaults.
	assert.Equal(t, loomv1.HygienePolicy_HYGIENE_POLICY_REQUIRE_FIX, auditor.ResolvePolicy(nil))
	assert.Equal(t, DefaultMaxRetries, auditor.ResolveMaxRetries(nil))
	assert.True(t, auditor.Enabled(nil), "nil config must default to enabled")

	// Explicit policy + max_retries override.
	cfg := &loomv1.HygieneConfig{
		Policy:     loomv1.HygienePolicy_HYGIENE_POLICY_AUTO_FIX,
		MaxRetries: 5,
	}
	assert.Equal(t, loomv1.HygienePolicy_HYGIENE_POLICY_AUTO_FIX, auditor.ResolvePolicy(cfg))
	assert.Equal(t, 5, auditor.ResolveMaxRetries(cfg))

	// Explicit enabled=false.
	off := false
	cfg.Enabled = &off
	assert.False(t, auditor.Enabled(cfg), "explicit enabled=false must disable hygiene")

	// UNSPECIFIED policy falls back to default.
	cfg2 := &loomv1.HygieneConfig{Policy: loomv1.HygienePolicy_HYGIENE_POLICY_UNSPECIFIED}
	assert.Equal(t, loomv1.HygienePolicy_HYGIENE_POLICY_REQUIRE_FIX, auditor.ResolvePolicy(cfg2))

	// max_retries <= 0 falls back to default.
	cfg3 := &loomv1.HygieneConfig{MaxRetries: 0}
	assert.Equal(t, DefaultMaxRetries, auditor.ResolveMaxRetries(cfg3))
	cfg3.MaxRetries = -1
	assert.Equal(t, DefaultMaxRetries, auditor.ResolveMaxRetries(cfg3))
}

func TestAuditor_NewAuditor_PanicsOnNil(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		NewAuditor(nil, &stubActiveSrc{})
	})
	assert.Panics(t, func() {
		NewAuditor(&stubLister{}, nil)
	})
}
