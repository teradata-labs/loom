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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/llm/factory"
	"github.com/teradata-labs/loom/pkg/types"
)

// classifyEnabled returns true when the user opted into LLM classification
// via the --classify flag.
var classifyEnabled bool

// classifyTimeout caps each classification LLM call. The fallback path
// (legacy unclassified/<domain>) takes over on timeout so the importer
// doesn't hang on a flaky provider.
const classifyTimeout = 30 * time.Second

// suggestedTaxonomies seed the LLM prompt with known parent_index_path
// buckets per domain. The classifier may propose new sibling paths under
// the same root when none of the suggested buckets fit, but it must not
// invent unrelated top-level roots — that keeps the resulting tree shallow.
//
// Domains here match `chooseDomain`'s output. Add new entries as more
// upstream skill libraries land.
var suggestedTaxonomies = map[string][]string{
	"teradata": {
		"teradata/performance",  // optimizer, statistics, partitioning, intelligent memory
		"teradata/security",     // RLAC, row-level access, encryption, auth
		"teradata/storage",      // NoPI tables, native object store, foreign tables
		"teradata/sql",          // SQL fundamentals, stored procedures, UDFs, data types
		"teradata/cloud",        // VantageCloud Lake, elasticity, capacity/consumption
		"teradata/admin",        // system admin, workload management, load isolation
		"teradata/ml",           // ML/graph engines, analytics functions
		"teradata/architecture", // AMPs, hashing, query banding
		"teradata/i18n",         // collation, character sets, locale-aware sorting
	},
}

// buildClassifyLLM constructs an LLM provider from environment variables.
// Returns nil with a non-nil error when classification is requested but the
// environment isn't configured. Returns (nil, nil) when classification was
// not requested — callers fall back to the legacy unclassified/<domain> path.
func buildClassifyLLM() (types.LLMProvider, error) {
	if !classifyEnabled {
		return nil, nil
	}
	provider := os.Getenv("LOOM_CLASSIFY_PROVIDER")
	if provider == "" {
		provider = os.Getenv("LOOM_DEFAULT_PROVIDER")
	}
	if provider == "" {
		return nil, fmt.Errorf("--classify requires LOOM_CLASSIFY_PROVIDER or LOOM_DEFAULT_PROVIDER " +
			"(supported: anthropic, bedrock, ollama, openai, azure-openai, mistral, gemini, huggingface)")
	}
	model := os.Getenv("LOOM_CLASSIFY_MODEL")

	cfg := factory.FactoryConfig{
		DefaultProvider: provider,
		DefaultModel:    model,
		// Provider creds are read from each provider's standard env vars
		// (ANTHROPIC_API_KEY, AWS_REGION + IAM, OPENAI_API_KEY, etc.) by
		// the factory's per-provider create* methods. Don't duplicate them
		// here — keep the importer's surface minimal.
		Temperature: 0.0, // classification is a deterministic task
	}
	f := factory.NewProviderFactory(cfg)

	raw, err := f.CreateProvider(provider, model)
	if err != nil {
		return nil, fmt.Errorf("create classify provider %q: %w", provider, err)
	}
	llm, ok := raw.(types.LLMProvider)
	if !ok {
		return nil, fmt.Errorf("classify provider %q does not implement types.LLMProvider", provider)
	}
	return llm, nil
}

// classifyProviderInfo returns a human-readable description of the
// configured classify provider for the import banner.
func classifyProviderInfo() string {
	provider := os.Getenv("LOOM_CLASSIFY_PROVIDER")
	if provider == "" {
		provider = os.Getenv("LOOM_DEFAULT_PROVIDER")
	}
	model := os.Getenv("LOOM_CLASSIFY_MODEL")
	if model == "" {
		model = "(provider default)"
	}
	return fmt.Sprintf("provider=%s model=%s", provider, model)
}

// classifyParentIndexPath asks the LLM to assign a parent_index_path to the
// imported skill from the suggested taxonomy for its domain. The LLM may
// propose a new sibling under the same domain root if none of the suggestions
// fit; it must not invent an unrelated top-level root.
//
// Returns the chosen path, or an empty string when classification fails or
// is disabled. Empty means "use the legacy unclassified/<domain> placement"
// computed by SkillPath() at index-build time.
func classifyParentIndexPath(ctx context.Context, llm types.LLMProvider,
	imp *importedSkill, domain string) string {
	if llm == nil || imp == nil {
		return ""
	}
	suggestions := suggestedTaxonomies[domain]
	if len(suggestions) == 0 {
		// Domain has no curated taxonomy yet; let the LLM propose a path
		// rooted at the domain. The validator below still enforces
		// "must start with <domain>/".
		suggestions = []string{domain + "/<topic>"}
	}

	prompt := buildClassifyPrompt(imp, domain, suggestions)
	cctx, cancel := context.WithTimeout(ctx, classifyTimeout)
	defer cancel()

	resp, err := llm.Chat(cctx, []types.Message{{Role: "user", Content: prompt}}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "classify %s: LLM call failed: %v (falling back to unclassified/%s)\n",
			imp.Name, err, domain)
		return ""
	}
	path, parseErr := parseClassifyResponse(resp.Content, domain)
	if parseErr != nil {
		fmt.Fprintf(os.Stderr, "classify %s: %v (falling back to unclassified/%s)\n",
			imp.Name, parseErr, domain)
		return ""
	}
	return path
}

// buildClassifyPrompt asks the LLM for a single parent_index_path under a
// fixed root. The format is constrained tightly because we're going to
// validate the response and reject anything outside the domain.
func buildClassifyPrompt(imp *importedSkill, domain string, suggestions []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Assign a hierarchical parent_index_path for this skill, rooted at \"%s\".\n", domain)
	b.WriteString("The path drives a routing tree: a router uses it to find the right skill for a user message.\n\n")

	b.WriteString("Constraints:\n")
	fmt.Fprintf(&b, "- The path MUST start with \"%s/\".\n", domain)
	b.WriteString("- Use 2 segments total when possible (e.g., teradata/performance). Avoid going deeper than 3.\n")
	b.WriteString("- Prefer one of the suggested buckets when it fits the skill.\n")
	b.WriteString("- Propose a NEW sibling bucket only when none of the suggestions are a clear fit.\n")
	b.WriteString("- Use kebab-case lowercase segments (no spaces, no underscores).\n\n")

	b.WriteString("Suggested buckets:\n")
	for _, s := range suggestions {
		fmt.Fprintf(&b, "  - %s\n", s)
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

// parseClassifyResponse extracts a single path from the LLM's JSON response
// and validates it. Returns the canonical path (trimmed) on success.
func parseClassifyResponse(raw, domain string) (string, error) {
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
	// Domain anchor: must start with the domain root. Reject hallucinated
	// top-level roots (e.g., "general/foo" when domain="teradata").
	if path != domain && !strings.HasPrefix(path, domain+"/") {
		return "", fmt.Errorf("path %q does not start with domain root %q/", path, domain)
	}
	// Segment hygiene: lowercase, kebab-case-ish.
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

// stripJSONFences removes common LLM quirks: markdown fences and surrounding
// prose. Mirrors extractJSON in pkg/skills/index/router.go but kept local to
// avoid leaking router internals into the importer.
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

// truncateClassify caps a string at n bytes; mirrored locally to keep this
// file self-contained.
func truncateClassify(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
