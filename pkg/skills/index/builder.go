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

package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/types"
)

// Source provides the skill catalog the builder indexes. Library satisfies
// this via its List() method; tests can supply a slice directly.
type Source interface {
	List() []*skills.Skill
}

// Builder constructs a hierarchical SkillIndex from a Source. It groups
// skills by SkillPath, populates ancestor nodes, and (when an LLM provider
// is configured) authors short summaries for each node so the router can
// reason about the tree without re-reading every skill.
//
// When LLM is nil, deterministic fallback summaries are generated. This
// makes the builder usable in tests and in offline cold-start scenarios
// where router quality is acceptable to delay.
type Builder struct {
	llm      types.LLMProvider
	tracer   observability.Tracer
	logger   *zap.Logger
	model    string
	maxRetry int
}

// BuilderOption configures a Builder during construction.
type BuilderOption func(*Builder)

// WithLLM attaches an LLM for summary generation. Without it, summaries
// fall back to a deterministic concatenation of attached skill titles.
func WithLLM(llm types.LLMProvider) BuilderOption {
	return func(b *Builder) { b.llm = llm }
}

// WithBuilderTracer attaches an observability tracer.
func WithBuilderTracer(t observability.Tracer) BuilderOption {
	return func(b *Builder) {
		if t != nil {
			b.tracer = t
		}
	}
}

// WithBuilderLogger attaches a zap logger.
func WithBuilderLogger(l *zap.Logger) BuilderOption {
	return func(b *Builder) {
		if l != nil {
			b.logger = l
		}
	}
}

// WithBuilderModel records the model identifier under which the index was
// built (used for cache invalidation when a deployment switches models).
func WithBuilderModel(name string) BuilderOption {
	return func(b *Builder) { b.model = name }
}

