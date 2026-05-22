// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package index

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	sqlitemig "github.com/teradata-labs/loom/pkg/storage/sqlite"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/types"
)

// =============================================================================
// node.go
// =============================================================================

func TestSkillPath(t *testing.T) {
	cases := []struct {
		name string
		s    *skills.Skill
		want string
	}{
		{"explicit path", &skills.Skill{Name: "x", ParentIndexPath: "ent/sql"}, "ent/sql"},
		{"path with trailing slash trimmed", &skills.Skill{Name: "x", ParentIndexPath: "ent/sql/"}, "ent/sql"},
		{"no path falls back to unclassified/<domain>",
			&skills.Skill{Name: "x", Domain: "ml"}, "unclassified/ml"},
		{"no path no domain falls back to unclassified/general",
			&skills.Skill{Name: "x"}, "unclassified/general"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SkillPath(tc.s))
		})
	}
}

func TestAncestorPaths(t *testing.T) {
	got := AncestorPaths("a/b/c")
	assert.Equal(t, []string{Root, "a", "a/b", "a/b/c"}, got)

	gotRoot := AncestorPaths("")
	assert.Equal(t, []string{Root}, gotRoot)
}

func TestParent(t *testing.T) {
	assert.Equal(t, "a/b", Parent("a/b/c"))
	assert.Equal(t, Root, Parent("a"))
	assert.Equal(t, Root, Parent(""))
}

func TestNodeIDStable(t *testing.T) {
	a := NodeID("enterprise/sql")
	b := NodeID("enterprise/sql")
	c := NodeID("enterprise/sql/optimization")
	assert.Equal(t, a, b, "NodeID must be deterministic")
	assert.NotEqual(t, a, c, "different paths must produce different ids")
	assert.NotEmpty(t, a)
}

func TestContentHashStableAndOrderInsensitive(t *testing.T) {
	a := &skills.Skill{Name: "a", Title: "A", Description: "do a", Domain: "sql"}
	b := &skills.Skill{Name: "b", Title: "B", Description: "do b", Domain: "sql"}
	h1 := ContentHash([]*skills.Skill{a, b})
	h2 := ContentHash([]*skills.Skill{b, a})
	assert.Equal(t, h1, h2, "ContentHash must be order-insensitive")

	// Mutating a salient field must drift the hash.
	bMut := *b
	bMut.Description = "do b differently"
	h3 := ContentHash([]*skills.Skill{a, &bMut})
	assert.NotEqual(t, h1, h3)
}

func TestTreeAdjacency(t *testing.T) {
	root := &skills.SkillIndexNode{ID: NodeID(Root), Title: "root", Children: []string{NodeID("a"), NodeID("b")}}
	nodeA := &skills.SkillIndexNode{ID: NodeID("a"), Title: "a", Depth: 1, Children: []string{NodeID("a/x")}}
	nodeAX := &skills.SkillIndexNode{ID: NodeID("a/x"), Title: "ax", Depth: 2, SkillRefs: []string{"sk-x"}}
	nodeB := &skills.SkillIndexNode{ID: NodeID("b"), Title: "b", Depth: 1}

	idx := &skills.SkillIndex{
		ID:     "test",
		RootID: root.ID,
		Nodes:  []*skills.SkillIndexNode{root, nodeA, nodeAX, nodeB},
	}

	tree := NewTree(idx)
	assert.Equal(t, root, tree.RootNode())
	assert.Equal(t, nodeA, tree.Get(nodeA.ID))

	rootKids := tree.Children(root.ID)
	require.Len(t, rootKids, 2)
	titles := []string{rootKids[0].Title, rootKids[1].Title}
	assert.Contains(t, titles, "a")
	assert.Contains(t, titles, "b")
}

// =============================================================================
// cache.go
// =============================================================================

func TestCachePutGetExpiry(t *testing.T) {
	now := time.Unix(0, 0)
	c := NewCache(WithMaxSize(4), WithDefaultTTL(time.Minute), withNowFunc(func() time.Time { return now }))

	key := CacheKey{SessionID: "s1", MessageHash: "m1", BindingsHash: "b1"}
	skill := &skills.Skill{Name: "a"}
	c.Put(key, []*skills.Skill{skill}, 0)

	assert.Len(t, c.Get(key), 1, "fresh entry should hit")

	now = now.Add(2 * time.Minute)
	assert.Empty(t, c.Get(key), "entry past TTL should miss")
	assert.Equal(t, 0, c.Size(), "expired read should evict")
}

