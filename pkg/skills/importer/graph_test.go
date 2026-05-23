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

package importer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/skills/index"
)

// fixtureIndexSource lets tests inject a SkillIndex without standing
// up a real Store. Mirrors only the LatestIndex method we depend on.
type fixtureIndexSource struct {
	idx *skills.SkillIndex
	err error
}

func (f *fixtureIndexSource) LatestIndex(_ context.Context) (*skills.SkillIndex, error) {
	return f.idx, f.err
}

// nodeID is a small wrapper over index.NodeID so the fixtures read
// like the structures the builder produces.
func nodeID(path string) string { return index.NodeID(path) }

// buildIndex constructs a populated SkillIndex resembling what
// index.Builder.Build would emit, suitable for testing the walk in
// BuildGraphContext. It accepts a flat (path, []skillRefs) spec and
// materializes ancestor nodes for each path.
func buildIndex(t *testing.T, leafs map[string][]string) *skills.SkillIndex {
	t.Helper()

	// Collect every path mentioned plus their ancestors.
	pathSet := map[string]bool{"": true}
	for p := range leafs {
		segs := splitGraphPath(p)
		for i := 1; i <= len(segs); i++ {
			pathSet[joinSegments(segs[:i])] = true
		}
	}

	// Build nodes keyed by path.
	byPath := make(map[string]*skills.SkillIndexNode, len(pathSet))
	for p := range pathSet {
		title := humanize(lastSegment(p))
		if p == "" {
			title = "All Skills"
		}
		byPath[p] = &skills.SkillIndexNode{
			ID:        nodeID(p),
			Title:     title,
			Depth:     int32(len(splitGraphPath(p))),
			SkillRefs: leafs[p],
		}
	}

	// Wire children pointers: for each non-root path, append its id
	// to the parent's Children list.
	for p := range pathSet {
		if p == "" {
			continue
		}
		segs := splitGraphPath(p)
		parent := joinSegments(segs[:len(segs)-1])
		byPath[parent].Children = append(byPath[parent].Children, byPath[p].ID)
	}

	nodes := make([]*skills.SkillIndexNode, 0, len(byPath))
	for _, n := range byPath {
		nodes = append(nodes, n)
	}

	return &skills.SkillIndex{
		ID:     "test-idx",
		RootID: nodeID(""),
		Nodes:  nodes,
	}
}

// joinSegments pieces "a/b/c" back together from a slice. Mirrors
// index.JoinPath without the import.
func joinSegments(segs []string) string {
	out := ""
	for i, s := range segs {
		if i > 0 {
			out += "/"
		}
		out += s
	}
	return out
}

// lastSegment returns the trailing path segment.
func lastSegment(path string) string {
	segs := splitGraphPath(path)
	if len(segs) == 0 {
		return ""
	}
	return segs[len(segs)-1]
}

// humanize is the test-side mirror of index.humanizeSegment so we
// can fixture-build identical Titles to what the real builder emits.
func humanize(seg string) string {
	out := ""
	upper := true
	for _, r := range seg {
		switch r {
		case '-', '_', ' ':
			out += " "
			upper = true
		default:
			if upper {
				if r >= 'a' && r <= 'z' {
					r = r - 'a' + 'A'
				}
				upper = false
			}
			out += string(r)
		}
	}
	return out
}

func TestBuildGraphContext_EmptyIndex(t *testing.T) {
	src := &fixtureIndexSource{idx: nil}
	got, err := BuildGraphContext(context.Background(), src)
	require.NoError(t, err)
	assert.Empty(t, got.Buckets)
}

func TestBuildGraphContext_NilSource(t *testing.T) {
	got, err := BuildGraphContext(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got.Buckets)
}

func TestBuildGraphContext_SingleBucket(t *testing.T) {
	idx := buildIndex(t, map[string][]string{
		"teradata/performance": {"teradata-statistics", "teradata-adaptive-optimizer"},
	})
	src := &fixtureIndexSource{idx: idx}

	got, err := BuildGraphContext(context.Background(), src)
	require.NoError(t, err)
	require.Len(t, got.Buckets, 1)
	assert.Equal(t, "teradata/performance", got.Buckets[0].Path)
	// Members are sorted alphabetically.
	assert.Equal(t,
		[]string{"teradata-adaptive-optimizer", "teradata-statistics"},
		got.Buckets[0].Members)
}

func TestBuildGraphContext_MultiBucketSortedDeterministic(t *testing.T) {
	idx := buildIndex(t, map[string][]string{
		"teradata/storage":     {"teradata-nopi-tables", "teradata-partitioning"},
		"teradata/performance": {"teradata-statistics"},
		"teradata/security":    {"teradata-security"},
		"teradata/admin":       {"teradata-system-admin", "teradata-workload-management"},
	})
	src := &fixtureIndexSource{idx: idx}

	got, err := BuildGraphContext(context.Background(), src)
	require.NoError(t, err)
	require.Len(t, got.Buckets, 4)

	// Path-sorted output.
	assert.Equal(t, "teradata/admin", got.Buckets[0].Path)
	assert.Equal(t, "teradata/performance", got.Buckets[1].Path)
	assert.Equal(t, "teradata/security", got.Buckets[2].Path)
	assert.Equal(t, "teradata/storage", got.Buckets[3].Path)

	// Members sorted within bucket.
	assert.Equal(t,
		[]string{"teradata-system-admin", "teradata-workload-management"},
		got.Buckets[0].Members)
	assert.Equal(t,
		[]string{"teradata-nopi-tables", "teradata-partitioning"},
		got.Buckets[3].Members)
}

