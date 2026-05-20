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

// Package importer converts Anthropic-style Agent Skill source directories
// (`<name>/SKILL.md` + `references/*.md`) into loom/v1 Skill YAML the
// loader can read.
//
// Two callers consume this package today:
//   - cmd/loom: the `loom skills import` CLI command (in-process).
//   - pkg/server: the SkillsImportService gRPC server (when a remote
//     server runs the importer on behalf of a client).
//
// The split is deliberate: the package is purely transformational
// (input → output), with no network, no LLM, no router-rebuild side
// effects. Those live in the caller. Phases 1–5 of the import pipeline
// are exposed as discrete functions so the gRPC server can drive them
// while streaming progress events; the CLI consumes the same pipeline
// via a callback adapter.
package importer

// Frontmatter is the minimal Anthropic-style Agent Skill frontmatter the
// importer reads from a SKILL.md.
type Frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Metadata    struct {
		Author  string `yaml:"author"`
		Version string `yaml:"version"`
	} `yaml:"metadata"`
}

// Skill is the intermediate form produced from one source directory
// before it is rendered into a loom/v1 Skill YAML.
//
// Field semantics:
//   - Name/Description/Author/Version mirror the SKILL.md frontmatter.
//   - Body holds the SKILL.md body with frontmatter stripped.
//   - References holds inlined references/*.md, sorted by filename so
//     output is deterministic.
//   - WhenToUse holds bullet points parsed from a "## When to Use"
//     section if one exists; used as a high-quality keyword source.
//   - LinkedSkills holds names found in cross-skill markdown links and
//     inline backtick code spans, deduped, excluding self. Includes
//     names that DO NOT resolve to importable skills — that's the
//     parent-index prompt body's full cross-reference list.
//   - ResolvedRefs is the subset of LinkedSkills whose names resolve
//     against the catalog the importer is producing in this run; used
//     for the skill_refs YAML field (loader-capped at 3).
//   - IsParentIndex is true when the skill name ends in "-skill-index";
//     such skills get mode: ALWAYS and special prompt handling.
//   - ParentIndexPath is the LLM-assigned hierarchical router path
//     (e.g., "teradata/performance"). Empty falls back to
//     unclassified/<domain> via SkillPath() at index-build time.
type Skill struct {
	Name        string
	Description string
	Author      string
	Version     string

	Body            string
	References      []Reference
	WhenToUse       []string
	LinkedSkills    []string
	ResolvedRefs    []string
	IsParentIndex   bool
	ParentIndexPath string
}

// Reference is one entry from a skill's references/ directory.
type Reference struct {
	Filename string // e.g. "transaction-and-session.md"
	Title    string // human-readable title (filename minus extension, prettified)
	Body     string // full markdown body (no frontmatter expected)
}