func TestCacheFIFOEviction(t *testing.T) {
	c := NewCache(WithMaxSize(2), WithDefaultTTL(time.Hour))
	for i, name := range []string{"a", "b", "c"} {
		c.Put(CacheKey{SessionID: "s", MessageHash: name},
			[]*skills.Skill{{Name: name}}, time.Hour)
		_ = i
	}
	assert.Equal(t, 2, c.Size(), "size cap must enforce eviction")
	assert.Empty(t, c.Get(CacheKey{SessionID: "s", MessageHash: "a"}),
		"oldest entry should have been evicted")
	assert.NotEmpty(t, c.Get(CacheKey{SessionID: "s", MessageHash: "c"}))
}

func TestCacheInvalidateSession(t *testing.T) {
	c := NewCache(WithMaxSize(8), WithDefaultTTL(time.Hour))
	c.Put(CacheKey{SessionID: "s1", MessageHash: "m"}, []*skills.Skill{{Name: "a"}}, 0)
	c.Put(CacheKey{SessionID: "s2", MessageHash: "m"}, []*skills.Skill{{Name: "b"}}, 0)
	c.InvalidateSession("s1")
	assert.Empty(t, c.Get(CacheKey{SessionID: "s1", MessageHash: "m"}))
	assert.NotEmpty(t, c.Get(CacheKey{SessionID: "s2", MessageHash: "m"}))
}

func TestCacheHashBindingsStable(t *testing.T) {
	a := []skills.SkillBinding{{Name: "x", Mode: skills.BindingEager, LabelMatch: map[string]string{"a": "1", "b": "2"}}}
	b := []skills.SkillBinding{{Name: "x", Mode: skills.BindingEager, LabelMatch: map[string]string{"b": "2", "a": "1"}}}
	assert.Equal(t, HashBindings(a), HashBindings(b),
		"HashBindings must be order-insensitive on label keys")
}

func TestCacheConcurrentSafe(t *testing.T) {
	c := NewCache(WithMaxSize(64))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := CacheKey{SessionID: "s", MessageHash: "m"}
			c.Put(key, []*skills.Skill{{Name: "x"}}, time.Hour)
			c.Get(key)
		}(i)
	}
	wg.Wait()
}

// =============================================================================
// store.go - Memory + SQL
// =============================================================================

func TestMemoryStore_Roundtrip(t *testing.T) {
	ctx := context.Background()
	ms := NewMemoryStore()

	idx := simpleIndex()
	require.NoError(t, ms.SaveIndex(ctx, idx))

	loaded, err := ms.LoadIndex(ctx, idx.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, idx.ID, loaded.ID)
	assert.Equal(t, len(idx.Nodes), len(loaded.Nodes))

	latest, err := ms.LatestIndex(ctx)
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, idx.ID, latest.ID)
}

func TestMemoryStore_UpsertNode(t *testing.T) {
	ctx := context.Background()
	ms := NewMemoryStore()
	idx := simpleIndex()
	require.NoError(t, ms.SaveIndex(ctx, idx))

	// Update an existing node summary.
	target := idx.Nodes[1]
	updated := *target
	updated.Summary = "new summary"
	require.NoError(t, ms.UpsertNode(ctx, idx.ID, &updated))

	loaded, err := ms.LoadIndex(ctx, idx.ID)
	require.NoError(t, err)
	for _, n := range loaded.Nodes {
		if n.ID == updated.ID {
			assert.Equal(t, "new summary", n.Summary)
			return
		}
	}
	t.Fatalf("updated node not found")
}

func TestSQLStore_Roundtrip(t *testing.T) {
	db := newMigratedSQLite(t)
	ctx := context.Background()
	store := NewSQLStore(db, DialectSQLite)

	idx := simpleIndex()
	require.NoError(t, store.SaveIndex(ctx, idx))

	loaded, err := store.LoadIndex(ctx, idx.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, idx.ID, loaded.ID)
	require.Len(t, loaded.Nodes, len(idx.Nodes))

	// Check that JSON round-trip preserves children/skill_refs/labels.
	for _, want := range idx.Nodes {
		var got *skills.SkillIndexNode
		for _, n := range loaded.Nodes {
			if n.ID == want.ID {
				got = n
				break
			}
		}
		require.NotNil(t, got, "node %q missing after roundtrip", want.ID)
		assert.Equal(t, want.Children, got.Children)
		assert.Equal(t, want.SkillRefs, got.SkillRefs)
		assert.Equal(t, want.Title, got.Title)
		assert.Equal(t, want.Depth, got.Depth)
	}
}

