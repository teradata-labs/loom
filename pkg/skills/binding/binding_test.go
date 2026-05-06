// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package binding

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/skills"
)

// fakeSource is a SkillSource backed by an in-memory slice. Avoids spinning
// up a real Library for unit tests of the resolver.
type fakeSource struct {
	skills []*skills.Skill
}

func (f *fakeSource) List() []*skills.Skill { return f.skills }

func (f *fakeSource) Load(name string) (*skills.Skill, error) {
	for _, s := range f.skills {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, nil
}

func mkSkill(name, parentPath, version string, labels map[string]string) *skills.Skill {
	return &skills.Skill{
		Name:            name,
		Version:         version,
		ParentIndexPath: parentPath,
		Labels:          labels,
		Domain:          "general",
	}
}

func TestMatchBinding_ExactName(t *testing.T) {
	s := mkSkill("sql-optimization", "enterprise/sql", "1.0.0", nil)
	b := skills.SkillBinding{Name: "sql-optimization"}

	got := MatchBinding(&b, s)
	assert.True(t, got.Matched)
	assert.Equal(t, MatchExactName, got.Kind)
	assert.Equal(t, "enterprise/sql/sql-optimization", got.FQN)
}

func TestMatchBinding_ExactFQN(t *testing.T) {
	s := mkSkill("sql-optimization", "enterprise/sql", "1.0.0", nil)
	b := skills.SkillBinding{Name: "enterprise/sql/sql-optimization"}

	got := MatchBinding(&b, s)
	assert.True(t, got.Matched)
	assert.Equal(t, MatchExactName, got.Kind)
}

func TestMatchBinding_GlobOnFQN(t *testing.T) {
	s := mkSkill("sql-optimization", "enterprise/sql", "1.0.0", nil)
	b := skills.SkillBinding{Name: "enterprise/sql/*"}

	got := MatchBinding(&b, s)
	assert.True(t, got.Matched)
	assert.Equal(t, MatchGlob, got.Kind)
}

func TestMatchBinding_GlobMissNonAdjacentSegment(t *testing.T) {
	// path.Match * does not cross /; enterprise/* must NOT match
	// enterprise/sql/sql-optimization.
	s := mkSkill("sql-optimization", "enterprise/sql", "1.0.0", nil)
	b := skills.SkillBinding{Name: "enterprise/*"}

	got := MatchBinding(&b, s)
	assert.False(t, got.Matched, "single * must not cross / segments")
}

func TestMatchBinding_GlobOnBareName(t *testing.T) {
	// Skills without a parent_index_path should still match name-only globs.
	s := mkSkill("sql-tuning", "", "1.0.0", nil)
	b := skills.SkillBinding{Name: "sql-*"}

	got := MatchBinding(&b, s)
	assert.True(t, got.Matched)
	assert.Equal(t, MatchGlob, got.Kind)
	assert.Equal(t, "sql-tuning", got.FQN)
}

func TestMatchBinding_LabelOnly(t *testing.T) {
	s := mkSkill("data-quality-check", "", "1.0.0", map[string]string{
		"team":  "data-platform",
		"tier":  "p0",
		"owner": "alice",
	})
	b := skills.SkillBinding{LabelMatch: map[string]string{"team": "data-platform", "tier": "p0"}}

	got := MatchBinding(&b, s)
	assert.True(t, got.Matched)
	assert.Equal(t, MatchLabel, got.Kind)
}

func TestMatchBinding_LabelANDFails(t *testing.T) {
	s := mkSkill("data-quality-check", "", "1.0.0", map[string]string{"team": "data-platform"})
	// Requires BOTH team=data-platform AND tier=p0; tier missing.
	b := skills.SkillBinding{LabelMatch: map[string]string{"team": "data-platform", "tier": "p0"}}

	got := MatchBinding(&b, s)
	assert.False(t, got.Matched, "label_match must AND across all keys")
}

func TestMatchBinding_LabelFilterRejectsNameMatch(t *testing.T) {
	// A binding that names a skill but whose label_match excludes it must
	// not match.
	s := mkSkill("sql-optimization", "", "1.0.0", map[string]string{"env": "prod"})
	b := skills.SkillBinding{
		Name:       "sql-optimization",
		LabelMatch: map[string]string{"env": "staging"},
	}

	got := MatchBinding(&b, s)
	assert.False(t, got.Matched)
}

func TestMatchBinding_MinVersionGate(t *testing.T) {
	cases := []struct {
		name       string
		actual     string
		minimum    string
		wantMatch  bool
	}{
		{"equal", "1.0.0", "1.0.0", true},
		{"greater patch", "1.0.5", "1.0.0", true},
		{"greater minor", "1.5.0", "1.4.9", true},
		{"less patch", "1.0.0", "1.0.5", false},
		{"less major", "1.0.0", "2.0.0", false},
		{"missing trailing segments", "1.2", "1.2.0", true},
		{"v-prefix", "v1.2.3", "1.2.0", true},
		{"prerelease suffix dropped", "1.2.3-alpha", "1.2.3", true},
		{"empty min always passes", "1.2.3", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mkSkill("v-skill", "", tc.actual, nil)
			b := skills.SkillBinding{Name: "v-skill", MinVersion: tc.minimum}
			got := MatchBinding(&b, s)
			assert.Equal(t, tc.wantMatch, got.Matched)
		})
	}
}

