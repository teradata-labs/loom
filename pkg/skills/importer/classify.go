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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/types"
)

// classifyTimeout caps each classification LLM call. The fallback path
// (legacy unclassified/<domain>) takes over on timeout so the importer
// doesn't hang on a flaky provider.
const classifyTimeout = 30 * time.Second

// GraphContext describes the current state of the live skill router's
// tree. The graph-aware classifier (ClassifyAgainstGraph) presents this
// context to the LLM so a NEW skill being added to a populated catalog
// joins existing buckets where possible, instead of inventing parallel
// siblings the stateless Classify variant might pick.
//
// GraphContext is constructed by the gRPC server from the persisted
// index in pkg/skills/index.Store. The CLI's offline --classify flow
// does NOT populate this — it uses the stateless Classify variant.
type GraphContext struct {
	// Buckets are the parent_index_path nodes that already hold at
	// least one skill. Order is not significant; the prompt builder
	// sorts deterministically.
	Buckets []GraphBucket
}

// GraphBucket is one node in the live router tree.
type GraphBucket struct {
	// Path is the parent_index_path (e.g., "teradata/performance").
	Path string
	// Members are the skill names already attached to this bucket.
	// Empty slice is permitted and surfaces to the prompt as
	// "(empty bucket)" so the LLM understands the bucket exists but
	// is unpopulated.
	Members []string
}

// Classify asks the LLM to assign a parent_index_path to the supplied
// skill from the seed taxonomy for its domain. The LLM may propose a
// new sibling under the same domain root if none of the suggestions
// fit; the validator rejects hallucinated unrelated top-level roots.
//
// Returns the chosen path, or a non-nil error when classification fails
// (LLM error, timeout, validator reject). Callers should treat errors
// as soft failures and fall back to leaving parent_index_path empty —
// the skill then lands at unclassified/<domain> via SkillPath() at
// index-build time.
//
// This is the stateless variant: each call sees only the supplied skill
// plus the seed taxonomy, with no awareness of other skills already in
// the catalog. Used by the CLI's --classify flag (offline import) where
// no live catalog state is available.
//
// For the server-side path that wants graph-aware bucketing, see
// ClassifyAgainstGraph.
func Classify(ctx context.Context, llm types.LLMProvider, imp *Skill, domain string, taxonomy Taxonomy) (string, error) {
	return classify(ctx, llm, imp, domain, taxonomy, GraphContext{})
}

// ClassifyAgainstGraph is the graph-aware classification entry point.
// The LLM sees both the seed taxonomy AND the live router tree
// (existing buckets + per-bucket members), so a new skill being added
// to a populated catalog tends to join existing buckets where possible
// rather than inventing parallel siblings.
//
// Same prompt builder + validator as Classify; differs only in the
// graph context appended to the prompt.
func ClassifyAgainstGraph(
	ctx context.Context,
	llm types.LLMProvider,
	imp *Skill,
	domain string,
	taxonomy Taxonomy,
	graph GraphContext,
) (string, error) {
	return classify(ctx, llm, imp, domain, taxonomy, graph)
}

// classify is the shared implementation. graph.Buckets being empty
// reduces to the stateless behavior automatically (the prompt simply
// omits the "current graph" section).
func classify(
	ctx context.Context,
	llm types.LLMProvider,
	imp *Skill,
	domain string,
	taxonomy Taxonomy,
	graph GraphContext,
) (string, error) {
	if llm == nil {
		return "", fmt.Errorf("classify: LLM provider required")
	}
	if imp == nil {
		return "", fmt.Errorf("classify: skill required")
	}
	suggestions := taxonomy.BucketsFor(domain)

	prompt := buildClassifyPrompt(imp, domain, suggestions, graph)
	cctx, cancel := context.WithTimeout(ctx, classifyTimeout)
	defer cancel()

	resp, err := llm.Chat(cctx, []types.Message{{Role: "user", Content: prompt}}, nil)
	if err != nil {
		return "", fmt.Errorf("classify: LLM call failed: %w", err)
	}
	path, parseErr := ParseClassifyResponse(resp.Content, domain)
	if parseErr != nil {
		return "", fmt.Errorf("classify: %w", parseErr)
	}
	return path, nil
}

