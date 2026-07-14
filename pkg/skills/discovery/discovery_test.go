// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package discovery

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/skills/binding"
	"github.com/teradata-labs/loom/pkg/skills/index"
	"github.com/teradata-labs/loom/pkg/types"
)

// =============================================================================
// Test fixtures
// =============================================================================

func mkSkill(name, parent string, slash []string, keywords []string) *skills.Skill {
	return &skills.Skill{
		Name:            name,
		Title:           name,
		Description:     "test skill " + name,
		Domain:          "general",
		ParentIndexPath: parent,
		Trigger: skills.SkillTrigger{
			SlashCommands: slash,
			Keywords:      keywords,
			Mode:          skills.ActivationHybrid,
		},
	}
}

// libraryWith returns a Library populated via Register so the test does
// not have to write YAML files. The library handles slash and keyword
// indexing internally.
func libraryWith(t *testing.T, items ...*skills.Skill) *skills.Library {
	t.Helper()
	lib := skills.NewLibrary()
	for _, s := range items {
		lib.Register(s)
	}
	return lib
}

// =============================================================================
// Tests
// =============================================================================

func TestDiscovery_DisabledConfig_ReturnsNothing(t *testing.T) {
	lib := libraryWith(t, mkSkill("a", "", nil, nil))
	r := binding.NewResolver(lib)
	d := New(lib, r)

	got, err := d.Discover(context.Background(), "s", "msg",
		&skills.SkillsConfig{Enabled: false, EnabledSkills: []string{"a"}})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDiscovery_SlashCommand_FastPath(t *testing.T) {
	a := mkSkill("a", "", []string{"/a"}, []string{"alpha"})
	b := mkSkill("b", "", []string{"/b"}, []string{"beta"})
	lib := libraryWith(t, a, b)
	d := New(lib, binding.NewResolver(lib))

	cfg := &skills.SkillsConfig{
		Enabled: true,
		Bindings: []skills.SkillBinding{
			{Name: "a", Mode: skills.BindingLazy},
			{Name: "b", Mode: skills.BindingLazy},
		},
	}
	got, err := d.Discover(context.Background(), "s", "/a please", cfg)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Skill.Name)
	assert.Equal(t, "slash_command", got[0].TriggerType)
	assert.Equal(t, 1.0, got[0].Confidence)
}

func TestDiscovery_SlashCommand_FilteredByBinding(t *testing.T) {
	// Skill 'a' exists in the library and matches /a, but the agent is not
	// bound to it. Discovery must not surface it.
	a := mkSkill("a", "", []string{"/a"}, nil)
	lib := libraryWith(t, a)
	d := New(lib, binding.NewResolver(lib))

	cfg := &skills.SkillsConfig{
		Enabled:  true,
		Bindings: []skills.SkillBinding{{Name: "z", Mode: skills.BindingLazy}}, // 'z' doesn't exist
	}
	got, err := d.Discover(context.Background(), "s", "/a", cfg)
	require.NoError(t, err)
	assert.Empty(t, got, "slash hit on out-of-binding skill must not surface")
}

func TestDiscovery_SlashCommand_NewlineSeparated(t *testing.T) {
	a := mkSkill("a", "", []string{"/a"}, nil)
	lib := libraryWith(t, a)
	d := New(lib, binding.NewResolver(lib))

	cfg := &skills.SkillsConfig{
		Enabled:  true,
		Bindings: []skills.SkillBinding{{Name: "a", Mode: skills.BindingLazy}},
	}

	for _, msg := range []string{
		"/a\ncheck this for me",
		"/a\tcheck this with tab",
		"/a\r\ncheck this with CRLF",
	} {
		got, err := d.Discover(context.Background(), "s", msg, cfg)
		require.NoError(t, err)
		require.Len(t, got, 1, "msg: %q", msg)
		assert.Equal(t, "a", got[0].Skill.Name)
		assert.Equal(t, "slash_command", got[0].TriggerType)
		assert.Equal(t, 1.0, got[0].Confidence)
		// discovery.go sets TriggerValue: cmd (without rest)
		assert.Equal(t, "/a", got[0].TriggerValue)
	}
}

