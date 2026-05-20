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

	"github.com/spf13/cobra"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// skillsAddCmd is the gRPC client for AddSkill. Adds a single skill
// to the catalog. Source is a path to a directory containing exactly
// one <name>/SKILL.md (+ optional references/), or a path to a zip
// archive of the same shape.
//
// Use this when adding a single skill outside a bulk import. The
// graph-aware classifier (when --classify is set) sees the existing
// catalog and tends to join the new skill to a populated bucket,
// rather than inventing a parallel sibling.
var skillsAddCmd = &cobra.Command{
	Use:   "add <path>",
	Short: "Add a single skill to the server's catalog",
	Long: `Adds one skill to the server's catalog. <path> is either:

  - a directory containing exactly one <name>/ subdirectory holding
    SKILL.md (+ optional references/*.md), or
  - a .zip file with the same shape.

When --classify is set the server uses ClassifyAgainstGraph: the
classifier sees the live catalog and tends to join existing buckets.
Pair with --taxonomy to override the seed taxonomy server-side.

After a successful add, the server reloads all running agents'
routers so the new skill is routable on the next chat turn.

Examples:

  # Add from a directory:
  loom skills add ~/Projects/skills/teradata-recovery

  # Add from a zip:
  loom skills add ~/Downloads/teradata-recovery.zip

  # Add and classify in one shot:
  loom skills add ~/Projects/skills/teradata-recovery --classify`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillsAdd,
}

var (
	skillsAddOutDir   string
	skillsAddOverride bool
	skillsAddClassify bool
	skillsAddTaxonomy string
	skillsAddZip      bool
)

func init() {
	skillsAddCmd.Flags().StringVar(&skillsAddOutDir, "out", "",
		"server-side output directory; empty uses the server's default")
	skillsAddCmd.Flags().BoolVar(&skillsAddOverride, "force", false,
		"overwrite existing destination YAML if a skill with this name "+
			"already exists in the catalog")
	skillsAddCmd.Flags().BoolVar(&skillsAddClassify, "classify", false,
		"server runs the graph-aware classifier to assign parent_index_path")
	skillsAddCmd.Flags().StringVar(&skillsAddTaxonomy, "taxonomy", "",
		"path to a custom taxonomy YAML; only consulted with --classify")
	skillsAddCmd.Flags().BoolVar(&skillsAddZip, "zip", false,
		"zip the source and upload (forced for non-loopback servers)")
}

func runSkillsAdd(cmd *cobra.Command, args []string) error {
	src, err := filepath.Abs(filepath.Clean(args[0]))
	if err != nil {
		return fmt.Errorf("resolve source path: %w", err)
	}
	st, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("source %s: %w", src, err)
	}

	conn, err := dialSkillsImportServer()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	client := loomv1.NewSkillsImportServiceClient(conn)

	req := &loomv1.AddSkillRequest{
		OutDir:    skillsAddOutDir,
		Overwrite: skillsAddOverride,
		Classify:  skillsAddClassify,
	}

	useZip := skillsAddZip || !serverIsLoopback(serverAddr) || !st.IsDir()
	switch {
	case useZip && st.IsDir():
		zipBytes, err := zipSourceTree(src)
		if err != nil {
			return fmt.Errorf("zip source: %w", err)
		}
		req.Source = &loomv1.AddSkillRequest_ZipArchive{ZipArchive: zipBytes}
	case useZip && !st.IsDir():
		// Path is already a zip file; load and forward.
		zipBytes, err := os.ReadFile(filepath.Clean(src)) // #nosec G304 -- user-supplied path is the documented contract
		if err != nil {
			return fmt.Errorf("read zip file: %w", err)
		}
		req.Source = &loomv1.AddSkillRequest_ZipArchive{ZipArchive: zipBytes}
	default:
		req.Source = &loomv1.AddSkillRequest_SkillDir{SkillDir: src}
	}

	if skillsAddTaxonomy != "" {
		taxBytes, err := os.ReadFile(filepath.Clean(skillsAddTaxonomy)) // #nosec G304 -- documented contract for --taxonomy
		if err != nil {
			return fmt.Errorf("read taxonomy file %s: %w", skillsAddTaxonomy, err)
		}
		req.TaxonomyOverride = taxBytes
	}

	var ctx context.Context
	if cmd != nil {
		ctx = cmd.Context()
	}
	if ctx == nil {
		ctx = context.Background()
	}

	resp, err := client.AddSkill(ctx, req)
	if err != nil {
		return describeRPCError("add", err)
	}
	if resp.Result == nil {
		return fmt.Errorf("add skill: server returned nil result")
	}

	// Render outcome line in the same format the bulk import uses.
	r := resp.Result
	classifyTag := ""
	switch {
	case r.ClassifySkipped:
		classifyTag = "  classify=skip(parent-index)"
	case r.ClassifyEnabled && r.ClassifyPath != "":
		classifyTag = fmt.Sprintf("  classify=%s", r.ClassifyPath)
	case r.ClassifyEnabled && r.ClassifyPath == "":
		classifyTag = "  classify=fallback(unclassified)"
	}
	metaTag := fmt.Sprintf("  domain=%s  trigger=%s  keywords=%d  slash-commands=%d  refs-inlined=%d",
		r.Domain, r.TriggerMode, r.KeywordsCount, r.SlashCommandsCount, r.ReferencesInlined)

	switch r.Outcome {
	case loomv1.Outcome_OUTCOME_WROTE:
		fmt.Fprintf(os.Stderr, "%s%s%s  [wrote] %s (%s)\n",
			r.SkillName, classifyTag, metaTag, displayPath(r.DestPath), formatSize(int(r.YamlBytes)))
	case loomv1.Outcome_OUTCOME_SKIPPED:
		fmt.Fprintf(os.Stderr, "%s%s%s  [skip] exists (use --force to overwrite)\n",
			r.SkillName, classifyTag, metaTag)
		return fmt.Errorf("skill %s already exists; pass --force to overwrite", r.SkillName)
	case loomv1.Outcome_OUTCOME_FAILED:
		return fmt.Errorf("add %s: %s", r.SkillName, r.Err)
	default:
		return fmt.Errorf("add %s: unexpected outcome %s", r.SkillName, r.Outcome)
	}

	if resp.RouterReloaded {
		fmt.Fprintf(os.Stderr, "==> router reloaded (running agents see the new skill immediately)\n")
	} else {
		fmt.Fprintf(os.Stderr, "==> router NOT reloaded (restart looms serve to surface the new skill)\n")
	}
	return nil
}
