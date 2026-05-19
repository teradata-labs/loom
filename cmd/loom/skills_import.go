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
"[name](../<other>/SKILL.md)" are extracted into skill_refs (capped at 3
to satisfy the loader). Trigger keywords are synthesized from the skill
name and any "## When to Use" bullets.

Special cases:
  - <name>/SKILL.md ending in "-skill-index" becomes a parent-index meta
    skill (mode: ALWAYS, domain: meta-agent). Its skill_refs are dropped
    in the prompt body since the loader caps skill_refs at 3.
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
	skillsImportCmd.Flags().BoolVar(&classifyEnabled, "classify", false,
		"call an LLM to assign each skill a parent_index_path from a "+
			"per-domain taxonomy (improves router precision; requires "+
			"LOOM_CLASSIFY_PROVIDER plus the provider's standard creds)")
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

	Body            string   // SKILL.md body (frontmatter stripped)
	References      []refDoc // references/*.md, sorted by filename
	WhenToUse       []string // bullets parsed from a "## When to Use" section
	LinkedSkills    []string // skill names found via markdown links (unfiltered, for prompt body)
	ResolvedRefs    []string // subset of LinkedSkills that resolve to a known importable skill (for skill_refs YAML field)
	IsParentIndex   bool     // true when name ends in "-skill-index"
	ParentIndexPath string   // LLM-assigned hierarchical router path; empty falls back to unclassified/<domain>
}

type refDoc struct {
	Filename string // e.g. "transaction-and-session.md"
	Title    string // human-readable title (filename minus extension, prettified)
	Body     string // full markdown body (no frontmatter expected)
}

