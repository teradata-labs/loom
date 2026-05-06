// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package skills

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// activeSkillNamesForSession returns the set of skill names currently
// active for a session, for assertion convenience.
func activeSkillNamesForSession(o *Orchestrator, sessionID string) []string {
	out := []string{}
	for _, as := range o.GetActiveSkills(sessionID) {
		out = append(out, as.Skill.Name)
	}
	return out
}

func TestActivateSkill_NonStickyEvictedAtCap(t *testing.T) {
	o := NewOrchestrator(NewLibrary(), WithMaxConcurrentSkills(2))

	skillA := &Skill{Name: "a"}
	skillB := &Skill{Name: "b"}
	skillC := &Skill{Name: "c"}

	o.ActivateSkill("s", skillA, "test", "", 0.5)
	o.ActivateSkill("s", skillB, "test", "", 0.7)
	o.ActivateSkill("s", skillC, "test", "", 0.9) // forces eviction

	names := activeSkillNamesForSession(o, "s")
	assert.Len(t, names, 2,
		"max concurrent must cap the active set")
	// Lowest-confidence non-sticky (a, conf 0.5) should be evicted.
	assert.NotContains(t, names, "a")
	assert.Contains(t, names, "b")
	assert.Contains(t, names, "c")
}

func TestActivateSkill_StickyFlagPreservesSkill(t *testing.T) {
	// Skill 'a' has Skill.Sticky=true even though it has the lowest
	// confidence; eviction must skip it and pick the next-lowest.
	o := NewOrchestrator(NewLibrary(), WithMaxConcurrentSkills(2))

	stickySkill := &Skill{Name: "sticky", Sticky: true}
	skillB := &Skill{Name: "b"}
	skillC := &Skill{Name: "c"}

	o.ActivateSkill("s", stickySkill, "test", "", 0.1)
	o.ActivateSkill("s", skillB, "test", "", 0.6)
	o.ActivateSkill("s", skillC, "test", "", 0.9)

	names := activeSkillNamesForSession(o, "s")
	require.Len(t, names, 2)
	assert.Contains(t, names, "sticky",
		"Skill.Sticky=true must preserve the skill across eviction")
	assert.Contains(t, names, "c")
	assert.NotContains(t, names, "b",
		"the next-lowest non-sticky skill should have been evicted")
}

func TestActivateSkill_StickinessCheckerKeepsSkillActive(t *testing.T) {
	// The checker reports skill 'a' as sticky because it has open tasks.
	// Eviction must respect the checker even when Sticky flag is false.
	checker := func(name, _ string) bool {
		return name == "a"
	}
	o := NewOrchestrator(NewLibrary(),
		WithMaxConcurrentSkills(2),
		WithStickinessChecker(checker),
	)

	o.ActivateSkill("s", &Skill{Name: "a"}, "test", "", 0.1)
	o.ActivateSkill("s", &Skill{Name: "b"}, "test", "", 0.6)
	o.ActivateSkill("s", &Skill{Name: "c"}, "test", "", 0.9)

	names := activeSkillNamesForSession(o, "s")
	require.Len(t, names, 2)
	assert.Contains(t, names, "a",
		"checker-sticky skill must survive eviction")
}

func TestActivateSkill_AllStickyOverflows(t *testing.T) {
	// When every active skill is sticky, the cap is allowed to overflow
	// rather than abandoning load-bearing work.
	checker := func(string, string) bool { return true }
	o := NewOrchestrator(NewLibrary(),
		WithMaxConcurrentSkills(2),
		WithStickinessChecker(checker),
	)

	o.ActivateSkill("s", &Skill{Name: "a"}, "test", "", 0.1)
	o.ActivateSkill("s", &Skill{Name: "b"}, "test", "", 0.5)
	o.ActivateSkill("s", &Skill{Name: "c"}, "test", "", 0.9)

	names := activeSkillNamesForSession(o, "s")
	assert.Len(t, names, 3,
		"all-sticky case must overflow the cap rather than evict")
	assert.Contains(t, names, "a")
	assert.Contains(t, names, "b")
	assert.Contains(t, names, "c")
}

func TestActivateSkill_DefaultCapWhenZero(t *testing.T) {
	// MaxConcurrentSkills not set -> falls back to legacy default (3).
	o := NewOrchestrator(NewLibrary())

	for i, name := range []string{"a", "b", "c", "d", "e"} {
		o.ActivateSkill("s", &Skill{Name: name}, "test", "", float64(i)/10.0)
	}

	assert.Len(t, activeSkillNamesForSession(o, "s"), 3,
		"default cap of 3 must apply when WithMaxConcurrentSkills is not set")
}

func TestSetStickinessChecker_RuntimeUpdate(t *testing.T) {
	// The checker can be installed after construction (used by the agent
	// layer where the task manager is only available post-NewAgent).
	o := NewOrchestrator(NewLibrary(), WithMaxConcurrentSkills(2))

	o.ActivateSkill("s", &Skill{Name: "a"}, "test", "", 0.1)
	o.ActivateSkill("s", &Skill{Name: "b"}, "test", "", 0.6)

	called := atomic.Int32{}
	o.SetStickinessChecker(func(name, _ string) bool {
		called.Add(1)
		return name == "a"
	})

	// Trigger eviction by activating a third skill.
	o.ActivateSkill("s", &Skill{Name: "c"}, "test", "", 0.9)

	assert.Greater(t, called.Load(), int32(0),
		"runtime-installed checker must be consulted during eviction")
	names := activeSkillNamesForSession(o, "s")
	assert.Contains(t, names, "a", "runtime checker must keep 'a' active")
}
