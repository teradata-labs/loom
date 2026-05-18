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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teradata-labs/loom/pkg/skills"
	"gopkg.in/yaml.v3"
)

// skillsImportCmd converts Anthropic-style Agent Skill directories
// (<name>/SKILL.md + references/*.md) into loom/v1 Skill YAML files
// the Loom skill library can load.
var skillsImportCmd = &cobra.Command{
	Use:   "import <src-dir>",
	Short: "Import Anthropic-style Agent Skill directories into loom/v1 YAML",
	Long: `Walks <src-dir> for subdirectories containing a SKILL.md file and
converts each one into a single loom/v1 Skill YAML, written to --out
(default: $LOOM_SKILLS_DIR, falling back to $HOME/.loom/skills).

Reference markdown files under <skill>/references/ are inlined into the
generated prompt.instructions under labeled "## Reference: <basename>"
sections. Cross-skill markdown links of the form
"[name](../<other>/SKILL.md)" are extracted into skill_refs (capped at 2
to satisfy the loader). Trigger keywords are synthesized from the skill
name and any "## When to Use" bullets.

Special cases:
  - <name>/SKILL.md ending in "-skill-index" becomes a parent-index meta
    skill (mode: ALWAYS, domain: meta-agent). Its skill_refs are dropped
    in the prompt body since the loader caps skill_refs at 2.
  - "agent-skill-builder" is skipped (meta-tooling for authoring skills).

Examples:

  # Default destination (~/.loom/skills):
  loom skills import ~/Projects/skills

  # Pick a different output dir:
  loom skills import ~/Projects/skills --out ./skills

  # Dry run (write nothing, list what would be produced):
  loom skills import ~/Projects/skills --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillsImport,
}

var (
	skillsImportOutDir   string
	skillsImportDryRun   bool
	skillsImportOverride bool
)

func init() {
	skillsImportCmd.Flags().StringVar(&skillsImportOutDir, "out", "",
		"output directory (default: $LOOM_SKILLS_DIR or $HOME/.loom/skills)")
	skillsImportCmd.Flags().BoolVar(&skillsImportDryRun, "dry-run", false,
		"print what would be written without touching the filesystem")
	skillsImportCmd.Flags().BoolVar(&skillsImportOverride, "force", false,
		"overwrite existing destination YAML files")
}

// frontmatter is the minimal Anthropic-style Agent Skill frontmatter.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Metadata    struct {
		Author  string `yaml:"author"`
		Version string `yaml:"version"`
	} `yaml:"metadata"`
}

// importedSkill is the intermediate form produced from one source directory
// before it is rendered into a loom/v1 Skill.
type importedSkill struct {
	Name        string
	Description string
	Author      string
	Version     string

	Body          string   // SKILL.md body (frontmatter stripped)
	References    []refDoc // references/*.md, sorted by filename
	WhenToUse     []string // bullets parsed from a "## When to Use" section
	LinkedSkills  []string // de-duplicated skill names referenced via markdown links
	IsParentIndex bool     // true when name ends in "-skill-index"
}

type refDoc struct {
	Filename string // e.g. "transaction-and-session.md"
	Title    string // human-readable title (filename minus extension, prettified)
	Body     string // full markdown body (no frontmatter expected)
}

func runSkillsImport(_ *cobra.Command, args []string) error {
	srcRoot := filepath.Clean(args[0])
	outDir, err := resolveImportOutDir(skillsImportOutDir)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		return fmt.Errorf("read source dir %s: %w", srcRoot, err)
	}

	if !skillsImportDryRun {
		if err := os.MkdirAll(outDir, 0o750); err != nil {
			return fmt.Errorf("create out dir %s: %w", outDir, err)
		}
	}

	var converted, skipped, failed int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillDir := filepath.Join(srcRoot, e.Name())
		skillPath := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			continue
		}
		if isSkippedSkill(e.Name()) {
			fmt.Fprintf(os.Stderr, "skip %s (meta-tooling, not convertible)\n", e.Name())
			skipped++
			continue
		}

		imp, err := readImportedSkill(skillDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fail %s: %v\n", e.Name(), err)
			failed++
			continue
		}

		// Reject any frontmatter name that could escape the output dir
		// before it ever reaches a filesystem path. The loader will reject
		// non-kebab-case names downstream too, but we want defense in depth
		// here because the name is a path component below.
		if !safeSkillName(imp.Name) {
			fmt.Fprintf(os.Stderr, "fail %s: unsafe skill name %q\n", e.Name(), imp.Name)
			failed++
			continue
		}

		yamlBytes, err := renderSkillYAML(imp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fail %s: render: %v\n", e.Name(), err)
			failed++
			continue
		}

		// Validate by round-tripping through the real loader so callers get
		// the same errors they'd see at runtime if we didn't catch them now.
		tmpPath := filepath.Join(os.TempDir(), filepath.Base(imp.Name)+".yaml")
		tmpPath = filepath.Clean(tmpPath)
		if err := os.WriteFile(tmpPath, yamlBytes, 0o600); err != nil { //nolint:gosec // G306/G703: path is filepath.Clean(temp + safe-name)
			fmt.Fprintf(os.Stderr, "fail %s: tmp write: %v\n", e.Name(), err)
			failed++
			continue
		}
		if _, err := skills.LoadSkill(tmpPath); err != nil {
			_ = os.Remove(tmpPath) //nolint:gosec // G703: same cleaned tmp path
			fmt.Fprintf(os.Stderr, "fail %s: validate: %v\n", e.Name(), err)
			failed++
			continue
		}
		_ = os.Remove(tmpPath) //nolint:gosec // G703: same cleaned tmp path

		dst := filepath.Clean(filepath.Join(outDir, filepath.Base(imp.Name)+".yaml"))
		if skillsImportDryRun {
			fmt.Printf("would write %s (%d bytes, %d refs, %d linked skills)\n",
				dst, len(yamlBytes), len(imp.References), len(imp.LinkedSkills))
			converted++
			continue
		}
		if _, err := os.Stat(dst); err == nil && !skillsImportOverride { //nolint:gosec // G304/G703: dst is filepath.Clean(outDir + safe-name)
			fmt.Fprintf(os.Stderr, "skip %s: %s exists (use --force to overwrite)\n", e.Name(), dst)
			skipped++
			continue
		}
		if err := os.WriteFile(dst, yamlBytes, 0o600); err != nil { //nolint:gosec // G306/G703: dst is filepath.Clean(outDir + safe-name)
			fmt.Fprintf(os.Stderr, "fail %s: write: %v\n", e.Name(), err)
			failed++
			continue
		}
		fmt.Printf("wrote %s\n", dst)
		converted++
	}

	fmt.Fprintf(os.Stderr, "\nimport summary: %d converted, %d skipped, %d failed\n",
		converted, skipped, failed)
	if failed > 0 {
		return fmt.Errorf("%d skill(s) failed to convert", failed)
	}
	return nil
}

// resolveImportOutDir picks the output directory in this order:
// explicit --out, LOOM_SKILLS_DIR, $HOME/.loom/skills.
func resolveImportOutDir(flagValue string) (string, error) {
	if flagValue != "" {
		return filepath.Clean(flagValue), nil
	}
	if env := os.Getenv("LOOM_SKILLS_DIR"); env != "" {
		return filepath.Clean(env), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".loom", "skills"), nil
}

// isSkippedSkill returns true for source skills the importer intentionally
// does not convert. Currently: agent-skill-builder is meta-tooling for
// authoring SKILL.md files and has no Loom-side equivalent.
func isSkippedSkill(name string) bool {
	return name == "agent-skill-builder"
}

// safeSkillNameRegex matches the same kebab-case shape the loader requires.
// It MUST be applied before using a skill name as a filesystem path component
// so a hostile SKILL.md frontmatter cannot escape the output directory via
// "../" or absolute paths.
var safeSkillNameRegex = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

func safeSkillName(name string) bool {
	if name == "" {
		return false
	}
	return safeSkillNameRegex.MatchString(name)
}

// readImportedSkill loads SKILL.md plus references for one source directory.
func readImportedSkill(skillDir string) (*importedSkill, error) {
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

	imp := &importedSkill{
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
func splitFrontmatter(raw []byte) (frontmatter, string, error) {
	idxs := frontmatterDelim.FindAllIndex(raw, 3)
	if len(idxs) < 2 {
		return frontmatter{}, "", fmt.Errorf("no YAML frontmatter delimited by --- found")
	}
	// idxs[0] is the opening "---", idxs[1] is the closing "---".
	yamlStart := idxs[0][1]
	yamlEnd := idxs[1][0]
	bodyStart := idxs[1][1]
	if yamlStart >= yamlEnd {
		return frontmatter{}, "", fmt.Errorf("malformed frontmatter delimiters")
	}

	var fm frontmatter
	if err := yaml.Unmarshal(raw[yamlStart:yamlEnd], &fm); err != nil {
		return frontmatter{}, "", fmt.Errorf("unmarshal frontmatter: %w", err)
	}
	body := strings.TrimLeft(string(raw[bodyStart:]), "\n")
	return fm, body, nil
}

// readReferences loads every references/*.md file in the skill directory,
// sorted by filename so output is deterministic.
func readReferences(skillDir string) ([]refDoc, error) {
	refDir := filepath.Join(skillDir, "references")
	entries, err := os.ReadDir(refDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var docs []refDoc
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
		docs = append(docs, refDoc{
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

// whenToUseHeading matches the start of a "When to Use" section in a SKILL.md.
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
	// Find the next "##" heading after the When to Use heading.
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
// relative paths that point at a sibling Agent Skill.
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

// renderSkillYAML produces a loom/v1 Skill YAML document for one importedSkill.
// The shape mirrors what skills.LoadSkill expects so callers can validate by
// round-tripping through the loader. Multi-line fields use literal block
// scalars (`|`) so the output is human-readable.
func renderSkillYAML(imp *importedSkill) ([]byte, error) {
	if imp == nil || imp.Name == "" {
		return nil, fmt.Errorf("importedSkill missing name")
	}

	domain := chooseDomain(imp)
	mode := "AUTO"
	if imp.IsParentIndex {
		mode = "ALWAYS"
	}

	instructions := buildInstructions(imp)
	keywords := buildKeywords(imp)
	slashCmds := buildSlashCommands(imp)

	// skill_refs is loader-capped at 2; only emit the most relevant ones.
	// Parent-index skills carry their full routing table inline already, so
	// we leave skill_refs empty for them and let the prompt do the work.
	var skillRefs []string
	if !imp.IsParentIndex && len(imp.LinkedSkills) > 0 {
		max := len(imp.LinkedSkills)
		if max > 2 {
			max = 2
		}
		skillRefs = imp.LinkedSkills[:max]
	}

	version := imp.Version
	if version == "" {
		version = "1.0.0"
	}
	// SKILL.md often uses "1.0"; normalize to semver-ish.
	if !strings.Contains(version, ".") || strings.Count(version, ".") < 2 {
		version = version + ".0"
	}

	author := imp.Author
	if author == "" {
		author = "imported"
	}

	root := mappingNode(
		scalarKV("apiVersion", "loom/v1"),
		scalarKV("kind", "Skill"),
		mappingKV("metadata",
			scalarKV("name", imp.Name),
			scalarKV("title", deriveTitle(imp.Name)),
			multilineKV("description", imp.Description),
			scalarKV("version", version),
			scalarKV("domain", domain),
			scalarKV("author", author),
			mappingKV("labels",
				scalarKV("source", "agent-skill-import"),
				scalarKV("upstream", imp.Name),
			),
		),
		mappingKV("trigger",
			seqKV("slash_commands", slashCmds),
			seqKV("keywords", keywords),
			seqKV("intent_categories", nil),
			scalarKV("mode", mode),
			scalarFloatKV("min_confidence", 0.6),
		),
		mappingKV("prompt",
			literalKV("instructions", instructions),
		),
		mappingKV("tools",
			seqKV("required_tools", nil),
			seqKV("preferred_order", nil),
			seqKV("excluded_tools", nil),
			seqKV("mcp_servers", nil),
		),
		seqKV("pattern_refs", nil),
		seqKV("skill_refs", skillRefs),
	)

	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close encoder: %w", err)
	}
	return []byte(buf.String()), nil
}

// --- yaml.Node builders -----------------------------------------------------
// These keep renderSkillYAML readable. Each builder returns a (key, value)
// node pair that mappingNode flattens into a mapping's Content slice.

type kvPair struct{ K, V *yaml.Node }

func keyNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: s}
}

func scalarKV(k, v string) kvPair {
	return kvPair{K: keyNode(k), V: &yaml.Node{Kind: yaml.ScalarNode, Value: v}}
}

func scalarFloatKV(k string, v float64) kvPair {
	return kvPair{K: keyNode(k), V: &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!float",
		Value: fmt.Sprintf("%g", v),
	}}
}

func multilineKV(k, v string) kvPair {
	var style yaml.Style
	if strings.Contains(v, "\n") {
		style = yaml.LiteralStyle
	}
	return kvPair{K: keyNode(k), V: &yaml.Node{
		Kind:  yaml.ScalarNode,
		Style: style,
		Value: v,
	}}
}

func literalKV(k, v string) kvPair {
	return kvPair{K: keyNode(k), V: &yaml.Node{
		Kind:  yaml.ScalarNode,
		Style: yaml.LiteralStyle,
		Value: v,
	}}
}

func seqKV(k string, items []string) kvPair {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	if len(items) == 0 {
		seq.Style = yaml.FlowStyle
	}
	for _, it := range items {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: it})
	}
	return kvPair{K: keyNode(k), V: seq}
}

func mappingNode(pairs ...kvPair) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, p := range pairs {
		n.Content = append(n.Content, p.K, p.V)
	}
	return n
}

func mappingKV(k string, pairs ...kvPair) kvPair {
	return kvPair{K: keyNode(k), V: mappingNode(pairs...)}
}

// chooseDomain maps imported skills to the loader's allowed domain set.
// Anything starting with "teradata-" gets domain "teradata"; the parent
// index gets "meta-agent"; everything else falls back to "general".
func chooseDomain(imp *importedSkill) string {
	if imp.IsParentIndex {
		return "meta-agent"
	}
	if strings.HasPrefix(imp.Name, "teradata-") {
		return "teradata"
	}
	return "general"
}

// deriveTitle converts a kebab-case skill name to a Title Case string.
// "teradata-sql-fundamentals" -> "Teradata Sql Fundamentals".
func deriveTitle(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// buildInstructions concatenates the SKILL.md body with each reference under
// a labeled section. Cross-skill markdown links are normalized to bare names
// so the LLM does not chase ../<name>/SKILL.md paths that don't exist in
// Loom's filesystem.
func buildInstructions(imp *importedSkill) string {
	var b strings.Builder
	body := normalizeCrossSkillLinks(imp.Body)
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	if imp.IsParentIndex && len(imp.LinkedSkills) > 0 {
		b.WriteString("\n## Linked Skills (Loom Catalog)\n\n")
		b.WriteString("This index points at the following skills available in the Loom catalog. Use slash commands or natural language to activate them:\n\n")
		for _, n := range imp.LinkedSkills {
			fmt.Fprintf(&b, "- `%s`\n", n)
		}
	}
	for _, r := range imp.References {
		fmt.Fprintf(&b, "\n## Reference: %s\n\n", r.Title)
		b.WriteString(normalizeCrossSkillLinks(r.Body))
		if !strings.HasSuffix(r.Body, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// normalizeCrossSkillLinks rewrites "[label](../foo/SKILL.md)" to
// "[label](skill:foo)" so the rendered prompt does not promise filesystem
// paths that are not part of the converted output. The skill: prefix is a
// hint to a future renderer; today the LLM just sees the link target text.
func normalizeCrossSkillLinks(body string) string {
	return crossSkillLink.ReplaceAllStringFunc(body, func(match string) string {
		groups := crossSkillLink.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		// Preserve the link label.
		labelEnd := strings.Index(match, "]")
		if labelEnd <= 0 {
			return match
		}
		label := match[1:labelEnd]
		return fmt.Sprintf("[%s](skill:%s)", label, groups[1])
	})
}

// buildKeywords synthesizes trigger keywords from the skill name plus the
// "When to Use" bullets, returning a deduped list capped to a reasonable
// size so the trigger config does not bloat.
func buildKeywords(imp *importedSkill) []string {
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || len(s) < 3 || len(s) > 60 {
			return
		}
		seen[s] = true
	}

	// The skill name itself, plus the bare suffix after any "teradata-" prefix.
	add(imp.Name)
	if strings.HasPrefix(imp.Name, "teradata-") {
		add(strings.TrimPrefix(imp.Name, "teradata-"))
	}
	// Every "## When to Use" bullet, lightly cleaned.
	for _, bullet := range imp.WhenToUse {
		// Strip leading verbs like "Use when " or "Use for "
		clean := bullet
		clean = strings.TrimPrefix(clean, "Use when ")
		clean = strings.TrimPrefix(clean, "Use for ")
		clean = strings.TrimPrefix(clean, "Using ")
		// Cut at first comma or em-dash; the prefix is the most distinctive.
		if i := strings.IndexAny(clean, ",—"); i > 0 {
			clean = clean[:i]
		}
		add(clean)
	}

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

// buildSlashCommands derives one slash command from the skill name. We do
// not attempt to support multiple aliases here — authors can add them after
// import. The parent index gets a stable alias so it's easy to summon.
func buildSlashCommands(imp *importedSkill) []string {
	if imp.IsParentIndex {
		return []string{"/" + imp.Name, "/skill-index"}
	}
	return []string{"/" + imp.Name}
}
