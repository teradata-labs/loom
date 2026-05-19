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
	"os"
	"path/filepath"

	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/types"
)

// Outcome describes what the pipeline did with a single source skill.
type Outcome int

const (
	// OutcomeWrote means the rendered YAML was written to the destination
	// directory.
	OutcomeWrote Outcome = iota
	// OutcomeWouldWrite means the pipeline ran in dry-run mode; no file
	// was touched.
	OutcomeWouldWrite
	// OutcomeSkipped means the destination YAML already exists and
	// Config.Overwrite was false. The pipeline left the existing file
	// in place.
	OutcomeSkipped
	// OutcomeFailed means the skill failed at one of: render, validate,
	// or write. The rendered bytes (when render succeeded) and the
	// classification info on the result are still populated so the
	// caller can show what was attempted.
	OutcomeFailed
)

// String returns a human-readable form for Outcome, matching the tags
// used by the CLI's per-skill output.
func (o Outcome) String() string {
	switch o {
	case OutcomeWrote:
		return "wrote"
	case OutcomeWouldWrite:
		return "would-write"
	case OutcomeSkipped:
		return "skip"
	case OutcomeFailed:
		return "fail"
	}
	return "unknown"
}

// SkillResult is one record from a Pipeline.Run call. It is rich enough
// for the CLI to render the same per-skill output line that the
// in-process importer produced before this package extraction.
type SkillResult struct {
	Index       int     // 1-based position in this run
	Total       int     // total source skills discovered
	Skill       *Skill  // parsed skill (with ParentIndexPath set when classified)
	Outcome     Outcome // what the pipeline did
	DestPath    string  // absolute path where the YAML was (or would be) written
	YAMLBytes   int     // size of rendered YAML
	Domain      string  // ChooseDomain(skill) — surfaced for output banners
	TriggerMode string  // "AUTO" or "ALWAYS"

	// Classification outcome, when --classify was requested.
	//
	// ClassifyEnabled distinguishes "we tried and got <ClassifyPath>" from
	// "we never asked the classifier" (the most common case, used to
	// skip the classify=... output tag entirely).
	ClassifyEnabled bool
	ClassifyPath    string // e.g. "teradata/performance"; empty on classifier failure
	ClassifyError   error  // non-nil when the LLM call or validator rejected; result is a soft failure (path stays empty)
	ClassifySkipped bool   // true for parent-index meta-skills (deliberately skipped)

	// Reference / link metadata surfaced by the parser.
	LinkedSkillsTotal    int      // raw count from extractLinkedSkillNames
	LinkedSkillsResolved int      // count that resolves to importable skills
	LinkedSkillsDropped  []string // names that did not resolve
	ReferencesInlined    int      // count of references/*.md inlined into prompt
	KeywordsCount        int      // len(BuildKeywords(skill))
	SlashCommandsCount   int      // len(BuildSlashCommands(skill))

	// Err is the failure that produced OutcomeFailed; nil otherwise.
	Err error
}

// SkipNote is one entry from the discovery phase: a source directory
// the pipeline decided not to import (currently only agent-skill-builder).
type SkipNote struct {
	Name   string
	Reason string
}

// FailNote is one entry from the discovery phase: a source directory the
// parser could not consume. These do not become SkillResults; the caller
// should surface them separately.
type FailNote struct {
	Name string
	Err  error
}

// DiscoveryReport summarises the discovery phase before the per-skill
// loop runs. The pipeline emits this once via Config.OnBanner so the
// caller can print a banner with totals.
type DiscoveryReport struct {
	SourceDir string
	OutDir    string
	DryRun    bool
	Overwrite bool
	Classify  bool

	TotalImportable int        // count that will pass through phases 4-5
	Skipped         []SkipNote // discovery-time skips (e.g. agent-skill-builder)
	Failed          []FailNote // discovery-time fails (e.g. malformed frontmatter)
	MaxNameWidth    int        // longest skill name in TotalImportable, for column padding
}

