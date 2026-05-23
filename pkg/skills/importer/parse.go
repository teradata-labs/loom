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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// safeSkillNameRegex matches the same kebab-case shape the loader requires.
// MUST be applied before using a skill name as a filesystem path component
// so a hostile SKILL.md frontmatter cannot escape the output directory via
// "../" or absolute paths.
var safeSkillNameRegex = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// IsSafeSkillName returns true when the supplied name matches the loader's
// kebab-case shape. Use this to gate any filesystem path construction
// derived from frontmatter input.
func IsSafeSkillName(name string) bool {
	if name == "" {
		return false
	}
	return safeSkillNameRegex.MatchString(name)
}

// IsSkippedSkill returns true for source skills the importer intentionally
// does not convert. Currently: agent-skill-builder is meta-tooling for
// authoring SKILL.md files and has no Loom-side equivalent.
func IsSkippedSkill(name string) bool {
	return name == "agent-skill-builder"
}

// ReadSkill loads SKILL.md plus references for one source directory.
//
// Frontmatter must contain at minimum a `name` field. References live under
// <skillDir>/references/*.md; absent dir is OK. Cross-skill markdown links
// and inline backtick names are extracted into Skill.LinkedSkills (deduped,
// self-excluded) for the caller to filter against the importable catalog.
func ReadSkill(skillDir string) (*Skill, error) {
	skillPath := filepath.Join(skillDir, "SKILL.md")
	raw, err := os.ReadFile(filepath.Clean(skillPath)) //nolint:gosec // user-supplied path is the documented contract
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", skillPath, err)
	}

	fm, body, err := splitFrontmatter(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", skillPath, err)
	}
	if fm.Name == "" {
		return nil, fmt.Errorf("%s: frontmatter missing name", skillPath)
	}

	imp := &Skill{
		Name:          fm.Name,
		Description:   fm.Description,
		Author:        fm.Metadata.Author,
		Version:       fm.Metadata.Version,
		Body:          strings.TrimSpace(body),
		IsParentIndex: strings.HasSuffix(fm.Name, "-skill-index"),
	}

	imp.WhenToUse = parseWhenToUseBullets(imp.Body)
	imp.LinkedSkills = extractLinkedSkillNames(imp.Body, imp.Name)

	refs, err := readReferences(skillDir)
	if err != nil {
		return nil, fmt.Errorf("read references for %s: %w", fm.Name, err)
	}
	imp.References = refs

	// Pull links from reference bodies too.
	for _, r := range refs {
		imp.LinkedSkills = append(imp.LinkedSkills, extractLinkedSkillNames(r.Body, imp.Name)...)
	}
	imp.LinkedSkills = dedupeSorted(imp.LinkedSkills)
	return imp, nil
}

// frontmatterDelim matches the Anthropic-style YAML frontmatter delimiter.
var frontmatterDelim = regexp.MustCompile(`(?m)^---\s*$`)

// splitFrontmatter pulls the YAML frontmatter out of a SKILL.md file.
// Returns the parsed struct, the markdown body that follows, and any error.
func splitFrontmatter(raw []byte) (Frontmatter, string, error) {
	idxs := frontmatterDelim.FindAllIndex(raw, 3)
	if len(idxs) < 2 {
		return Frontmatter{}, "", fmt.Errorf("no YAML frontmatter delimited by --- found")
	}
	yamlStart := idxs[0][1]
	yamlEnd := idxs[1][0]
	bodyStart := idxs[1][1]
	if yamlStart >= yamlEnd {
		return Frontmatter{}, "", fmt.Errorf("malformed frontmatter delimiters")
	}

	var fm Frontmatter
	if err := yaml.Unmarshal(raw[yamlStart:yamlEnd], &fm); err != nil {
		return Frontmatter{}, "", fmt.Errorf("unmarshal frontmatter: %w", err)
	}
	body := strings.TrimLeft(string(raw[bodyStart:]), "\n")
	return fm, body, nil
}

// readReferences loads every references/*.md file in the skill directory,
// sorted by filename so output is deterministic.
func readReferences(skillDir string) ([]Reference, error) {
	refDir := filepath.Join(skillDir, "references")
	entries, err := os.ReadDir(refDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var docs []Reference
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(refDir, e.Name())
		raw, err := os.ReadFile(filepath.Clean(path)) //nolint:gosec // path under user-supplied root
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		// Reference files may also start with frontmatter; strip if present.
		body := strings.TrimSpace(string(raw))
		if strings.HasPrefix(body, "---") {
			if _, stripped, err := splitFrontmatter([]byte(body)); err == nil {
				body = strings.TrimSpace(stripped)
			}
		}
		docs = append(docs, Reference{
			Filename: e.Name(),
			Title:    prettifyFilename(e.Name()),
			Body:     body,
		})
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Filename < docs[j].Filename })
	return docs, nil
}

// prettifyFilename converts "ppi-joins-and-optimization.md" to
// "Ppi Joins And Optimization".
func prettifyFilename(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.Split(base, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// whenToUseHeading matches the start of a "When to Use" section.
var whenToUseHeading = regexp.MustCompile(`(?mi)^##\s+When to Use\s*$`)

// nextHeading matches the first "##" heading after a given offset.
var nextHeading = regexp.MustCompile(`(?m)^##\s+`)

// bulletLine matches markdown bullet items.
var bulletLine = regexp.MustCompile(`(?m)^\s*[-*]\s+(.+?)\s*$`)

// parseWhenToUseBullets pulls bullet points out of the "## When to Use"
// section so we can synthesize trigger keywords. Returns nil when the
// section is absent.
func parseWhenToUseBullets(body string) []string {
	loc := whenToUseHeading.FindStringIndex(body)
	if loc == nil {
		return nil
	}
	rest := body[loc[1]:]
	if next := nextHeading.FindStringIndex(rest); next != nil {
		rest = rest[:next[0]]
	}
	matches := bulletLine.FindAllStringSubmatch(rest, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		line := strings.TrimSpace(m[1])
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// crossSkillLink matches "[anything](../<skill-name>/SKILL.md)" and similar
// relative paths that point at a sibling Agent Skill. Exported for
// renderer reuse (link normalization).
var crossSkillLink = regexp.MustCompile(`\[[^\]]+\]\((?:\.\./)?([a-z][a-z0-9-]*)/SKILL\.md(?:#[^)]+)?\)`)

// inlineSkillName matches occurrences of "`teradata-foo`" — the routing
// table in teradata-skill-index uses backtick code spans, not links.
var inlineSkillName = regexp.MustCompile("`(teradata-[a-z0-9-]+)`")

// extractLinkedSkillNames walks the body for cross-skill markdown links and
// inline backticked skill names, returning a deduped list excluding self.
func extractLinkedSkillNames(body, self string) []string {
	seen := map[string]bool{}
	for _, m := range crossSkillLink.FindAllStringSubmatch(body, -1) {
		if m[1] != self {
			seen[m[1]] = true
		}
	}
	for _, m := range inlineSkillName.FindAllStringSubmatch(body, -1) {
		if m[1] != self {
			seen[m[1]] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, s := range in {
		seen[s] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
