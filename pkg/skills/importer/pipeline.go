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
	// ProcessOptions.Overwrite was false. The pipeline left the existing
	// file in place.
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

// SkillResult is one record from a pipeline call. Rich enough for a CLI
// or a streaming gRPC server to render the same per-skill output line
// the in-process importer produced before the pipeline split.
type SkillResult struct {
	Index       int     // 1-based position in this run
	Total       int     // total source skills in this run
	Skill       *Skill  // parsed skill (with ParentIndexPath set when classified)
	Outcome     Outcome // what the pipeline did
	DestPath    string  // absolute path where the YAML was (or would be) written
	YAMLBytes   int     // size of rendered YAML
	Domain      string  // ChooseDomain(skill) — surfaced for output banners
	TriggerMode string  // "AUTO" or "ALWAYS"

	// Classification outcome, when ProcessOptions.Classifier was set.
	//
	// ClassifyEnabled distinguishes "we tried and got <ClassifyPath>" from
	// "we never asked the classifier" (used by callers to skip the
	// classify=... output tag entirely).
	ClassifyEnabled bool
	ClassifyPath    string // e.g. "teradata/performance"; empty on classifier failure
	ClassifyError   error  // non-nil when LLM call or validator rejected
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
// surfaces them separately.
type FailNote struct {
	Name string
	Err  error
}

// DiscoveryReport summarises the discovery phase before the per-skill
// loop runs. The pipeline emits this once via ProcessOptions.OnBanner so
// the caller can print a banner with totals.
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

// Totals captures the summary counts returned by the run entry points.
// Returned as a struct rather than three named returns so callers can
// add fields (e.g. classifications-per-bucket) without breaking the
// signature.
type Totals struct {
	Converted int
	Skipped   int
	Failed    int
}

// ProcessOptions are the per-call settings that don't depend on the
// source-discovery shape (dir vs zip vs inline). Used by ProcessSkill
// directly, and embedded in DirConfig / SkillsConfig for the run
// entry points.
type ProcessOptions struct {
	// OutDir is the destination for rendered YAMLs. Required.
	// Created by RunFromDir / RunFromSkills if absent (when DryRun is
	// false). ProcessSkill assumes the directory already exists.
	OutDir string
	// DryRun reports what would happen without writing.
	DryRun bool
	// Overwrite controls whether existing destination YAMLs are
	// replaced. When false, ProcessSkill emits OutcomeSkipped for any
	// pre-existing file.
	Overwrite bool

	// Classifier is the optional LLM provider used to assign
	// parent_index_path. When nil, classification is skipped entirely
	// and the pipeline never makes an LLM call.
	Classifier types.LLMProvider
	// Taxonomy is the seed bucket map handed to the classifier prompt.
	// When zero (Taxonomy{}), the classifier uses DefaultTaxonomy().
	// Ignored when Classifier is nil.
	Taxonomy Taxonomy
	// Graph is optional live-router-tree context. When non-nil, the
	// pipeline calls ClassifyAgainstGraph instead of Classify so the
	// LLM can prefer joining existing buckets over inventing new ones.
	// Used by the gRPC server-side path; CLI's offline --classify
	// leaves this nil.
	Graph *GraphContext

	// OnResult fires once per source skill that reached phase 4
	// (rendering). May be nil.
	OnResult func(SkillResult)
}

// DirConfig drives RunFromDir.
type DirConfig struct {
	// SourceDir is the directory containing one subdir per skill, each
	// holding a SKILL.md.
	SourceDir string

	// ProcessOptions controls the per-skill phase. SourceDir-driven
	// callers set OutDir there.
	ProcessOptions

	// OnBanner fires once after discovery, before the per-skill loop.
	// May be nil.
	OnBanner func(DiscoveryReport)
}

// SkillsConfig drives RunFromSkills (the pre-parsed-slice entry point
// used by the gRPC server when source arrives as a zip archive or
// InlineSkill records).
type SkillsConfig struct {
	// Skills are the pre-parsed source skills to render. Caller is
	// responsible for ensuring each has a non-empty Name and that names
	// are unique across the slice (cross-skill ref resolution uses the
	// names verbatim).
	Skills []*Skill

	// ProcessOptions controls the per-skill phase.
	ProcessOptions

	// OnBanner fires once before the per-skill loop. The DiscoveryReport
	// reports TotalImportable=len(Skills) with empty skip/fail notes
	// (the caller has already done discovery). May be nil.
	OnBanner func(DiscoveryReport)
}

// RunFromDir is the directory-walk entry point. Discovers source skills
// under cfg.SourceDir, invokes OnBanner, then loops through each skill
// via ProcessSkill.
//
// Pipeline phases:
//  1. Discovery: walk SourceDir, partition into pendingSkills (parsed
//     successfully + safe-name-checked), skipped (intentional), failed
//     (parse/safety errors).
//  2. Banner: invoke cfg.OnBanner with a DiscoveryReport.
//  3. Per-skill loop: ProcessSkill for each pending skill.
func RunFromDir(ctx context.Context, cfg DirConfig) (Totals, error) {
	srcRoot := filepath.Clean(cfg.SourceDir)
	outDir := filepath.Clean(cfg.OutDir)

	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		return Totals{}, fmt.Errorf("read source dir %s: %w", srcRoot, err)
	}

	if !cfg.DryRun {
		if err := os.MkdirAll(outDir, 0o750); err != nil {
			return Totals{}, fmt.Errorf("create out dir %s: %w", outDir, err)
		}
	}

	// Phase 1: discovery
	var (
		pendingSkills []*Skill
		skipNotes     []SkipNote
		failNotes     []FailNote
	)
	totals := Totals{}
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
			totals.Skipped++
			continue
		}
		imp, err := ReadSkill(skillDir)
		if err != nil {
			failNotes = append(failNotes, FailNote{e.Name(), err})
			totals.Failed++
			continue
		}
		if !IsSafeSkillName(imp.Name) {
			failNotes = append(failNotes, FailNote{e.Name(), fmt.Errorf("unsafe skill name %q", imp.Name)})
			totals.Failed++
			continue
		}
		pendingSkills = append(pendingSkills, imp)
	}

	// Phase 2: banner
	maxNameWidth := 0
	for _, s := range pendingSkills {
		if n := len(s.Name); n > maxNameWidth {
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

	// Phase 3-5: per-skill loop. Build the knownNames set from all
	// pending skills so cross-skill refs resolve against this run's
	// catalog.
	knownNames := make(map[string]bool, len(pendingSkills))
	for _, s := range pendingSkills {
		knownNames[s.Name] = true
	}
	loopTotals := processLoop(ctx, pendingSkills, knownNames, cfg.ProcessOptions)
	totals.Converted += loopTotals.Converted
	totals.Skipped += loopTotals.Skipped
	totals.Failed += loopTotals.Failed
	return totals, nil
}

// RunFromSkills is the pre-parsed-slice entry point. Skips discovery
// (the caller has already parsed the source) and goes straight to
// banner + per-skill loop. Used by the gRPC server when source arrived
// as a zip archive or as InlineSkill records.
func RunFromSkills(ctx context.Context, cfg SkillsConfig) (Totals, error) {
	outDir := filepath.Clean(cfg.OutDir)
	if !cfg.DryRun {
		if err := os.MkdirAll(outDir, 0o750); err != nil {
			return Totals{}, fmt.Errorf("create out dir %s: %w", outDir, err)
		}
	}
	maxNameWidth := 0
	for _, s := range cfg.Skills {
		if n := len(s.Name); n > maxNameWidth {
			maxNameWidth = n
		}
	}
	if cfg.OnBanner != nil {
		cfg.OnBanner(DiscoveryReport{
			SourceDir:       "",
			OutDir:          outDir,
			DryRun:          cfg.DryRun,
			Overwrite:       cfg.Overwrite,
			Classify:        cfg.Classifier != nil,
			TotalImportable: len(cfg.Skills),
			MaxNameWidth:    maxNameWidth,
		})
	}
	knownNames := make(map[string]bool, len(cfg.Skills))
	for _, s := range cfg.Skills {
		knownNames[s.Name] = true
	}
	return processLoop(ctx, cfg.Skills, knownNames, cfg.ProcessOptions), nil
}

// ProcessSkill renders, validates, optionally classifies, and writes one
// already-parsed skill. Used by the gRPC server's AddSkill RPC and by
// the directory / slice run entry points internally.
//
// knownNames is the set of skill names the caller considers
// "importable" (used to filter cross-skill refs); pass nil to drop all
// LinkedSkills from skill_refs and treat them all as dropped.
//
// opts.OutDir must already exist. Use RunFromDir / RunFromSkills when
// you need directory creation handled.
func ProcessSkill(ctx context.Context, idx, total int, skill *Skill, knownNames map[string]bool, opts ProcessOptions) SkillResult {
	// Resolve cross-skill references against the supplied catalog.
	resolved := make([]string, 0, len(skill.LinkedSkills))
	var dropped []string
	for _, name := range skill.LinkedSkills {
		if knownNames[name] {
			resolved = append(resolved, name)
		} else {
			dropped = append(dropped, name)
		}
	}
	skill.ResolvedRefs = resolved

	// Compute domain + trigger up front so the result carries them
	// even when render fails.
	domain := ChooseDomain(skill)
	triggerMode := "AUTO"
	if skill.IsParentIndex {
		triggerMode = "ALWAYS"
	}

	result := SkillResult{
		Index:                idx,
		Total:                total,
		Skill:                skill,
		Domain:               domain,
		TriggerMode:          triggerMode,
		LinkedSkillsTotal:    len(skill.LinkedSkills),
		LinkedSkillsResolved: len(resolved),
		LinkedSkillsDropped:  dropped,
		ReferencesInlined:    len(skill.References),
	}

	// Classify if requested. Parent-index meta-skills are deliberately
	// skipped: they live at their own well-known position
	// (unclassified/meta-agent) and surface every turn via mode: ALWAYS.
	if opts.Classifier != nil {
		result.ClassifyEnabled = true
		if skill.IsParentIndex {
			result.ClassifySkipped = true
		} else {
			taxonomy := opts.Taxonomy
			if len(taxonomy.Domains) == 0 {
				taxonomy = DefaultTaxonomy()
			}
			var (
				path string
				cerr error
			)
			if opts.Graph != nil {
				path, cerr = ClassifyAgainstGraph(ctx, opts.Classifier, skill, domain, taxonomy, *opts.Graph)
			} else {
				path, cerr = Classify(ctx, opts.Classifier, skill, domain, taxonomy)
			}
			if cerr != nil {
				result.ClassifyError = cerr
			}
			if path != "" {
				skill.ParentIndexPath = path
				result.ClassifyPath = path
			}
		}
	}

	// Phase 4: render. Compute counts after classification so the YAML
	// and the result agree.
	result.KeywordsCount = len(BuildKeywords(skill))
	result.SlashCommandsCount = len(BuildSlashCommands(skill))

	yamlBytes, rerr := RenderYAML(skill)
	if rerr != nil {
		result.Outcome = OutcomeFailed
		result.Err = fmt.Errorf("render: %w", rerr)
		return result
	}
	result.YAMLBytes = len(yamlBytes)

	// Phase 5: validate by round-tripping through the real loader.
	tmpPath := filepath.Join(os.TempDir(), filepath.Base(skill.Name)+".yaml")
	tmpPath = filepath.Clean(tmpPath)
	if werr := os.WriteFile(tmpPath, yamlBytes, 0o600); werr != nil { //nolint:gosec // G306/G703: path is filepath.Clean(temp + safe-name)
		result.Outcome = OutcomeFailed
		result.Err = fmt.Errorf("tmp write: %w", werr)
		return result
	}
	if _, verr := skills.LoadSkill(tmpPath); verr != nil {
		_ = os.Remove(tmpPath) //nolint:gosec // G703: same cleaned tmp path
		result.Outcome = OutcomeFailed
		result.Err = fmt.Errorf("validate: %w", verr)
		return result
	}
	_ = os.Remove(tmpPath) //nolint:gosec // G703: same cleaned tmp path

	dst := filepath.Clean(filepath.Join(filepath.Clean(opts.OutDir), filepath.Base(skill.Name)+".yaml"))
	result.DestPath = dst

	switch {
	case opts.DryRun:
		result.Outcome = OutcomeWouldWrite
	default:
		if _, err := os.Stat(dst); err == nil && !opts.Overwrite { //nolint:gosec // G304/G703: dst is filepath.Clean(outDir + safe-name)
			result.Outcome = OutcomeSkipped
			result.Err = fmt.Errorf("destination exists (use Overwrite=true)")
		} else {
			if werr := os.WriteFile(dst, yamlBytes, 0o600); werr != nil { //nolint:gosec // G306/G703: dst is filepath.Clean(outDir + safe-name)
				result.Outcome = OutcomeFailed
				result.Err = fmt.Errorf("write: %w", werr)
			} else {
				result.Outcome = OutcomeWrote
			}
		}
	}
	return result
}

// processLoop is the shared per-skill iteration used by RunFromDir and
// RunFromSkills. Tallies outcomes into a Totals and fires opts.OnResult
// for each result.
func processLoop(ctx context.Context, src []*Skill, knownNames map[string]bool, opts ProcessOptions) Totals {
	totals := Totals{}
	total := len(src)
	for i, skill := range src {
		result := ProcessSkill(ctx, i+1, total, skill, knownNames, opts)
		switch result.Outcome {
		case OutcomeWrote, OutcomeWouldWrite:
			totals.Converted++
		case OutcomeSkipped:
			totals.Skipped++
		case OutcomeFailed:
			totals.Failed++
		}
		if opts.OnResult != nil {
			opts.OnResult(result)
		}
	}
	return totals
}