func TestSQLStore_LatestPicksMostRecent(t *testing.T) {
	db := newMigratedSQLite(t)
	ctx := context.Background()
	store := NewSQLStore(db, DialectSQLite)

	older := simpleIndex()
	older.ID = "older"
	older.RootID = NodeID(Root) + "-older"
	older.BuiltAtMs = 100
	for _, n := range older.Nodes {
		n.ID = n.ID + "-older"
	}
	older.Nodes[0].ID = older.RootID
	require.NoError(t, store.SaveIndex(ctx, older))

	newer := simpleIndex()
	newer.ID = "newer"
	newer.RootID = NodeID(Root) + "-newer"
	newer.BuiltAtMs = 200
	for _, n := range newer.Nodes {
		n.ID = n.ID + "-newer"
	}
	newer.Nodes[0].ID = newer.RootID
	require.NoError(t, store.SaveIndex(ctx, newer))

	latest, err := store.LatestIndex(ctx)
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, "newer", latest.ID)
}

// =============================================================================
// builder.go - deterministic (no LLM) path
// =============================================================================

type fakeSource struct{ items []*skills.Skill }

func (f *fakeSource) List() []*skills.Skill { return f.items }

func TestBuilder_NoLLMFallback(t *testing.T) {
	src := &fakeSource{items: []*skills.Skill{
		{Name: "sql-opt", Title: "SQL Optimisation", Description: "tune queries",
			Domain: "sql", ParentIndexPath: "enterprise/sql"},
		{Name: "sql-shape", Title: "SQL Shape", Description: "shape data",
			Domain: "sql", ParentIndexPath: "enterprise/sql"},
		{Name: "pr-feedback", Title: "PR Feedback", Description: "review code",
			Domain: "code"},
	}}

	b := NewBuilder()
	idx, err := b.Build(context.Background(), src)
	require.NoError(t, err)
	require.NotNil(t, idx)
	require.NotEmpty(t, idx.Nodes)

	// Every skill path must be represented.
	pathPresent := map[string]bool{}
	for _, n := range idx.Nodes {
		pathPresent[n.ID] = true
	}
	assert.True(t, pathPresent[NodeID(Root)], "root node must exist")
	assert.True(t, pathPresent[NodeID("enterprise")])
	assert.True(t, pathPresent[NodeID("enterprise/sql")])
	assert.True(t, pathPresent[NodeID("unclassified/code")],
		"skills without parent_index_path land under unclassified/<domain>")

	// Leaf at enterprise/sql attaches both sql skills.
	for _, n := range idx.Nodes {
		if n.ID == NodeID("enterprise/sql") {
			assert.ElementsMatch(t, []string{"sql-opt", "sql-shape"}, n.SkillRefs)
		}
	}
}

func TestBuilder_LLMSummaries(t *testing.T) {
	src := &fakeSource{items: []*skills.Skill{
		{Name: "x", Title: "X", Description: "do X", Domain: "sql", ParentIndexPath: "ent/sql"},
	}}
	llm := newScriptedLLM([]string{"This subtree handles X-style work."})
	b := NewBuilder(WithLLM(llm), WithBuilderModel("test-model"))

	idx, err := b.Build(context.Background(), src)
	require.NoError(t, err)
	require.NotEmpty(t, idx.Nodes)
	assert.Equal(t, "test-model", idx.BuiltByModel)

	hadLLMSummary := false
	for _, n := range idx.Nodes {
		if n.Summary == "This subtree handles X-style work." {
			hadLLMSummary = true
			break
		}
	}
	assert.True(t, hadLLMSummary, "at least one node should carry the LLM-authored summary")
}

// =============================================================================
// router.go
// =============================================================================

// fakeResolver implements the Resolver interface backed by a name->skill map.
type fakeResolver struct{ skills map[string]*skills.Skill }