// NewBuilder constructs a Builder. The LLM is optional; when absent the
// builder uses a deterministic fallback summary so the index can still be
// built (for unit tests, CI, and cold-start scenarios).
func NewBuilder(opts ...BuilderOption) *Builder {
	b := &Builder{
		tracer:   observability.NewNoOpTracer(),
		logger:   zap.NewNop(),
		maxRetry: 1,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Build groups src.List() into a tree, creates ancestor nodes for every
// SkillPath, and populates summaries. Returns a SkillIndex ready to hand
// to Store.SaveIndex and to Router.
func (b *Builder) Build(ctx context.Context, src Source) (*skills.SkillIndex, error) {
	if src == nil {
		return nil, fmt.Errorf("index.Build: source is required")
	}

	ctx, span := b.tracer.StartSpan(ctx, "skills.index.build")
	defer b.tracer.EndSpan(span)

	all := src.List()
	if len(all) == 0 {
		return emptyIndex(b.model), nil
	}

	// Group skills by SkillPath, then materialize ancestor nodes.
	byPath := map[string][]*skills.Skill{}
	for _, s := range all {
		path := SkillPath(s)
		byPath[path] = append(byPath[path], s)
	}

	// Collect every path mentioned, including all ancestors of leaf paths.
	pathSet := map[string]bool{Root: true}
	for path := range byPath {
		for _, anc := range AncestorPaths(path) {
			pathSet[anc] = true
		}
	}

	// Sort paths by depth then lexically for deterministic output.
	paths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		di := len(SplitPath(paths[i]))
		dj := len(SplitPath(paths[j]))
		if di != dj {
			return di < dj
		}
		return paths[i] < paths[j]
	})

	// Build nodes; resolve children via the path map afterward.
	nodeByPath := map[string]*skills.SkillIndexNode{}
	for _, p := range paths {
		segments := SplitPath(p)
		title := "All Skills"
		if len(segments) > 0 {
			title = humanizeSegment(segments[len(segments)-1])
		}
		attached := byPath[p]
		refs := make([]string, 0, len(attached))
		for _, s := range attached {
			refs = append(refs, s.Name)
		}
		sort.Strings(refs)

		// Skill index trees are bounded by the namespace depth in
		// parent_index_path; in practice <10 levels. The int32 conversion
		// is safe; the explicit clamp is for static analysis.
		depth := len(segments)
		if depth > int(^int32(0)>>1) {
			depth = int(^int32(0) >> 1)
		}
		node := &skills.SkillIndexNode{
			ID:          NodeID(p),
			Title:       title,
			Depth:       int32(depth),
			SkillRefs:   refs,
			ContentHash: ContentHash(attached),
		}
		nodeByPath[p] = node
	}

	// Wire children pointers: for each non-root path, append its id to its
	// parent's Children list.
	for _, p := range paths {
		if p == Root {
			continue
		}
		parent := nodeByPath[Parent(p)]
		parent.Children = append(parent.Children, nodeByPath[p].ID)
	}
	for _, n := range nodeByPath {
		sort.Strings(n.Children)
	}

	// Walk paths deepest-first to author summaries (children-aware).
	deepFirst := append([]string(nil), paths...)
	sort.Slice(deepFirst, func(i, j int) bool {
		di := len(SplitPath(deepFirst[i]))
		dj := len(SplitPath(deepFirst[j]))
		if di != dj {
			return di > dj
		}
		return deepFirst[i] < deepFirst[j]
	})
	for _, p := range deepFirst {
		node := nodeByPath[p]
		summary, err := b.summarize(ctx, p, node, byPath[p], nodeByPath)
		if err != nil {
			b.logger.Warn("skill-index: summary generation failed; using fallback",
				zap.String("path", p),
				zap.Error(err))
			summary = fallbackSummary(p, node, byPath[p], nodeByPath)
		}
		node.Summary = summary
	}

	// Order nodes by depth then id for stable persistence.
	out := make([]*skills.SkillIndexNode, 0, len(nodeByPath))
	for _, n := range nodeByPath {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Depth != out[j].Depth {
			return out[i].Depth < out[j].Depth
		}
		return out[i].ID < out[j].ID
	})

	idx := &skills.SkillIndex{
		RootID:       NodeID(Root),
		Nodes:        out,
		BuiltAtMs:    time.Now().UnixMilli(),
		BuiltByModel: b.model,
	}
	idx.ID = computeIndexID(idx)
	return idx, nil
}

