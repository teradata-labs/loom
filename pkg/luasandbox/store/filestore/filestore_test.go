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

package filestore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/luasandbox/store"
)

var ctx = context.Background()

func script(name string) store.Script {
	return store.Script{Name: name, Description: "Sums a list.", Source: `return #args`, Owner: "agent-a"}
}

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir, luasandbox.Limits{})
	require.NoError(t, err)
	return s
}

func TestSaveGetVersionsAndDuplicates(t *testing.T) {
	s := open(t, t.TempDir())

	got, created, err := s.Save(ctx, script("sum_list"), false)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 1, got.Version)
	assert.False(t, got.Published)

	_, _, err = s.Save(ctx, script("sum_list"), false)
	assert.ErrorIs(t, err, store.ErrDuplicate)
	assert.Contains(t, err.Error(), "version 1")

	require.NoError(t, s.SetPublished(ctx, "sum_list", true))
	v2 := script("sum_list")
	v2.Source = `return #args + 0`
	got, created, err = s.Save(ctx, v2, true)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, 2, got.Version)
	assert.True(t, got.Published, "a new version keeps the published flag")

	read, err := s.Get(ctx, "sum_list")
	require.NoError(t, err)
	assert.Equal(t, `return #args + 0`, read.Source)

	runner, err := s.GetForRunner(ctx, "sum_list")
	require.NoError(t, err)
	assert.Equal(t, luasandbox.TrustOwn, runner.Trust)

	_, err = s.Get(ctx, "missing")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSaveValidates(t *testing.T) {
	s := open(t, t.TempDir())
	big := map[string]any{"type": "object", "properties": map[string]any{}}
	for i := 0; i < 21; i++ {
		big["properties"].(map[string]any)[fmt.Sprintf("p%d", i)] = map[string]any{"type": "string"}
	}
	for name, sc := range map[string]store.Script{
		"bad name":        {Name: "Bad-Name", Description: "d", Source: "return 1"},
		"short name":      {Name: "ab", Description: "d", Source: "return 1"},
		"no description":  {Name: "abc", Source: "return 1"},
		"empty source":    {Name: "abc", Description: "d", Source: "  "},
		"does not parse":  {Name: "abc", Description: "d", Source: "return ("},
		"params not obj":  {Name: "abc", Description: "d", Source: "return 1", Manifest: &store.Manifest{Parameters: map[string]any{"type": "array"}}},
		"too many params": {Name: "abc", Description: "d", Source: "return 1", Manifest: &store.Manifest{Parameters: big}},
		"$ref":            {Name: "abc", Description: "d", Source: "return 1", Manifest: &store.Manifest{Parameters: map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"$ref": "#/x"}}}}},
		"bad requires":    {Name: "abc", Description: "d", Source: "return 1", Manifest: &store.Manifest{Requires: []string{"two words"}}},
		"multi-line ret":  {Name: "abc", Description: "d", Source: "return 1", Manifest: &store.Manifest{Returns: "a\nb"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := s.Save(ctx, sc, false)
			assert.ErrorIs(t, err, store.ErrInvalid)
		})
	}
	scripts, err := s.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, scripts, "nothing invalid was stored")
}

func TestPublishAttachDetachDelete(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	_, _, err := s.Save(ctx, script("report"), false)
	require.NoError(t, err)

	assert.ErrorIs(t, s.Attach(ctx, "agent-a", "report"), store.ErrNotPublished, "attach needs publish")
	require.NoError(t, s.SetPublished(ctx, "report", true))
	require.NoError(t, s.Attach(ctx, "agent-a", "report"))
	require.NoError(t, s.Attach(ctx, "agent-a", "report"), "attaching twice is a no-op")
	require.NoError(t, s.Attach(ctx, "agent-b", "report"))

	got, err := s.Attachments(ctx, "agent-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"report"}, got)
	agents, err := s.AttachedAgents(ctx, "report")
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-a", "agent-b"}, agents)

	require.NoError(t, s.Detach(ctx, "agent-a", "report"))
	require.NoError(t, s.Detach(ctx, "agent-a", "report"), "detaching twice is a no-op")
	agents, _ = s.AttachedAgents(ctx, "report")
	assert.Equal(t, []string{"agent-b"}, agents, "detach leaves other agents alone")

	require.NoError(t, s.SetPublished(ctx, "report", false))
	agents, _ = s.AttachedAgents(ctx, "report")
	assert.Equal(t, []string{"agent-b"}, agents, "unpublish keeps attachments")

	require.NoError(t, s.Delete(ctx, "report"))
	assert.ErrorIs(t, s.Delete(ctx, "report"), store.ErrNotFound)
	agents, _ = s.AttachedAgents(ctx, "report")
	assert.Empty(t, agents, "delete removes attachments")
	_, err = os.Stat(filepath.Join(dir, "report.lua"))
	assert.True(t, os.IsNotExist(err))
}

func TestReloadFromDisk(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	sc := script("join_sales")
	sc.Manifest = &store.Manifest{
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"region": map[string]any{"type": "string"}}},
		Requires:   []string{},
		Returns:    "rows",
	}
	_, _, err := s.Save(ctx, sc, false)
	require.NoError(t, err)
	require.NoError(t, s.SetPublished(ctx, "join_sales", true))
	require.NoError(t, s.Attach(ctx, "agent-a", "join_sales"))

	re := open(t, dir)
	assert.Empty(t, re.LoadErrors())
	got, err := re.Get(ctx, "join_sales")
	require.NoError(t, err)
	assert.True(t, got.Published)
	require.NotNil(t, got.Manifest)
	assert.NotNil(t, got.Manifest.Requires, "an empty requires list survives the round trip")
	assert.Empty(t, got.Manifest.Requires)
	att, _ := re.Attachments(ctx, "agent-a")
	assert.Equal(t, []string{"join_sales"}, att)
}

func TestCorruptFilesAreSkippedAndReported(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	_, _, err := s.Save(ctx, script("good_one"), false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orphan.json"), []byte(`{"name":"orphan"}`), 0o600)) // no .lua
	require.NoError(t, os.WriteFile(filepath.Join(dir, "BAD.json"), []byte(`{}`), 0o600))

	re := open(t, dir)
	assert.Len(t, re.LoadErrors(), 3)
	all, err := re.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "good_one", all[0].Name)
}

// The design's test: 50 concurrent saves and attachments to distinct names,
// then a fresh store loaded from disk has all 50.
func TestConcurrentWritersLoseNothing(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("script_%02d", i)
			_, _, err := s.Save(ctx, script(name), false)
			assert.NoError(t, err)
			assert.NoError(t, s.SetPublished(ctx, name, true))
			assert.NoError(t, s.Attach(ctx, fmt.Sprintf("agent-%d", i%5), name))
			_, _ = s.List(ctx) // readers run alongside writers
		}(i)
	}
	wg.Wait()

	re := open(t, dir)
	all, err := re.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 50)
	total := 0
	for i := 0; i < 5; i++ {
		att, _ := re.Attachments(ctx, fmt.Sprintf("agent-%d", i))
		total += len(att)
	}
	assert.Equal(t, 50, total)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".tmp-"), "no temp file left behind: %s", e.Name())
	}
}

func TestOpenErrors(t *testing.T) {
	_, err := Open("", luasandbox.Limits{})
	assert.Error(t, err)
	file := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	_, err = Open(file, luasandbox.Limits{})
	assert.Error(t, err)
}