func (f *fakeResolver) Load(name string) (*skills.Skill, error) {
	if s, ok := f.skills[name]; ok {
		return s, nil
	}
	return nil, nil
}

func TestRouter_LeafShortcut(t *testing.T) {
	// One-node tree: a leaf with attached skills, no children. The router
	// should surface them without an LLM call.
	leaf := &skills.SkillIndexNode{
		ID: NodeID("ent/sql"), Title: "SQL", Depth: 1,
		SkillRefs: []string{"sql-opt"},
	}
	root := &skills.SkillIndexNode{ID: NodeID(Root), Title: "root", Children: []string{leaf.ID}}
	idx := &skills.SkillIndex{ID: "i", RootID: root.ID, Nodes: []*skills.SkillIndexNode{root, leaf}}

	resolver := &fakeResolver{skills: map[string]*skills.Skill{"sql-opt": {Name: "sql-opt"}}}
	scripted := newScriptedLLM([]string{`{"descend":["` + leaf.ID + `"],"skills":[],"reason":"go to sql"}`})
	router := NewRouter(resolver,
		WithRouterLLM(scripted),
		WithRouterCache(NewCache()),
		WithRouterMaxCandidates(5),
	)
	router.SetTree(NewTree(idx))

	got, err := router.Route(context.Background(), "sess", "tune this query", nil, "h")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "sql-opt", got[0].Name)
}

func TestRouter_EligibleFilter(t *testing.T) {
	leaf := &skills.SkillIndexNode{
		ID: NodeID("a"), Depth: 1,
		SkillRefs: []string{"want-it", "filtered-out"},
	}
	root := &skills.SkillIndexNode{ID: NodeID(Root), Title: "root", Children: []string{leaf.ID}}
	idx := &skills.SkillIndex{ID: "i", RootID: root.ID, Nodes: []*skills.SkillIndexNode{root, leaf}}

	resolver := &fakeResolver{skills: map[string]*skills.Skill{
		"want-it":      {Name: "want-it"},
		"filtered-out": {Name: "filtered-out"},
	}}
	scripted := newScriptedLLM([]string{`{"descend":["` + leaf.ID + `"]}`})
	router := NewRouter(resolver, WithRouterLLM(scripted))
	router.SetTree(NewTree(idx))

	eligible := map[string]bool{"want-it": true} // filtered-out NOT eligible
	got, err := router.Route(context.Background(), "s", "msg", eligible, "h")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "want-it", got[0].Name)
}

func TestRouter_CacheHitSkipsLLM(t *testing.T) {
	leaf := &skills.SkillIndexNode{ID: NodeID("a"), Depth: 1, SkillRefs: []string{"x"}}
	root := &skills.SkillIndexNode{ID: NodeID(Root), Children: []string{leaf.ID}}
	idx := &skills.SkillIndex{ID: "i", RootID: root.ID, Nodes: []*skills.SkillIndexNode{root, leaf}}

	resolver := &fakeResolver{skills: map[string]*skills.Skill{"x": {Name: "x"}}}
	scripted := newScriptedLLM([]string{`{"descend":["` + leaf.ID + `"]}`})
	router := NewRouter(resolver, WithRouterLLM(scripted), WithRouterCache(NewCache()))
	router.SetTree(NewTree(idx))

	first, err := router.Route(context.Background(), "s", "m", nil, "h")
	require.NoError(t, err)
	require.Len(t, first, 1)

	second, err := router.Route(context.Background(), "s", "m", nil, "h")
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, 1, scripted.calls,
		"second identical call must hit the cache and skip the LLM")
}