func runSkillsImport(cmd *cobra.Command, args []string) error {
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

	// Optional LLM classifier. Constructed once and reused across all
	// skills in this run so we don't re-pay client setup per call. nil
	// when --classify is unset OR when env vars are missing — see error
	// returned and stop loudly so the user knows their flag was ignored.
	classifier, err := buildClassifyLLM()
	if err != nil {
		return fmt.Errorf("classify setup: %w", err)
	}
	if classifier != nil {
		fmt.Fprintf(os.Stderr, "classifier enabled: %s\n", classifyProviderInfo())
	}
	var classifyCtx context.Context
	if cmd != nil {
		classifyCtx = cmd.Context()
	}
	if classifyCtx == nil {
		classifyCtx = context.Background()
	}

	// Pass 1: discover all importable skills so we can resolve skill_refs
	// against the actual catalog. This filters out cross-skill mentions
	// that look like skill names (e.g. `teradata-python-addons` is a Linux
	// rpm package name, not a Loom skill) but don't correspond to anything
	// the importer will produce.
	type pending struct {
		entry os.DirEntry
		dir   string
		skill *importedSkill
	}
	var skipped, failed int
	pendingSkills := make([]pending, 0, len(entries))
	knownNames := make(map[string]bool)
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
		if !safeSkillName(imp.Name) {
			fmt.Fprintf(os.Stderr, "fail %s: unsafe skill name %q\n", e.Name(), imp.Name)
			failed++
			continue
		}
		pendingSkills = append(pendingSkills, pending{entry: e, dir: skillDir, skill: imp})
		knownNames[imp.Name] = true
	}

	// Pass 2: render and write each skill. Filter LinkedSkills against
	// the known set so skill_refs only points at things we'll actually
	// import. The full LinkedSkills list is preserved on importedSkill
	// because the parent-index prompt body lists every cross-reference,
	// resolved or not.
	var converted int
	for _, p := range pendingSkills {
		imp, e := p.skill, p.entry
		resolved := make([]string, 0, len(imp.LinkedSkills))
		for _, name := range imp.LinkedSkills {
			if knownNames[name] {
				resolved = append(resolved, name)
			}
		}
		imp.ResolvedRefs = resolved

		// Classifier assigns a hierarchical parent_index_path (e.g.,
		// teradata/performance) so the router can sub-categorize within
		// a domain. Skipped for parent-index meta-skills (they live at
		// their own well-known location). Failures fall back to the
		// legacy unclassified/<domain> path computed by SkillPath().
		if classifier != nil && !imp.IsParentIndex {
			domain := chooseDomain(imp)
			path := classifyParentIndexPath(classifyCtx, classifier, imp, domain)
			imp.ParentIndexPath = path
			if path != "" {
				fmt.Fprintf(os.Stderr, "classify %s -> %s\n", imp.Name, path)
			}
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

	// skill_refs is loader-capped at 3; only emit the most relevant ones.
	// Parent-index skills carry their full routing table inline already, so
	// we leave skill_refs empty for them and let the prompt do the work.
	// We use ResolvedRefs (filtered against the known importable set) so
	// dangling references like Linux package names don't leak into the
	// generated YAML.
	var skillRefs []string
	if !imp.IsParentIndex && len(imp.ResolvedRefs) > 0 {
		maxRefs := len(imp.ResolvedRefs)
		if maxRefs > 3 {
			maxRefs = 3
		}
		skillRefs = imp.ResolvedRefs[:maxRefs]
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

	rootKVs := []kvPair{
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
	}
	// parent_index_path is loader-aware — when set, the index builder uses
	// it directly to place the skill in the routing tree. When empty, the
	// builder falls back to "unclassified/<domain>" via SkillPath(). Emit
	// the field only when a path was assigned to keep older YAMLs unchanged.
	if imp.ParentIndexPath != "" {
		rootKVs = append(rootKVs, scalarKV("parent_index_path", imp.ParentIndexPath))
	}
	root := mappingNode(rootKVs...)

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

// keywordStopwords contains common English filler words that make poor
// trigger keywords because they match too broadly.
var keywordStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true,
	"at": true, "be": true, "by": true, "for": true, "from": true,
	"has": true, "have": true, "in": true, "into": true, "is": true,
	"it": true, "its": true, "of": true, "on": true, "or": true,
	"over": true, "the": true, "this": true, "to": true, "use": true,
	"used": true, "using": true, "via": true, "was": true, "when": true,
	"with": true, "within": true, "without": true, "you": true, "your": true,
	"any": true, "all": true, "also": true, "but": true, "can": true,
	"do": true, "does": true, "if": true, "may": true, "must": true,
	"not": true, "should": true, "than": true, "that": true, "their": true,
	"them": true, "they": true, "what": true, "which": true, "while": true,
	"who": true, "why": true, "will": true, "would": true,
}

// codeSpan matches markdown inline code spans like `MLPPI` or `RANGE_N`.
var codeSpan = regexp.MustCompile("`([A-Za-z][A-Za-z0-9_./-]{1,40})`")

// capsAcronym matches all-caps tokens of length 2-12 (PPI, MLPPI, NUPI, NOS, JSON).
var capsAcronym = regexp.MustCompile(`\b([A-Z][A-Z0-9_]{1,11})\b`)

// quotedPhrase matches "double-quoted" multi-word phrases of up to 4 words.
// var quotedPhrase = regexp.MustCompile(`"([^"\n]{2,40})"`) // currently unused

// genericSQLKeywords are SQL DML/DDL verbs and structural words that
// appear all-caps in any SQL-related skill. They are not distinctive
// enough to route on, so we drop them from the all-caps acronym pass.
var genericSQLKeywords = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"MERGE": true, "CREATE": true, "DROP": true, "ALTER": true,
	"GRANT": true, "REVOKE": true, "REPLACE": true, "EXPLAIN": true,
	"FROM": true, "WHERE": true, "GROUP": true, "ORDER": true,
	"HAVING": true, "JOIN": true, "INNER": true, "OUTER": true,
	"LEFT": true, "RIGHT": true, "FULL": true, "UNION": true,
	"WITH": true, "CASE": true, "WHEN": true, "THEN": true,
	"ELSE": true, "END": true, "AND": true, "NOT": true,
	"NULL": true, "INTO": true, "VALUES": true, "SET": true,
	"AS": true, "BY": true, "OR": true, "ON": true, "IS": true,
	"DDL": true, "DML": true, "DCL": true,
	"HELP": true, "SHOW": true,
	"TRUE": true, "FALSE": true,
}

// commonShortWords are short tokens that look distinctive but appear too
// often in casual English to be useful as keyword routing signals. They
// get filtered even when they'd otherwise pass the length floor.
var commonShortWords = map[string]bool{
	"after": true, "alter": true, "build": true, "before": true,
	"check": true, "could": true, "every": true, "first": true,
	"given": true, "issue": true, "later": true, "level": true,
	"means": true, "might": true, "needs": true, "often": true,
	"order": true, "other": true, "place": true, "since": true,
	"still": true, "thing": true, "those": true, "under": true,
	"until": true, "where": true, "whose": true, "write": true,
	"based": true, "match": true, "items": true,
}

// kwCandidate carries a keyword candidate plus a priority score so we can
// rank by distinctiveness before truncating to the 32-entry cap.
type kwCandidate struct {
	value    string
	priority int // higher = more likely to be useful
}

// buildKeywords synthesizes trigger keywords for FTS5 routing. The matcher
// in pkg/skills/library.go FindByKeywords scores a hit when a keyword is
// either an exact tokenized match against the user message or a substring
// of it. That makes long phrases nearly useless and short, distinctive
// tokens highly valuable, so we emit:
//
//   - the skill name and its bare suffix (e.g. "partitioning")
//   - markdown inline code spans from the SKILL body (`MLPPI`, `RANGE_N`)
//   - all-caps acronyms from the SKILL body (PPI, NUPI, NOS, TASM)
//   - 2- to 3-word non-stopword phrases from each "When to Use" bullet
//   - distinctive single tokens (>= 6 chars, not a common English short word)
//
// Candidates are scored by source priority; the top 32 are emitted.
func buildKeywords(imp *importedSkill) []string {
	seen := map[string]int{} // value -> highest priority seen
	add := func(s string, priority int) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || len(s) < 3 || len(s) > 40 {
			return
		}
		single := !strings.Contains(s, " ")
		if single {
			if keywordStopwords[s] || commonShortWords[s] {
				return
			}
			// Reject single tokens that are mostly digits.
			if isAllDigits(s) {
				return
			}
		}
		if cur, ok := seen[s]; !ok || priority > cur {
			seen[s] = priority
		}
	}

	// Priority 100: skill name itself — always include.
	add(imp.Name, 100)
	if strings.HasPrefix(imp.Name, "teradata-") {
		add(strings.TrimPrefix(imp.Name, "teradata-"), 100)
	}

	// Priority 90: terms from the description. The description is the most
	// curated, distilled signal we have — its CAPS acronyms and ngrams are
	// almost always the right routing terms.
	for _, m := range capsAcronym.FindAllStringSubmatch(imp.Description, -1) {
		token := m[1]
		if len(token) < 3 || len(token) > 12 || hasDigit(token) ||
			genericSQLKeywords[strings.ToUpper(token)] {
			continue
		}
		add(token, 90)
	}
	descSegments := splitOnAny(stripParens(imp.Description), ",;:—-(/)")
	for _, seg := range descSegments {
		words := tokenizeWords(seg)
		for n := 2; n <= 3 && n <= len(words); n++ {
			for i := 0; i+n <= len(words); i++ {
				gram := words[i : i+n]
				if keywordStopwords[gram[0]] || keywordStopwords[gram[len(gram)-1]] {
					continue
				}
				add(strings.Join(gram, " "), 90)
			}
		}
	}

	// Priority 80: inline code spans from the SKILL body. We deliberately do
	// NOT mine reference bodies — they contain too many citations and
	// unrelated SQL keywords that pollute the keyword list.
	for _, m := range codeSpan.FindAllStringSubmatch(imp.Body, -1) {
		token := m[1]
		if strings.ContainsAny(token, "/.") {
			continue
		}
		add(token, 80)
	}

	// Priority 70: all-caps acronyms from the SKILL body. Capped via the
	// per-source budget below so a body with hundreds of acronym mentions
	// can't crowd out higher-quality bullet ngrams.
	for _, m := range capsAcronym.FindAllStringSubmatch(imp.Body, -1) {
		token := m[1]
		if len(token) < 3 || len(token) > 12 || hasDigit(token) ||
			genericSQLKeywords[strings.ToUpper(token)] {
			continue
		}
		add(token, 70)
	}

	// Priority 60: 2- and 3-word n-grams from "When to Use" bullets.
	for _, bullet := range imp.WhenToUse {
		clean := bullet
		clean = strings.TrimPrefix(clean, "Use when ")
		clean = strings.TrimPrefix(clean, "Use for ")
		clean = strings.TrimPrefix(clean, "Using ")
		clean = stripParens(clean)
		segments := splitOnAny(clean, ",;:—-(/)")
		for _, seg := range segments {
			words := tokenizeWords(seg)
			for n := 2; n <= 3 && n <= len(words); n++ {
				for i := 0; i+n <= len(words); i++ {
					gram := words[i : i+n]
					if keywordStopwords[gram[0]] || keywordStopwords[gram[len(gram)-1]] {
						continue
					}
					add(strings.Join(gram, " "), 60)
				}
			}
		}
	}

	// Rank by priority desc, then alphabetically for stable output.
	cands := make([]kwCandidate, 0, len(seen))
	for v, p := range seen {
		cands = append(cands, kwCandidate{value: v, priority: p})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].priority != cands[j].priority {
			return cands[i].priority > cands[j].priority
		}
		return cands[i].value < cands[j].value
	})

	const maxKeywords = 32
	if len(cands) > maxKeywords {
		cands = cands[:maxKeywords]
	}

	// Sort the final cut alphabetically so the YAML output is stable
	// regardless of insertion order from regex/parser passes.
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.value
	}
	sort.Strings(out)
	return out
}

// stripParens removes everything between balanced parentheses.
func stripParens(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// splitOnAny splits s on any byte from seps, returning trimmed non-empty
// segments. Mirrors strings.FieldsFunc but with an explicit separator set.
func splitOnAny(s, seps string) []string {
	cut := func(r rune) bool { return strings.ContainsRune(seps, r) }
	parts := strings.FieldsFunc(s, cut)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// tokenizeWords lowercases s and returns its alphabetic word tokens.
// Numeric-only tokens are dropped because version numbers and the like
// produce useless keywords. Hyphenated terms (sql-fundamentals,
// columnar-production) are preserved as single tokens.
func tokenizeWords(s string) []string {
	s = strings.ToLower(s)
	cut := func(r rune) bool {
		alpha := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		return !alpha && !digit && r != '_' && r != '-'
	}
	parts := strings.FieldsFunc(s, cut)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if isAllDigits(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hasDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
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
