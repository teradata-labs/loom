// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package index

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/skills"
)

// indexCallSource counts how many times List() is called so tests can verify
// debouncing.
type indexCallSource struct {
	calls atomic.Int32
	items []*skills.Skill
}

func (s *indexCallSource) List() []*skills.Skill {
	s.calls.Add(1)
	return s.items
}

func TestHotReloadHandler_BuildsAndPersists(t *testing.T) {
	src := &indexCallSource{items: []*skills.Skill{
		{Name: "a", Title: "A", Domain: "sql"},
		{Name: "b", Title: "B", Domain: "code"},
	}}
	store := NewMemoryStore()
	cb := HotReloadHandler(RebuildOptions{
		Builder:        NewBuilder(),
		Store:          store,
		Source:         func() Source { return src },
		DebounceWindow: 30 * time.Millisecond,
	})

	cb("modify", "a", nil)

	// Wait for the debounced rebuild plus a small grace window for the
	// build/persist to complete.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if idx, _ := store.LatestIndex(context.Background()); idx != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	idx, err := store.LatestIndex(context.Background())
	require.NoError(t, err)
	require.NotNil(t, idx, "rebuild must persist an index")
	assert.NotEmpty(t, idx.Nodes)
}

func TestHotReloadHandler_Debounces(t *testing.T) {
	src := &indexCallSource{items: []*skills.Skill{{Name: "x", Domain: "sql"}}}
	store := NewMemoryStore()
	cb := HotReloadHandler(RebuildOptions{
		Builder:        NewBuilder(),
		Store:          store,
		Source:         func() Source { return src },
		DebounceWindow: 50 * time.Millisecond,
	})

	// Fire 5 events back-to-back; only one rebuild should run.
	for i := 0; i < 5; i++ {
		cb("modify", "x", nil)
	}

	time.Sleep(200 * time.Millisecond)

	calls := src.calls.Load()
	assert.LessOrEqual(t, calls, int32(1),
		"debounce window must coalesce rapid events into one rebuild")
}

func TestHotReloadHandler_ValidationFailureSkipsRebuild(t *testing.T) {
	src := &indexCallSource{items: []*skills.Skill{{Name: "x", Domain: "sql"}}}
	store := NewMemoryStore()
	cb := HotReloadHandler(RebuildOptions{
		Builder:        NewBuilder(),
		Store:          store,
		Source:         func() Source { return src },
		DebounceWindow: 30 * time.Millisecond,
	})

	cb("validation_failed", "x", assertErrLocal("bad yaml"))

	time.Sleep(100 * time.Millisecond)

	idx, err := store.LatestIndex(context.Background())
	require.NoError(t, err)
	assert.Nil(t, idx, "validation failure must NOT trigger a rebuild")
	assert.Equal(t, int32(0), src.calls.Load(),
		"source must not have been consulted on a validation failure")
}

func TestHotReloadHandler_NilOptsReturnsNoOp(t *testing.T) {
	cb := HotReloadHandler(RebuildOptions{}) // no Builder, no Source
	// Must not panic when invoked.
	cb("modify", "x", nil)
}

func TestHotReloadHandler_RouterAndCacheRefreshed(t *testing.T) {
	src := &indexCallSource{items: []*skills.Skill{
		{Name: "x", Title: "X", Domain: "sql", ParentIndexPath: "ent/sql"},
	}}
	store := NewMemoryStore()
	router := NewRouter(&fakeResolver{skills: map[string]*skills.Skill{
		"x": src.items[0],
	}})
	cache := NewCache()

	// Pre-populate cache to verify it gets cleared.
	cache.Put(CacheKey{SessionID: "s", MessageHash: "m"},
		[]*skills.Skill{{Name: "stale"}}, time.Minute)
	require.Equal(t, 1, cache.Size())

	cb := HotReloadHandler(RebuildOptions{
		Builder:        NewBuilder(),
		Store:          store,
		Source:         func() Source { return src },
		Router:         router,
		Cache:          cache,
		DebounceWindow: 30 * time.Millisecond,
	})

	cb("modify", "x", nil)

	// Wait for rebuild
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if router.Tree() != nil && cache.Size() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	assert.NotNil(t, router.Tree(), "router must receive the rebuilt tree")
	assert.Equal(t, 0, cache.Size(),
		"cache must be cleared after rebuild — prior decisions are stale")
}

type assertErrLocal string

func (e assertErrLocal) Error() string { return string(e) }