// TestRouter_FatLeafFilter exercises the per-leaf LLM pick that fires when
// a terminal leaf carries more skill_refs than maxCandidates. Without this
// branch the router would dump every attached skill via the
// "len(children)==0" shortcut, then trim alphabetically — giving the wrong
// answer for any leaf that wasn't sub-categorized.
func TestRouter_FatLeafFilter(t *testing.T) {
	// Leaf with 8 attached skills; maxCandidates is 3.
	skillNames := []string{
		"teradata-architecture", "teradata-elasticity", "teradata-load-isolation",
		"teradata-ml-graph-engines", "teradata-partitioning", "teradata-security",
		"teradata-statistics", "teradata-stored-procedures",
	}
	leaf := &skills.SkillIndexNode{
		ID: NodeID("teradata"), Title: "Teradata", Depth: 1,
		SkillRefs: skillNames,
	}
	root := &skills.SkillIndexNode{ID: NodeID(Root), Title: "root", Children: []string{leaf.ID}}
	idx := &skills.SkillIndex{ID: "i", RootID: root.ID, Nodes: []*skills.SkillIndexNode{root, leaf}}

	skillMap := map[string]*skills.Skill{}
	for _, n := range skillNames {
		skillMap[n] = &skills.Skill{Name: n}
	}
	resolver := &fakeResolver{skills: skillMap}

	scripted := newScriptedLLM([]string{
		// Step 1: root → descend into leaf.
		`{"descend":["` + leaf.ID + `"],"skills":[],"reason":"go to teradata"}`,
		// Step 2: leaf-filter LLM picks the relevant subset.
		`{"skills":["teradata-statistics","teradata-partitioning"],"reason":"performance question"}`,
	})
	router := NewRouter(resolver,
		WithRouterLLM(scripted),
		WithRouterCache(NewCache()),
		WithRouterMaxCandidates(3),
	)
	router.SetTree(NewTree(idx))

	got, err := router.Route(context.Background(), "sess",
		"my optimizer keeps choosing nested-loop joins; how do I refresh statistics?",
		nil, "h")
	require.NoError(t, err)

	gotNames := make([]string, 0, len(got))
	for _, s := range got {
		gotNames = append(gotNames, s.Name)
	}
	sort.Strings(gotNames)
	assert.Equal(t, []string{"teradata-partitioning", "teradata-statistics"}, gotNames,
		"leaf filter must surface the LLM-picked subset, not the alphabetical-first slice")
	assert.Equal(t, 2, scripted.calls,
		"router must make exactly 2 LLM calls: one descend decision + one leaf pick")
}

// TestRouter_FatLeafFilter_FallbackOnLLMError verifies deterministic
// degradation when the leaf-filter LLM call fails: instead of returning
// nothing (which would fall through to FTS5 and lose all candidates), the
// router takes the first maxCandidates skills in stable order.
func TestRouter_FatLeafFilter_FallbackOnLLMError(t *testing.T) {
	skillNames := []string{"sk-a", "sk-b", "sk-c", "sk-d", "sk-e", "sk-f"}
	leaf := &skills.SkillIndexNode{
		ID: NodeID("td"), Title: "Teradata", Depth: 1,
		SkillRefs: skillNames,
	}
	root := &skills.SkillIndexNode{ID: NodeID(Root), Title: "root", Children: []string{leaf.ID}}
	idx := &skills.SkillIndex{ID: "i", RootID: root.ID, Nodes: []*skills.SkillIndexNode{root, leaf}}

	skillMap := map[string]*skills.Skill{}
	for _, n := range skillNames {
		skillMap[n] = &skills.Skill{Name: n}
	}
	resolver := &fakeResolver{skills: skillMap}

	// First call (descend decision) succeeds; second call (leaf pick) errors.
	// Use a custom LLM that returns the first response then errors thereafter.
	llm := &leafErrorLLM{
		firstResponse: `{"descend":["` + leaf.ID + `"],"skills":[]}`,
	}
	router := NewRouter(resolver,
		WithRouterLLM(llm),
		WithRouterCache(NewCache()),
		WithRouterMaxCandidates(3),
	)
	router.SetTree(NewTree(idx))

	got, err := router.Route(context.Background(), "s", "m", nil, "h")
	require.NoError(t, err)
	gotNames := make([]string, 0, len(got))
	for _, s := range got {
		gotNames = append(gotNames, s.Name)
	}
	sort.Strings(gotNames)
	// Stable order: after expandSkills preserves SkillRefs order, the first 3
	// of [sk-a, sk-b, sk-c, sk-d, sk-e, sk-f] are [sk-a, sk-b, sk-c].
	assert.Equal(t, []string{"sk-a", "sk-b", "sk-c"}, gotNames,
		"on leaf-filter failure, surface stable first-N rather than empty")
}

// leafErrorLLM returns firstResponse on the first call and errors after.
// Used to simulate "descend decision works but leaf pick fails".
type leafErrorLLM struct {
	mu            sync.Mutex
	calls         int
	firstResponse string
}