func TestDiscovery_SlashCommand_CapturesArgs(t *testing.T) {
	a := mkSkill("profile", "", []string{"/profile"}, nil)
	lib := libraryWith(t, a)
	d := New(lib, binding.NewResolver(lib))

	cfg := &skills.SkillsConfig{
		Enabled:  true,
		Bindings: []skills.SkillBinding{{Name: "profile", Mode: skills.BindingLazy}},
	}

	// Trailing text after the slash command is captured as TriggerArgs.
	got, err := d.Discover(context.Background(), "s",
		"/profile summarize demo_user.online_retail", cfg)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "slash_command", got[0].TriggerType)
	assert.Equal(t, "/profile", got[0].TriggerValue)
	assert.Equal(t, "summarize demo_user.online_retail", got[0].TriggerArgs)

	// A bare slash command with no trailing text yields empty args.
	got, err = d.Discover(context.Background(), "s", "/profile", cfg)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].TriggerArgs)
}

func TestDiscovery_FTSFallback_WhenNoRouter(t *testing.T) {
	a := mkSkill("sql-tune", "", nil, []string{"slow query", "tune query"})
	b := mkSkill("pr", "", nil, []string{"review pr"})
	lib := libraryWith(t, a, b)
	d := New(lib, binding.NewResolver(lib))

	cfg := &skills.SkillsConfig{
		Enabled: true,
		Bindings: []skills.SkillBinding{
			{Name: "sql-tune", Mode: skills.BindingLazy},
			{Name: "pr", Mode: skills.BindingLazy},
		},
		MinAutoConfidence: 0.1, // permissive so FTS hits
	}
	got, err := d.Discover(context.Background(), "s", "this is a slow query that needs help", cfg)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Equal(t, "sql-tune", got[0].Skill.Name)
	assert.Equal(t, "fts", got[0].TriggerType)
}

func TestDiscovery_RouterFirstThenFTSFallback(t *testing.T) {
	// Build a router that always returns one skill via the LLM. FTS would
	// return a different skill on the same message; this proves router
	// takes precedence when wired.
	a := mkSkill("router-pick", "ent/x", nil, []string{"keyword"})
	b := mkSkill("fts-pick", "", nil, []string{"keyword"})
	lib := libraryWith(t, a, b)

	// Manually build a tiny tree the router can walk: root -> ent/x leaf
	// holding skill "router-pick".
	leaf := &skills.SkillIndexNode{
		ID:        index.NodeID("ent/x"),
		Title:     "ent x",
		Depth:     1,
		SkillRefs: []string{"router-pick"},
	}
	root := &skills.SkillIndexNode{
		ID:       index.NodeID(index.Root),
		Title:    "root",
		Children: []string{leaf.ID},
	}
	idx := &skills.SkillIndex{
		ID:     "test-idx",
		RootID: root.ID,
		Nodes:  []*skills.SkillIndexNode{root, leaf},
	}

	scripted := newScriptedLLM([]string{
		`{"descend":["` + leaf.ID + `"],"skills":[],"reason":"go to ent/x"}`,
	})
	router := index.NewRouter(lib,
		index.WithRouterLLM(scripted),
		index.WithRouterMaxCandidates(5),
	)
	router.SetTree(index.NewTree(idx))

	d := New(lib, binding.NewResolver(lib),
		WithRouter(router),
		WithCache(index.NewCache()),
	)

	cfg := &skills.SkillsConfig{
		Enabled:           true,
		MinAutoConfidence: 0.1,
		Bindings: []skills.SkillBinding{
			{Name: "router-pick", Mode: skills.BindingLazy},
			{Name: "fts-pick", Mode: skills.BindingLazy},
		},
	}
	got, err := d.Discover(context.Background(), "s", "use the keyword", cfg)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Equal(t, "router-pick", got[0].Skill.Name,
		"router-first must return the router's pick, not FTS5's")
	assert.Equal(t, "router", got[0].TriggerType)
}

func TestDiscovery_FallthroughToFTS_WhenRouterReturnsEmpty(t *testing.T) {
	// Router with empty tree returns no candidates -> fall through to FTS5.
	a := mkSkill("a", "", nil, []string{"hello world"})
	lib := libraryWith(t, a)

	emptyIdx := &skills.SkillIndex{
		ID:     "empty",
		RootID: index.NodeID(index.Root),
		Nodes: []*skills.SkillIndexNode{{
			ID:    index.NodeID(index.Root),
			Title: "root",
		}},
	}
	scripted := newScriptedLLM([]string{`{"descend":[],"skills":[]}`})
	router := index.NewRouter(lib, index.WithRouterLLM(scripted))
	router.SetTree(index.NewTree(emptyIdx))

	d := New(lib, binding.NewResolver(lib),
		WithRouter(router),
		WithCache(index.NewCache()),
	)
	cfg := &skills.SkillsConfig{
		Enabled:           true,
		MinAutoConfidence: 0.1,
		Bindings:          []skills.SkillBinding{{Name: "a"}},
	}
	got, err := d.Discover(context.Background(), "s", "hello world", cfg)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Equal(t, "fts", got[0].TriggerType,
		"router empty result must let FTS5 fallback fire")
}

