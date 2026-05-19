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
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teradata-labs/loom/pkg/skills/importer"
)

// skillsImportCmd converts Anthropic-style Agent Skill directories
// (<name>/SKILL.md + references/*.md) into loom/v1 Skill YAML files
// the Loom skill library can load.
//
// All transformation logic lives in pkg/skills/importer; this file is
// the cobra command + per-skill output rendering.
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
	skillsImportTaxonomy string
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
	skillsImportCmd.Flags().StringVar(&skillsImportTaxonomy, "taxonomy", "",
		"path to a custom taxonomy YAML (default: built-in seed shipped "+
			"in embedded/taxonomy.yaml). Only consulted when --classify "+
			"is set. See docs/architecture/skills-import.md#taxonomy for "+
			"the file format.")
}

// runSkillsImport is the cobra RunE for `loom skills import`. It builds an
// importer.Config from the parsed flags + env, hands it to importer.Run,
// and renders progress events to stderr in the format users have come to
// expect:
//
//	==> import: 23 source skills | mode=write | output=~/.loom/skills | classifier=...
//	[ 1/23] teradata-adaptive-optimizer    classify=teradata/performance  ...  [wrote] ...
//	...
//	==> summary: 23 converted, 1 skipped, 0 failed
//	==> classifications: teradata/admin:4, teradata/performance:4, ...
func runSkillsImport(cmd *cobra.Command, args []string) error {
	outDir, err := resolveImportOutDir(skillsImportOutDir)
	if err != nil {
		return err
	}

	classifier, err := buildClassifyLLM()
	if err != nil {
		return fmt.Errorf("classify setup: %w", err)
	}

	// Load taxonomy when the classifier is on. Empty path -> default
	// seed (embedded/taxonomy.yaml). Validation runs at load time so a
	// malformed user file is reported before the first LLM call.
	var taxonomy importer.Taxonomy
	if classifier != nil {
		taxonomy, err = importer.LoadTaxonomy(skillsImportTaxonomy)
		if err != nil {
			return fmt.Errorf("taxonomy: %w", err)
		}
	}

	var ctx context.Context
	if cmd != nil {
		ctx = cmd.Context()
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Bookkeeping for output rendering and the final summary line.
	var (
		prefixWidth   int
		maxNameWidth  int
		classifyTally = map[string]int{}
		total         int
	)

	cfg := importer.DirConfig{
		SourceDir: filepath.Clean(args[0]),
		ProcessOptions: importer.ProcessOptions{
			OutDir:     outDir,
			DryRun:     skillsImportDryRun,
			Overwrite:  skillsImportOverride,
			Classifier: classifier,
			Taxonomy:   taxonomy,

			OnResult: func(r importer.SkillResult) {
				renderSkillResult(r, prefixWidth, maxNameWidth, total, classifyTally)
			},
		},

		OnBanner: func(d importer.DiscoveryReport) {
			total = d.TotalImportable
			prefixWidth = numWidth(total)
			maxNameWidth = d.MaxNameWidth

			mode := "write"
			if d.DryRun {
				mode = "dry-run (no files written)"
			}
			classifierBanner := "off"
			if classifier != nil {
				classifierBanner = classifyProviderInfo()
				if skillsImportTaxonomy != "" {
					classifierBanner += " taxonomy=" + displayPath(skillsImportTaxonomy)
				}
			}
			fmt.Fprintf(os.Stderr, "==> import: %d source skills | mode=%s | output=%s | classifier=%s\n",
				d.TotalImportable, mode, displayPath(d.OutDir), classifierBanner)
			for _, s := range d.Skipped {
				fmt.Fprintf(os.Stderr, "        [skip] %s (%s)\n", s.Name, s.Reason)
			}
			for _, f := range d.Failed {
				fmt.Fprintf(os.Stderr, "        [fail] %s: %s\n", f.Name, f.Err.Error())
			}
		},
	}

	totals, err := importer.RunFromDir(ctx, cfg)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "\n==> summary: %d converted, %d skipped, %d failed\n",
		totals.Converted, totals.Skipped, totals.Failed)
	if len(classifyTally) > 0 {
		fmt.Fprintf(os.Stderr, "==> classifications: %s\n", formatBucketCounts(classifyTally))
	}
	if totals.Failed > 0 {
		return fmt.Errorf("%d skill(s) failed to convert", totals.Failed)
	}
	return nil
}