func (l *leafErrorLLM) Chat(_ context.Context, _ []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls == 1 {
		return &types.LLMResponse{Content: l.firstResponse}, nil
	}
	return nil, assertErr("simulated leaf-pick failure")
}

func (l *leafErrorLLM) Name() string  { return "leaf-error" }
func (l *leafErrorLLM) Model() string { return "leaf-error-model" }

// TestRouter_SmallLeafSkipsLLM keeps the original cheap path: when a leaf
// has skill_refs <= maxCandidates, the router surfaces them all without
// invoking the leaf-filter LLM.
func TestRouter_SmallLeafSkipsLLM(t *testing.T) {
	leaf := &skills.SkillIndexNode{
		ID: NodeID("td"), Title: "Teradata", Depth: 1,
		SkillRefs: []string{"sk-a", "sk-b"},
	}
	root := &skills.SkillIndexNode{ID: NodeID(Root), Title: "root", Children: []string{leaf.ID}}
	idx := &skills.SkillIndex{ID: "i", RootID: root.ID, Nodes: []*skills.SkillIndexNode{root, leaf}}

	resolver := &fakeResolver{skills: map[string]*skills.Skill{
		"sk-a": {Name: "sk-a"}, "sk-b": {Name: "sk-b"},
	}}
	scripted := newScriptedLLM([]string{
		`{"descend":["` + leaf.ID + `"],"skills":[]}`,
	})
	router := NewRouter(resolver,
		WithRouterLLM(scripted),
		WithRouterMaxCandidates(5),
	)
	router.SetTree(NewTree(idx))

	got, err := router.Route(context.Background(), "s", "m", nil, "h")
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, 1, scripted.calls,
		"small leaf must NOT trigger the leaf-pick LLM call")
}

func TestRouter_LLMErrorReturnsNoDecision(t *testing.T) {
	leaf := &skills.SkillIndexNode{ID: NodeID("a"), Depth: 1, SkillRefs: []string{"x"}}
	root := &skills.SkillIndexNode{ID: NodeID(Root), Children: []string{leaf.ID}}
	idx := &skills.SkillIndex{ID: "i", RootID: root.ID, Nodes: []*skills.SkillIndexNode{root, leaf}}

	resolver := &fakeResolver{skills: map[string]*skills.Skill{"x": {Name: "x"}}}
	failing := &erroringLLM{}
	router := NewRouter(resolver, WithRouterLLM(failing))
	router.SetTree(NewTree(idx))

	got, err := router.Route(context.Background(), "s", "m", nil, "h")
	require.NoError(t, err, "LLM errors must not propagate to caller — caller falls back to FTS5")
	assert.Empty(t, got)
}

// =============================================================================
// Helpers
// =============================================================================

func simpleIndex() *skills.SkillIndex {
	root := &skills.SkillIndexNode{ID: NodeID(Root), Title: "All Skills"}
	a := &skills.SkillIndexNode{ID: NodeID("a"), Title: "A", Depth: 1, SkillRefs: []string{"sk-a1"}}
	b := &skills.SkillIndexNode{ID: NodeID("b"), Title: "B", Depth: 1, Labels: map[string]string{"k": "v"}}
	root.Children = []string{a.ID, b.ID}
	idx := &skills.SkillIndex{
		ID:           "test-idx",
		RootID:       root.ID,
		Nodes:        []*skills.SkillIndexNode{root, a, b},
		BuiltAtMs:    time.Now().UnixMilli(),
		BuiltByModel: "test",
	}
	return idx
}

func newMigratedSQLite(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	migrator, err := sqlitemig.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(context.Background()))
	return db
}

// scriptedLLM returns successive canned responses, then errors out.
// Implements types.LLMProvider for use with the index builder and router.
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
		// Repeat the last response forever rather than erroring; the router
		// invokes Chat once per node and the test data only sets up the
		// first one.
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

// erroringLLM always returns an error. Used to verify the router's
// fallback behavior when the LLM is unreachable.
type erroringLLM struct{}

func (e *erroringLLM) Chat(_ context.Context, _ []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	return nil, assertErr("simulated LLM failure")
}
func (e *erroringLLM) Name() string  { return "erroring" }
func (e *erroringLLM) Model() string { return "erroring-model" }

type assertErr string

func (e assertErr) Error() string { return string(e) }