// summarize generates a short router hint for a node. Uses the LLM when one
// is configured; falls back to a deterministic synthesis otherwise. The
// child summaries for intermediate nodes are pulled from already-built
// children (deep-first walk in Build).
func (b *Builder) summarize(ctx context.Context, path string,
	node *skills.SkillIndexNode, attached []*skills.Skill,
	all map[string]*skills.SkillIndexNode) (string, error) {
	if b.llm == nil {
		return fallbackSummary(path, node, attached, all), nil
	}
	prompt := buildSummaryPrompt(path, node, attached, all)
	messages := []types.Message{{Role: "user", Content: prompt}}
	resp, err := b.llm.Chat(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return cleanSummary(resp.Content), nil
}

// buildSummaryPrompt asks the LLM to write a 1-2 sentence summary describing
// the kinds of work the subtree covers. The prompt is intentionally tight
// because thousands of summary calls add up at scale.
func buildSummaryPrompt(path string, node *skills.SkillIndexNode,
	attached []*skills.Skill, all map[string]*skills.SkillIndexNode) string {
	var b strings.Builder
	b.WriteString("Write a 1-2 sentence summary of the kinds of skills this index node holds.\n")
	b.WriteString("The summary will be shown to a routing LLM that picks subtrees to descend.\n")
	b.WriteString("Be concrete: name domains, tasks, or technologies. No marketing language.\n")
	b.WriteString("Output the summary as plain text only, no JSON, no markdown headings.\n\n")

	if path == Root {
		b.WriteString("Path: (root)\n")
	} else {
		fmt.Fprintf(&b, "Path: %s\n", path)
	}
	if node.Title != "" {
		fmt.Fprintf(&b, "Title: %s\n", node.Title)
	}

	if len(attached) > 0 {
		b.WriteString("\nSkills directly under this node:\n")
		for _, s := range attached {
			t := s.Title
			if t == "" {
				t = s.Name
			}
			fmt.Fprintf(&b, "- %s: %s\n", t, truncate(s.Description, 200))
		}
	}

	if len(node.Children) > 0 {
		b.WriteString("\nChild subtrees:\n")
		for _, childID := range node.Children {
			for _, child := range all {
				if child.ID == childID {
					fmt.Fprintf(&b, "- %s: %s\n", child.Title, truncate(child.Summary, 160))
					break
				}
			}
		}
	}

	return b.String()
}

// fallbackSummary builds a deterministic summary when no LLM is configured
// or when the LLM call fails. Mentions attached skills then child branches.
func fallbackSummary(path string, node *skills.SkillIndexNode,
	attached []*skills.Skill, all map[string]*skills.SkillIndexNode) string {
	var b strings.Builder
	if path == Root {
		b.WriteString("Top-level skill catalog. ")
	} else {
		fmt.Fprintf(&b, "%s skills. ", node.Title)
	}
	if len(attached) > 0 {
		titles := make([]string, 0, len(attached))
		for _, s := range attached {
			t := s.Title
			if t == "" {
				t = s.Name
			}
			titles = append(titles, t)
		}
		sort.Strings(titles)
		const maxList = 5
		if len(titles) > maxList {
			fmt.Fprintf(&b, "Includes: %s, and %d more.",
				strings.Join(titles[:maxList], "; "), len(titles)-maxList)
		} else {
			fmt.Fprintf(&b, "Includes: %s.", strings.Join(titles, "; "))
		}
	}
	if len(node.Children) > 0 {
		childTitles := make([]string, 0, len(node.Children))
		for _, childID := range node.Children {
			for _, child := range all {
				if child.ID == childID {
					childTitles = append(childTitles, child.Title)
					break
				}
			}
		}
		sort.Strings(childTitles)
		fmt.Fprintf(&b, " Subtopics: %s.", strings.Join(childTitles, ", "))
	}
	return strings.TrimSpace(b.String())
}

// cleanSummary trims whitespace and strips simple markdown wrappers an LLM
// might add despite the prompt asking for plain text.
func cleanSummary(s string) string {
	s = strings.TrimSpace(s)
	for _, fence := range []string{"```", "~~~"} {
		if strings.HasPrefix(s, fence) {
			if idx := strings.Index(s[len(fence):], "\n"); idx >= 0 {
				s = s[len(fence)+idx+1:]
			} else {
				s = strings.TrimPrefix(s, fence)
			}
		}
		s = strings.TrimSuffix(s, fence)
	}
	return strings.TrimSpace(s)
}

// truncate returns at most n bytes of s with an ellipsis suffix when cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// humanizeSegment turns a kebab/snake path segment into a Title Case label
// suitable for router display. "data-quality" -> "Data Quality".
func humanizeSegment(seg string) string {
	if seg == "" {
		return ""
	}
	parts := strings.FieldsFunc(seg, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// computeIndexID hashes the node-level content hashes plus the model name
// to produce a stable id. This is what Cache and Store key on.
func computeIndexID(idx *skills.SkillIndex) string {
	h := sha256.New()
	for _, n := range idx.Nodes {
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00", n.ID, n.ContentHash)
	}
	_, _ = fmt.Fprintf(h, "%s\x00", idx.BuiltByModel)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func emptyIndex(model string) *skills.SkillIndex {
	root := &skills.SkillIndexNode{
		ID:    NodeID(Root),
		Title: "All Skills",
		Depth: 0,
	}
	idx := &skills.SkillIndex{
		RootID:       root.ID,
		Nodes:        []*skills.SkillIndexNode{root},
		BuiltAtMs:    time.Now().UnixMilli(),
		BuiltByModel: model,
	}
	idx.ID = computeIndexID(idx)
	return idx
}