func TestMatchBinding_EmptyBindingNoLabelsIsNoop(t *testing.T) {
	s := mkSkill("any", "", "1.0.0", nil)
	b := skills.SkillBinding{}
	got := MatchBinding(&b, s)
	assert.False(t, got.Matched)
}

func TestValidatePattern(t *testing.T) {
	assert.NoError(t, ValidatePattern("plain-name"))
	assert.NoError(t, ValidatePattern("enterprise/*"))
	assert.NoError(t, ValidatePattern("[ab]-skill"))
	// path.Match returns ErrBadPattern on unclosed bracket.
	assert.Error(t, ValidatePattern("[unterminated"))
}

func TestResolve_BindingsTakesPrecedence(t *testing.T) {
	src := &fakeSource{skills: []*skills.Skill{
		mkSkill("sql-optimization", "enterprise/sql", "1.0.0", nil),
		mkSkill("pr-feedback", "", "1.0.0", nil),
	}}
	cfg := &skills.SkillsConfig{
		Enabled: true,
		// Legacy fields populated but Bindings non-empty -> ignored.
		EnabledSkills: []string{"pr-feedback"},
		Bindings: []skills.SkillBinding{
			{Name: "sql-optimization", Mode: skills.BindingEager},
		},
	}

	r := NewResolver(src)
	got, err := r.Resolve(cfg)
	require.NoError(t, err)
	require.Len(t, got, 1, "explicit Bindings must override legacy EnabledSkills")
	assert.Equal(t, "sql-optimization", got[0].Skill.Name)
	assert.Equal(t, skills.BindingEager, got[0].Mode)
	assert.Equal(t, "explicit", got[0].Source)
}

func TestResolve_LegacyEnabledSkillsShim(t *testing.T) {
	src := &fakeSource{skills: []*skills.Skill{
		mkSkill("a", "", "1.0.0", nil),
		mkSkill("b", "", "1.0.0", nil),
		mkSkill("c", "", "1.0.0", nil),
	}}
	cfg := &skills.SkillsConfig{
		Enabled:       true,
		EnabledSkills: []string{"a", "c"},
	}

	r := NewResolver(src)
	got, err := r.Resolve(cfg)
	require.NoError(t, err)
	require.Len(t, got, 2)

	names := []string{got[0].Skill.Name, got[1].Skill.Name}
	sort.Strings(names)
	assert.Equal(t, []string{"a", "c"}, names)
	for _, rb := range got {
		assert.Equal(t, skills.BindingEager, rb.Mode,
			"legacy EnabledSkills shim must map to EAGER for back-compat")
		assert.Equal(t, "legacy_enabled", rb.Source)
	}
}

