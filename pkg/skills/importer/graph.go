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
	"fmt"
	"sort"
	"strings"

	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/skills/index"
)

// IndexSource is the read-only view BuildGraphContext needs from the
// router's persistence layer. index.Store satisfies this; tests pass
// in fixtures directly.
//
// The classifier's "Current catalog" prompt section is built from
// what the ROUTER currently sees, not what the on-disk YAMLs say or
// what the in-memory library holds. The three views diverge in
// realistic scenarios:
//
//   - Post-import, before warmSkillIndex completes: library has the
//     new skill but the router doesn't.
//   - Post-reclassify (yaml hand-edited or ClassifySkill RPC just
//     ran): on-disk yaml has the new path but the live router still
//     has the old tree.
//   - After a partial hot-reload: subset of skills reflect the new
//     yaml, others don't.
//
// Pulling from the persisted index (index.Store.LatestIndex) gives
// the LLM the same picture the router will use to actually route
// queries, so the "join an existing bucket" guidance has teeth.
type IndexSource interface {
	// LatestIndex returns the most-recently-built index, or
	// (nil, nil) when no index has ever been persisted. Mirrors
	// index.Store.LatestIndex exactly.
	LatestIndex(ctx context.Context) (*skills.SkillIndex, error)
}

// BuildGraphContext walks the persisted router index and groups
// every leaf bucket (terminal node carrying skill_refs) into a
// GraphContext. The returned context's Bucket.Path is the
// reconstructed path from root (e.g., "teradata/performance"),
// matching what index.SkillPath would emit for a skill with that
// parent_index_path.
//
// Empty index (no skills loaded yet, fresh server boot before warm)
// returns an empty GraphContext. The classifier prompt builder
// degrades gracefully — graph.Buckets being empty omits the
// "Current catalog" section entirely, falling back to seed-taxonomy-
// only behavior.
//
// Determinism: buckets are sorted by path; members within each bucket
// are sorted by name. Stable ordering matters for prompt caching
// (identical prompts hit cache) and for repeatable test output.
//
// Errors from LatestIndex are propagated. A nil-but-no-error result
// (no index persisted yet) returns an empty GraphContext, NOT an
// error — callers should treat "no graph yet" as a soft fallback to
// stateless Classify.
func BuildGraphContext(ctx context.Context, src IndexSource) (GraphContext, error) {
	if src == nil {
		return GraphContext{}, nil
	}
	idx, err := src.LatestIndex(ctx)
	if err != nil {
		return GraphContext{}, fmt.Errorf("read persisted index: %w", err)
	}
	if idx == nil || len(idx.Nodes) == 0 {
		return GraphContext{}, nil
	}

	// Index nodes are stored as a flat slice with parent->child
	// relationships expressed via SkillIndexNode.Children. To
	// reconstruct each node's path-from-root we have to walk top-down
	// from the root, accumulating segments as we descend.
	//
	// We build a parent_id -> []child_id adjacency once, then DFS
	// from the root collecting (path, skill_refs) for every node
	// that has skill_refs (the "populated buckets").
	byID := make(map[string]*skills.SkillIndexNode, len(idx.Nodes))
	for _, n := range idx.Nodes {
		byID[n.ID] = n
	}

	// Walk descendants of root, building path strings as we go.
	root, ok := byID[idx.RootID]
	if !ok {
		return GraphContext{}, fmt.Errorf("persisted index missing root node id %q", idx.RootID)
	}

	type entry struct {
		path string
		refs []string
	}
	var populated []entry
	var walk func(node *skills.SkillIndexNode, parentPath string)
	walk = func(node *skills.SkillIndexNode, parentPath string) {
		// The synthetic root node has no path segment; descend into
		// its children with an empty path prefix.
		var path string
		if node == root {
			path = ""
		} else {
			path = pathSegmentFromTitle(node.Title)
			if parentPath != "" {
				path = parentPath + "/" + path
			}
		}
		if len(node.SkillRefs) > 0 {
			refs := append([]string(nil), node.SkillRefs...)
			sort.Strings(refs)
			populated = append(populated, entry{path: path, refs: refs})
		}
		for _, childID := range node.Children {
			c, ok := byID[childID]
			if !ok {
				continue
			}
			walk(c, path)
		}
	}
	walk(root, "")

	sort.Slice(populated, func(i, j int) bool {
		return populated[i].path < populated[j].path
	})

	buckets := make([]GraphBucket, 0, len(populated))
	for _, p := range populated {
		buckets = append(buckets, GraphBucket{
			Path:    p.path,
			Members: p.refs,
		})
	}
	return GraphContext{Buckets: buckets}, nil
}

// FilterGraphByDomain returns a GraphContext containing only the
// buckets whose path's first segment equals domain. Useful when a
// classifier call is rooted at a specific domain and presenting the
// full graph would dilute the prompt with irrelevant subtrees.
//
// "unclassified/<domain>" buckets are NOT filtered out — they match
// the domain logically (their second segment IS the domain), so they
// remain visible to the classifier. This lets a new teradata-foo
// skill's classifier see "unclassified/teradata already holds N
// skills" and prefer joining it (or a sibling under teradata/) over
// inventing a parallel bucket.
//
// Empty domain returns the input unchanged.
func FilterGraphByDomain(g GraphContext, domain string) GraphContext {
	if domain == "" {
		return g
	}
	out := GraphContext{Buckets: make([]GraphBucket, 0, len(g.Buckets))}
	for _, b := range g.Buckets {
		segs := splitGraphPath(b.Path)
		if len(segs) == 0 {
			continue
		}
		// Two cases pass:
		//   1. <domain>/* — the obvious case
		//   2. unclassified/<domain> — second segment is the domain
		if segs[0] == domain {
			out.Buckets = append(out.Buckets, b)
			continue
		}
		if segs[0] == "unclassified" && len(segs) >= 2 && segs[1] == domain {
			out.Buckets = append(out.Buckets, b)
			continue
		}
	}
	return out
}

// pathSegmentFromTitle is the inverse of index.humanizeSegment. The
// builder converts kebab-case path segments to Title Case for display
// ("data-quality" -> "Data Quality"); we reverse that here so paths
// the classifier emits round-trip with the on-disk parent_index_path
// values.
//
// We keep this private to importer/ rather than exporting from index/
// because pathSegmentFromTitle is specific to the round-trip
// reconstruction we need; the index package owns the forward
// conversion in builder.humanizeSegment but doesn't otherwise need
// the reverse.
func pathSegmentFromTitle(title string) string {
	if title == "" {
		return ""
	}
	parts := strings.FieldsFunc(title, func(r rune) bool {
		return r == ' '
	})
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToLower(p)
	}
	return strings.Join(parts, "-")
}

// splitGraphPath splits a path like "teradata/performance" or
// "unclassified/meta-agent" into its segments. Empty path returns
// nil. Mirrors index.SplitPath but kept private to avoid coupling
// callers to that import.
func splitGraphPath(path string) []string {
	if path == "" {
		return nil
	}
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if i > start {
				out = append(out, path[start:i])
			}
			start = i + 1
		}
	}
	if start < len(path) {
		out = append(out, path[start:])
	}
	return out
}

// Compile-time assertion that index.Store satisfies IndexSource.
// If index.Store ever drops LatestIndex this fails at build time so
// we don't drift into runtime failures.
var _ IndexSource = (index.Store)(nil)