// renderSkillResult prints one per-skill line in the format the legacy
// in-process importer used. Tag order matches data flow:
// classify -> links -> metadata -> outcome.
func renderSkillResult(r importer.SkillResult, prefixWidth, maxNameWidth, total int, classifyTally map[string]int) {
	linePrefix := fmt.Sprintf("[%*d/%d] %-*s",
		prefixWidth, r.Index, total, maxNameWidth, r.Skill.Name)

	classifyTag := ""
	switch {
	case r.ClassifySkipped:
		classifyTag = "  classify=skip(parent-index)"
	case r.ClassifyEnabled && r.ClassifyPath != "":
		classifyTally[r.ClassifyPath]++
		classifyTag = fmt.Sprintf("  classify=%s", r.ClassifyPath)
	case r.ClassifyEnabled && r.ClassifyPath == "":
		classifyTag = "  classify=fallback(unclassified)"
	}

	linksTag := ""
	if r.LinkedSkillsTotal > 0 {
		emitted := r.LinkedSkillsResolved
		if emitted > importer.SkillRefsCap {
			emitted = importer.SkillRefsCap
		}
		linksTag = fmt.Sprintf("  refs=%d/%d", emitted, r.LinkedSkillsTotal)
	}

	metaTag := fmt.Sprintf("  domain=%s  trigger=%s  keywords=%d  slash-commands=%d  refs-inlined=%d",
		r.Domain, r.TriggerMode, r.KeywordsCount, r.SlashCommandsCount, r.ReferencesInlined)

	tags := classifyTag + linksTag + metaTag

	switch r.Outcome {
	case importer.OutcomeWrote:
		fmt.Fprintf(os.Stderr, "%s%s  [wrote] %s (%s)\n",
			linePrefix, tags, displayPath(r.DestPath), formatSize(r.YAMLBytes))
	case importer.OutcomeWouldWrite:
		fmt.Fprintf(os.Stderr, "%s%s  [would-write] %s (%s)\n",
			linePrefix, tags, displayPath(r.DestPath), formatSize(r.YAMLBytes))
	case importer.OutcomeSkipped:
		fmt.Fprintf(os.Stderr, "%s%s  [skip] exists (use --force to overwrite)\n", linePrefix, tags)
	case importer.OutcomeFailed:
		fmt.Fprintf(os.Stderr, "%s%s  [fail] %v\n", linePrefix, tags, r.Err)
	}

	// When refs were dropped (linked names that don't resolve to
	// importable skills), surface them on a follow-up indented line.
	// Indents to align under the skill-name column.
	if len(r.LinkedSkillsDropped) > 0 {
		indent := prefixWidth*2 + len("[/] ") + maxNameWidth
		fmt.Fprintf(os.Stderr, "%*s  refs-dropped: %s\n",
			indent, "", strings.Join(r.LinkedSkillsDropped, ", "))
	}
}

// numWidth returns the count of digits needed to render n. Used for
// right-padding the [N/M] progress prefix so the names line up.
func numWidth(n int) int {
	if n <= 0 {
		return 1
	}
	w := 0
	for n > 0 {
		w++
		n /= 10
	}
	return w
}

// formatSize renders a byte count in human-friendly units.
func formatSize(bytes int) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%d KB", bytes/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// displayPath collapses $HOME to ~ for cleaner per-line output. Falls
// back to the original path when home isn't resolvable.
func displayPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// formatBucketCounts joins {bucket: count} into a single readable line
// sorted by descending count then alphabetically. Used for the final
// classifier summary.
func formatBucketCounts(counts map[string]int) string {
	type kv struct {
		k string
		v int
	}
	pairs := make([]kv, 0, len(counts))
	for k, v := range counts {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("%s:%d", p.k, p.v))
	}
	return strings.Join(parts, ", ")
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