func TestBuildGraphContext_UnpopulatedAncestorsOmitted(t *testing.T) {
	// The "teradata" ancestor of teradata/performance carries no
	// skill_refs of its own — only its leaf does. The walk should
	// emit ONLY populated buckets, not synthetic intermediate nodes.
	idx := buildIndex(t, map[string][]string{
		"teradata/performance": {"teradata-statistics"},
	})
	src := &fixtureIndexSource{idx: idx}

	got, err := BuildGraphContext(context.Background(), src)
	require.NoError(t, err)
	require.Len(t, got.Buckets, 1,
		"intermediate nodes (teradata/) without skill_refs must be omitted")
	assert.Equal(t, "teradata/performance", got.Buckets[0].Path)
}

func TestBuildGraphContext_UnclassifiedDomain(t *testing.T) {
	// Skills with no parent_index_path land at unclassified/<domain>
	// per index.SkillPath. The walk reconstructs this exactly so
	// "Current catalog" presents what the router actually sees.
	idx := buildIndex(t, map[string][]string{
		"unclassified/teradata":   {"teradata-elasticity"},
		"unclassified/meta-agent": {"weaver-creation", "teradata-skill-index"},
	})
	src := &fixtureIndexSource{idx: idx}

	got, err := BuildGraphContext(context.Background(), src)
	require.NoError(t, err)
	require.Len(t, got.Buckets, 2)
	assert.Equal(t, "unclassified/meta-agent", got.Buckets[0].Path)
	assert.Equal(t, "unclassified/teradata", got.Buckets[1].Path)
}

func TestBuildGraphContext_MissingRoot(t *testing.T) {
	// Index claims a root id but the corresponding node is missing.
	idx := &skills.SkillIndex{
		ID:     "broken",
		RootID: nodeID(""),
		Nodes: []*skills.SkillIndexNode{
			// One leaf node, no root entry.
			{
				ID:        nodeID("teradata/performance"),
				Title:     "Performance",
				SkillRefs: []string{"teradata-statistics"},
			},
		},
	}
	src := &fixtureIndexSource{idx: idx}

	_, err := BuildGraphContext(context.Background(), src)
	assert.Error(t, err, "missing root node must surface as a hard error, not a silent empty graph")
}

func TestBuildGraphContext_PropagatesSourceError(t *testing.T) {
	expected := assertErr("simulated db failure")
	src := &fixtureIndexSource{err: expected}
	_, err := BuildGraphContext(context.Background(), src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated db failure")
}

func TestFilterGraphByDomain(t *testing.T) {
	full := GraphContext{
		Buckets: []GraphBucket{
			{Path: "teradata/performance", Members: []string{"a"}},
			{Path: "teradata/security", Members: []string{"b"}},
			{Path: "postgres/replication", Members: []string{"c"}},
			{Path: "unclassified/teradata", Members: []string{"d"}},
			{Path: "unclassified/postgres", Members: []string{"e"}},
		},
	}

	t.Run("teradata domain", func(t *testing.T) {
		got := FilterGraphByDomain(full, "teradata")
		paths := bucketPaths(got)
		// Both teradata/* and unclassified/teradata pass.
		assert.ElementsMatch(t,
			[]string{"teradata/performance", "teradata/security", "unclassified/teradata"},
			paths)
	})

	t.Run("postgres domain", func(t *testing.T) {
		got := FilterGraphByDomain(full, "postgres")
		paths := bucketPaths(got)
		assert.ElementsMatch(t,
			[]string{"postgres/replication", "unclassified/postgres"},
			paths)
	})

	t.Run("unknown domain returns empty", func(t *testing.T) {
		got := FilterGraphByDomain(full, "rust")
		assert.Empty(t, got.Buckets)
	})

	t.Run("empty domain returns input unchanged", func(t *testing.T) {
		got := FilterGraphByDomain(full, "")
		assert.Equal(t, full, got)
	})
}

func TestPathSegmentFromTitle(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"Performance", "performance"},
		{"Data Quality", "data-quality"},
		{"All Skills", "all-skills"},
		{"Vantage Cloud Lake", "vantage-cloud-lake"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			assert.Equal(t, tc.want, pathSegmentFromTitle(tc.title))
		})
	}
}

// bucketPaths extracts paths into a slice for ElementsMatch tests.
func bucketPaths(g GraphContext) []string {
	out := make([]string, 0, len(g.Buckets))
	for _, b := range g.Buckets {
		out = append(out, b.Path)
	}
	return out
}

// assertErr is a tiny error type for fixture errors.
type assertErr string

func (e assertErr) Error() string { return string(e) }