func TestResolve_LegacyDefaultShim(t *testing.T) {
	// Empty Bindings AND empty EnabledSkills -> resolver synthesizes LAZY
	// bindings for everything in the library minus DisabledSkills.
	src := &fakeSource{skills: []*skills.Skill{
		mkSkill("a", "", "1.0.0", nil),
		mkSkill("b", "", "1.0.0", nil),
		mkSkill("c", "", "1.0.0", nil),
	}}
	cfg := &skills.SkillsConfig{
		Enabled:        true,
		DisabledSkills: []string{"b"},
	}

	r := NewResolver(src)
	got, err := r.Resolve(cfg)
	require.NoError(t, err)
	require.Len(t, got, 2)

	names := []string{got[0].Skill.Name, got[1].Skill.Name}
	sort.Strings(names)
	assert.Equal(t, []string{"a", "c"}, names)
	for _, rb := range got {
		assert.Equal(t, skills.BindingLazy, rb.Mode)
		assert.Equal(t, "legacy_default", rb.Source)
	}
}

func TestResolve_GlobAndExactCoexist(t *testing.T) {
	// Glob covers both, but the explicit name binding pins a specific one
	// to EAGER; the other should land at LAZY via the glob.
	src := &fakeSource{skills: []*skills.Skill{
		mkSkill("query-tune", "enterprise/sql", "1.0.0", nil),
		mkSkill("plan-fix", "enterprise/sql", "1.0.0", nil),
	}}
	cfg := &skills.SkillsConfig{
		Enabled: true,
		Bindings: []skills.SkillBinding{
			{Name: "enterprise/sql/*", Mode: skills.BindingLazy, Priority: 10},
			{Name: "query-tune", Mode: skills.BindingEager, Priority: 1},
		},
	}

	r := NewResolver(src)
	got, err := r.Resolve(cfg)
	require.NoError(t, err)
	require.Len(t, got, 2)

	byName := map[string]ResolvedBinding{}
	for _, rb := range got {
		byName[rb.Skill.Name] = rb
	}

	// Exact-name binding wins for query-tune even though the glob has
	// higher priority — match-kind precedence outranks priority.
	assert.Equal(t, skills.BindingEager, byName["query-tune"].Mode)
	assert.Equal(t, MatchExactName, byName["query-tune"].MatchKind)
	assert.Equal(t, "explicit", byName["query-tune"].Source)

	assert.Equal(t, skills.BindingLazy, byName["plan-fix"].Mode)
	assert.Equal(t, MatchGlob, byName["plan-fix"].MatchKind)
	assert.Equal(t, "glob", byName["plan-fix"].Source)
}

func TestResolve_TieBreakOnPriorityThenMode(t *testing.T) {
	// Same MatchKind, different priorities: higher priority wins.
	src := &fakeSource{skills: []*skills.Skill{
		mkSkill("svc", "ent/x", "1.0.0", nil),
	}}
	cfg := &skills.SkillsConfig{
		Enabled: true,
		Bindings: []skills.SkillBinding{
			{Name: "ent/*", Mode: skills.BindingLazy, Priority: 1},
			{Name: "ent/x/*", Mode: skills.BindingAlways, Priority: 5},
		},
	}

	r := NewResolver(src)
	got, err := r.Resolve(cfg)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, skills.BindingAlways, got[0].Mode,
		"higher priority among same-kind matches must win")
}

func TestResolve_DisabledConfigYieldsNothing(t *testing.T) {
	src := &fakeSource{skills: []*skills.Skill{mkSkill("a", "", "1.0.0", nil)}}
	cfg := &skills.SkillsConfig{Enabled: false, EnabledSkills: []string{"a"}}

	r := NewResolver(src)
	got, err := r.Resolve(cfg)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestResolve_BadGlobPatternErrors(t *testing.T) {
	src := &fakeSource{skills: []*skills.Skill{mkSkill("a", "", "1.0.0", nil)}}
	cfg := &skills.SkillsConfig{
		Enabled:  true,
		Bindings: []skills.SkillBinding{{Name: "[unterminated"}},
	}

	r := NewResolver(src)
	_, err := r.Resolve(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[unterminated")
}

func TestResolve_DefaultModeIsLazy(t *testing.T) {
	src := &fakeSource{skills: []*skills.Skill{mkSkill("a", "", "1.0.0", nil)}}
	cfg := &skills.SkillsConfig{
		Enabled:  true,
		Bindings: []skills.SkillBinding{{Name: "a"}}, // Mode left empty
	}

	r := NewResolver(src)
	got, err := r.Resolve(cfg)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, skills.BindingLazy, got[0].Mode,
		"empty mode must default to LAZY (new-style default)")
}