// buildClassifyPrompt asks the LLM for a single parent_index_path under
// a fixed root. The format is constrained tightly because we're going
// to validate the response and reject anything outside the domain.
//
// When suggestions is empty, the prompt falls back to a generic
// <domain>/<topic> placeholder so the LLM knows it must propose its own
// bucket name — the validator still enforces "must start with <domain>/".
//
// When graph.Buckets is non-empty, the prompt also lists the live
// catalog state (existing bucket paths + their member skills) so the
// LLM can prefer joining a populated bucket over creating a new sibling.
func buildClassifyPrompt(imp *Skill, domain string, suggestions []TaxonomyBucket, graph GraphContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Assign a hierarchical parent_index_path for this skill, rooted at %q.\n", domain)
	b.WriteString("The path drives a routing tree: a router uses it to find the right skill for a user message.\n\n")

	b.WriteString("Constraints:\n")
	fmt.Fprintf(&b, "- The path MUST start with %q.\n", domain+"/")
	b.WriteString("- Use 2 segments total when possible (e.g., teradata/performance). Avoid going deeper than 3.\n")
	b.WriteString("- Prefer one of the suggested buckets when it fits the skill.\n")
	b.WriteString("- Propose a NEW sibling bucket only when none of the suggestions are a clear fit.\n")
	b.WriteString("- Use kebab-case lowercase segments (no spaces, no underscores).\n")
	if len(graph.Buckets) > 0 {
		b.WriteString("- Prefer joining a bucket that already holds related skills (see \"Current catalog\" below) over inventing a parallel sibling.\n")
	}
	b.WriteString("\n")

	b.WriteString("Suggested buckets:\n")
	if len(suggestions) > 0 {
		for _, s := range suggestions {
			if s.Description != "" {
				fmt.Fprintf(&b, "  - %s — %s\n", s.Path, s.Description)
			} else {
				fmt.Fprintf(&b, "  - %s\n", s.Path)
			}
		}
	} else {
		fmt.Fprintf(&b, "  - %s/<topic>  (no curated taxonomy for this domain; propose your own)\n", domain)
	}

	if len(graph.Buckets) > 0 {
		b.WriteString("\nCurrent catalog (live router tree under this domain):\n")
		// Stable sort: alphabetical by path so the prompt is deterministic.
		paths := make([]string, 0, len(graph.Buckets))
		byPath := make(map[string]GraphBucket, len(graph.Buckets))
		for _, gb := range graph.Buckets {
			paths = append(paths, gb.Path)
			byPath[gb.Path] = gb
		}
		// Use a stable sort without importing sort: small N, simple insertion.
		for i := 1; i < len(paths); i++ {
			for j := i; j > 0 && paths[j] < paths[j-1]; j-- {
				paths[j], paths[j-1] = paths[j-1], paths[j]
			}
		}
		for _, p := range paths {
			gb := byPath[p]
			if len(gb.Members) == 0 {
				fmt.Fprintf(&b, "  - %s (empty bucket)\n", p)
				continue
			}
			fmt.Fprintf(&b, "  - %s: %s\n", p, strings.Join(gb.Members, ", "))
		}
	}

	fmt.Fprintf(&b, "\nSkill to classify:\n")
	fmt.Fprintf(&b, "  name: %s\n", imp.Name)
	fmt.Fprintf(&b, "  description: %s\n", truncateClassify(imp.Description, 400))
	if len(imp.WhenToUse) > 0 {
		b.WriteString("  when-to-use:\n")
		for _, w := range imp.WhenToUse {
			fmt.Fprintf(&b, "    - %s\n", truncateClassify(w, 200))
		}
	}

	b.WriteString("\nRespond with a single JSON object: {\"path\":\"<chosen-path>\",\"reason\":\"<short>\"}\n")
	b.WriteString("Output ONLY the JSON object. No markdown fences.\n")
	return b.String()
}

// ParseClassifyResponse extracts a single path from the LLM's JSON
// response and validates it. Returns the canonical path (trimmed) on
// success.
//
// Rejection rules:
//   - Path must start with "<domain>/" (or equal "<domain>"). Hallucinated
//     top-levels (e.g., "general/foo" when domain="teradata") rejected.
//   - No prefix collisions: "teradatacloud/foo" rejected when
//     domain="teradata".
//   - All segments lowercase kebab-case (no spaces, no underscores).
//   - No empty segments.
//   - Tolerates LLM quirks: markdown fences, leading prose, surrounding
//     slashes.
func ParseClassifyResponse(raw, domain string) (string, error) {
	cleaned := stripJSONFences(raw)
	var d struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(cleaned), &d); err != nil {
		return "", fmt.Errorf("JSON parse failed: %w (first 200: %s)", err, truncateClassify(cleaned, 200))
	}
	path := strings.Trim(strings.TrimSpace(d.Path), "/")
	if path == "" {
		return "", fmt.Errorf("LLM returned empty path")
	}
	if path != domain && !strings.HasPrefix(path, domain+"/") {
		return "", fmt.Errorf("path %q does not start with domain root %q/", path, domain)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			return "", fmt.Errorf("empty segment in path %q", path)
		}
		if seg != strings.ToLower(seg) {
			return "", fmt.Errorf("non-lowercase segment %q in path %q", seg, path)
		}
		if strings.ContainsAny(seg, " _") {
			return "", fmt.Errorf("invalid characters in segment %q in path %q", seg, path)
		}
	}
	return path, nil
}

// stripJSONFences removes common LLM quirks: markdown fences and
// surrounding prose. Mirrors extractJSON in pkg/skills/index/router.go
// but kept local to avoid leaking router internals into the importer.
func stripJSONFences(raw string) string {
	s := strings.TrimSpace(raw)
	for _, fence := range []string{"```json", "```JSON", "```", "~~~"} {
		s = strings.TrimPrefix(s, fence)
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSuffix(s, "~~~")
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 && j < len(s)-1 {
		s = s[:j+1]
	}
	return s
}

// truncateClassify caps a string at n bytes.
func truncateClassify(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