func TestDiscovery_AlwaysBindingSurfacesEveryTurn(t *testing.T) {
	a := mkSkill("guardrail", "", nil, nil) // no triggers
	b := mkSkill("normal", "", []string{"/normal"}, nil)
	lib := libraryWith(t, a, b)
	d := New(lib, binding.NewResolver(lib))

	cfg := &skills.SkillsConfig{
		Enabled: true,
		Bindings: []skills.SkillBinding{
			{Name: "guardrail", Mode: skills.BindingAlways},
			{Name: "normal", Mode: skills.BindingLazy},
		},
		MaxConcurrentSkills: 5,
	}
	got, err := d.Discover(context.Background(), "s",
		"random unrelated message", cfg)
	require.NoError(t, err)
	names := []string{}
	for _, c := range got {
		names = append(names, c.Skill.Name)
	}
	assert.Contains(t, names, "guardrail",
		"ALWAYS-mode binding must surface even with no trigger match")
}

func TestDiscovery_MaxConcurrentSkillsCap(t *testing.T) {
	// Five skills, all keyword-matched; cap to 2.
	a := mkSkill("a", "", nil, []string{"foo"})
	b := mkSkill("b", "", nil, []string{"foo"})
	c := mkSkill("c", "", nil, []string{"foo"})
	dSk := mkSkill("d", "", nil, []string{"foo"})
	e := mkSkill("e", "", nil, []string{"foo"})
	lib := libraryWith(t, a, b, c, dSk, e)
	disc := New(lib, binding.NewResolver(lib))

	cfg := &skills.SkillsConfig{
		Enabled:             true,
		MinAutoConfidence:   0.01,
		MaxConcurrentSkills: 2,
		Bindings: []skills.SkillBinding{
			{Name: "*", Mode: skills.BindingLazy}, // glob over everything
		},
	}
	got, err := disc.Discover(context.Background(), "s", "foo foo foo", cfg)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(got), 2, "MaxConcurrentSkills must cap output")
}

func TestDiscovery_BindingCacheReusesResolution(t *testing.T) {
	a := mkSkill("a", "", []string{"/a"}, nil)
	lib := libraryWith(t, a)
	d := New(lib, binding.NewResolver(lib))

	cfg := &skills.SkillsConfig{
		Enabled:  true,
		Bindings: []skills.SkillBinding{{Name: "a", Mode: skills.BindingLazy}},
	}

	// Two discovers with the same config should share the cached binding
	// resolution.
	_, err := d.Discover(context.Background(), "s1", "/a", cfg)
	require.NoError(t, err)
	_, err = d.Discover(context.Background(), "s1", "/a", cfg)
	require.NoError(t, err)

	// Trigger a config change and confirm the cache busts.
	cfg2 := &skills.SkillsConfig{
		Enabled:  true,
		Bindings: []skills.SkillBinding{{Name: "a", Mode: skills.BindingEager}},
	}
	_, err = d.Discover(context.Background(), "s1", "/a", cfg2)
	require.NoError(t, err)
	// Smoke test only: the cache itself is verified via configFingerprint
	// stability below.
}

func TestConfigFingerprint_StableOrderInsensitive(t *testing.T) {
	a := &skills.SkillsConfig{
		Enabled:        true,
		Bindings:       []skills.SkillBinding{{Name: "x"}, {Name: "y"}},
		EnabledSkills:  []string{"a", "b"},
		DisabledSkills: []string{"c"},
	}
	b := &skills.SkillsConfig{
		Enabled:        true,
		Bindings:       []skills.SkillBinding{{Name: "x"}, {Name: "y"}},
		EnabledSkills:  []string{"b", "a"},
		DisabledSkills: []string{"c"},
	}
	assert.Equal(t, configFingerprint(a), configFingerprint(b),
		"fingerprint must ignore EnabledSkills order")

	c := &skills.SkillsConfig{
		Enabled:  false, // changed
		Bindings: []skills.SkillBinding{{Name: "x"}, {Name: "y"}},
	}
	assert.NotEqual(t, configFingerprint(a), configFingerprint(c),
		"toggling Enabled must drift the fingerprint")
}

// =============================================================================
// Test stubs
// =============================================================================

// scriptedLLM returns successive canned responses, then repeats the last.
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