// Config drives a Pipeline run.
type Config struct {
	// SourceDir is the directory containing one subdir per skill, each
	// holding a SKILL.md.
	SourceDir string
	// OutDir is the destination for rendered YAMLs. Created if absent.
	// When DryRun is true, the directory is not created.
	OutDir string
	// DryRun prints what would happen without writing.
	DryRun bool
	// Overwrite controls whether existing destination YAMLs are
	// replaced. When false, the pipeline emits OutcomeSkipped for any
	// pre-existing file.
	Overwrite bool

	// Classifier is the optional LLM provider used to assign
	// parent_index_path. When nil, classification is skipped entirely
	// and the pipeline never makes an LLM call.
	Classifier types.LLMProvider

	// OnBanner fires once after discovery, before the per-skill loop.
	// The DiscoveryReport carries all the totals + skip/fail notes a
	// caller needs to print a banner. May be nil.
	OnBanner func(DiscoveryReport)
	// OnResult fires once per source skill that reached phase 4
	// (rendering). May be nil. Failed-discovery skills do not produce
	// SkillResults; they are reported via DiscoveryReport.Failed.
	OnResult func(SkillResult)
}

// Run executes the import pipeline against cfg.
//
// Pipeline phases:
//  1. Discovery: walk SourceDir, partition into pendingSkills (parsed
//     successfully + safe-name-checked), skipped (intentional), failed
//     (parse/safety errors).
//  2. Banner: invoke cfg.OnBanner with a DiscoveryReport.
//  3. Per-skill loop: for each pending skill, resolve cross-skill
//     references, optionally classify, render YAML, validate by
//     round-tripping through skills.LoadSkill, write or skip per
//     cfg.DryRun + cfg.Overwrite, then invoke cfg.OnResult.
//
// Returns the same totals printed in the legacy summary line.
func Run(ctx context.Context, cfg Config) (converted, skipped, failed int, err error) {
	srcRoot := filepath.Clean(cfg.SourceDir)
	outDir := filepath.Clean(cfg.OutDir)

	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("read source dir %s: %w", srcRoot, err)
	}

	if !cfg.DryRun {
		if err := os.MkdirAll(outDir, 0o750); err != nil {
			return 0, 0, 0, fmt.Errorf("create out dir %s: %w", outDir, err)
		}
	}

	// Phase 1: discovery
	type pending struct {
		dir   string
		skill *Skill
	}
	var (
		pendingSkills = make([]pending, 0, len(entries))
		knownNames    = make(map[string]bool)
		skipNotes     []SkipNote
		failNotes     []FailNote
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillDir := filepath.Join(srcRoot, e.Name())
		skillPath := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			continue
		}
		if IsSkippedSkill(e.Name()) {
			skipNotes = append(skipNotes, SkipNote{e.Name(), "meta-tooling, not convertible"})
			skipped++
			continue
		}
		imp, err := ReadSkill(skillDir)
		if err != nil {
			failNotes = append(failNotes, FailNote{e.Name(), err})
			failed++
			continue
		}
		if !IsSafeSkillName(imp.Name) {
			failNotes = append(failNotes, FailNote{e.Name(), fmt.Errorf("unsafe skill name %q", imp.Name)})
			failed++
			continue
		}
		pendingSkills = append(pendingSkills, pending{dir: skillDir, skill: imp})
		knownNames[imp.Name] = true
	}

	// Phase 2: banner
	maxNameWidth := 0
	for _, p := range pendingSkills {
		if n := len(p.skill.Name); n > maxNameWidth {
			maxNameWidth = n
		}
	}
	if cfg.OnBanner != nil {
		cfg.OnBanner(DiscoveryReport{
			SourceDir:       srcRoot,
			OutDir:          outDir,
			DryRun:          cfg.DryRun,
			Overwrite:       cfg.Overwrite,
			Classify:        cfg.Classifier != nil,
			TotalImportable: len(pendingSkills),
			Skipped:         skipNotes,
			Failed:          failNotes,
			MaxNameWidth:    maxNameWidth,
		})
	}

	// Phase 3-5: per-skill loop
	total := len(pendingSkills)
	for i, p := range pendingSkills {
		imp := p.skill
		// Resolve cross-skill references against the catalog this run
		// will produce. References that don't resolve are dropped from
		// skill_refs (the renderer caps emitted to 3 anyway) but
		// preserved on the result so the caller can show them.
		resolved := make([]string, 0, len(imp.LinkedSkills))
		var dropped []string
		for _, name := range imp.LinkedSkills {
			if knownNames[name] {
				resolved = append(resolved, name)
			} else {
				dropped = append(dropped, name)
			}
		}
		imp.ResolvedRefs = resolved

		// Compute domain + trigger up front so OnResult can show them
		// even when render fails.
		domain := ChooseDomain(imp)
		triggerMode := "AUTO"
		if imp.IsParentIndex {
			triggerMode = "ALWAYS"
		}

		result := SkillResult{
			Index:                i + 1,
			Total:                total,
			Skill:                imp,
			Domain:               domain,
			TriggerMode:          triggerMode,
			LinkedSkillsTotal:    len(imp.LinkedSkills),
			LinkedSkillsResolved: len(resolved),
			LinkedSkillsDropped:  dropped,
			ReferencesInlined:    len(imp.References),
		}

		// Classify if requested. Parent-index meta-skills are
		// deliberately skipped: they live at their own well-known
		// position (unclassified/meta-agent) and surface every turn
		// via mode: ALWAYS.
		if cfg.Classifier != nil {
			result.ClassifyEnabled = true
			if imp.IsParentIndex {
				result.ClassifySkipped = true
			} else {
				path, cerr := Classify(ctx, cfg.Classifier, imp, domain)
				if cerr != nil {
					result.ClassifyError = cerr
				}
				if path != "" {
					imp.ParentIndexPath = path
					result.ClassifyPath = path
				}
			}
		}

		// Phase 4: render. Compute counts after classification so
		// the YAML and the result agree.
		result.KeywordsCount = len(BuildKeywords(imp))
		result.SlashCommandsCount = len(BuildSlashCommands(imp))

		yamlBytes, rerr := RenderYAML(imp)
		if rerr != nil {
			result.Outcome = OutcomeFailed
			result.Err = fmt.Errorf("render: %w", rerr)
			failed++
			if cfg.OnResult != nil {
				cfg.OnResult(result)
			}
			continue
		}
		result.YAMLBytes = len(yamlBytes)

		// Phase 5: validate by round-tripping through the real loader.
		tmpPath := filepath.Join(os.TempDir(), filepath.Base(imp.Name)+".yaml")
		tmpPath = filepath.Clean(tmpPath)
		if werr := os.WriteFile(tmpPath, yamlBytes, 0o600); werr != nil { //nolint:gosec // G306/G703: path is filepath.Clean(temp + safe-name)
			result.Outcome = OutcomeFailed
			result.Err = fmt.Errorf("tmp write: %w", werr)
			failed++
			if cfg.OnResult != nil {
				cfg.OnResult(result)
			}
			continue
		}
		if _, verr := skills.LoadSkill(tmpPath); verr != nil {
			_ = os.Remove(tmpPath) //nolint:gosec // G703: same cleaned tmp path
			result.Outcome = OutcomeFailed
			result.Err = fmt.Errorf("validate: %w", verr)
			failed++
			if cfg.OnResult != nil {
				cfg.OnResult(result)
			}
			continue
		}
		_ = os.Remove(tmpPath) //nolint:gosec // G703: same cleaned tmp path

		dst := filepath.Clean(filepath.Join(outDir, filepath.Base(imp.Name)+".yaml"))
		result.DestPath = dst

		switch {
		case cfg.DryRun:
			result.Outcome = OutcomeWouldWrite
			converted++
		default:
			if _, err := os.Stat(dst); err == nil && !cfg.Overwrite { //nolint:gosec // G304/G703: dst is filepath.Clean(outDir + safe-name)
				result.Outcome = OutcomeSkipped
				result.Err = fmt.Errorf("destination exists (use Overwrite=true)")
				skipped++
			} else {
				if werr := os.WriteFile(dst, yamlBytes, 0o600); werr != nil { //nolint:gosec // G306/G703: dst is filepath.Clean(outDir + safe-name)
					result.Outcome = OutcomeFailed
					result.Err = fmt.Errorf("write: %w", werr)
					failed++
				} else {
					result.Outcome = OutcomeWrote
					converted++
				}
			}
		}

		// Suppress vet hint that the unused i would-be-shadowed; we
		// intentionally use i+1 above for the result index.
		_ = i

		if cfg.OnResult != nil {
			cfg.OnResult(result)
		}
	}

	return converted, skipped, failed, nil
}
