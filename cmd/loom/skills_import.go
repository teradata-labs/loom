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
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// skillsImportCmd is the gRPC client for loomv1.SkillsImportService/
// BulkImportSkills. The CLI no longer runs the importer in-process —
// all transformation and writes happen on the server, which holds the
// classifier creds, the persisted index, and the wired router
// subsystems that need to be reloaded after writes.
//
// Source delivery is auto-selected: when the server addr resolves to
// a loopback address (the local-serve case), the CLI sends the source
// path via src_dir. Non-loopback servers default to zipping the
// source tree client-side and sending zip_archive bytes. --zip forces
// zip mode regardless of address.
var skillsImportCmd = &cobra.Command{
	Use:   "import <src-dir>",
	Short: "Import Anthropic-style Agent Skill directories into loom/v1 YAML",
	Long: `Walks <src-dir> for subdirectories containing a SKILL.md file and
converts each one into a loom/v1 Skill YAML on the server.

The server (looms serve) must be running. Set --server (or LOOM_SERVER)
to point at a remote instance.

Source delivery: when the server address resolves to a loopback
address (default: 127.0.0.1:60051), the CLI passes <src-dir> as a
filesystem path the server reads directly. For non-loopback servers,
the CLI zips <src-dir> client-side and uploads it. Use --zip to force
zip mode for a local server too.

Reference markdown files under <skill>/references/ are inlined into
the generated prompt.instructions under labeled "## Reference: <basename>"
sections. Cross-skill markdown links of the form
"[name](../<other>/SKILL.md)" are extracted into skill_refs (capped at 3).
Trigger keywords are synthesized from the skill name and any
"## When to Use" bullets.

Special cases:
  - <name>/SKILL.md ending in "-skill-index" becomes a parent-index
    meta skill (mode: ALWAYS, domain: meta-agent).
  - "agent-skill-builder" is skipped (meta-tooling for authoring skills).

Examples:

  # Default destination (server's $LOOM_SKILLS_DIR or ~/.loom/skills):
  loom skills import ~/Projects/skills

  # Pick a different output dir on the server:
  loom skills import ~/Projects/skills --out /custom/server-side/path

  # Dry run (server reports what would be written; no files written):
  loom skills import ~/Projects/skills --dry-run

  # Force zip upload (e.g., for shared-but-non-loopback dev server):
  loom skills import ~/Projects/skills --zip`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillsImport,
}

var (
	skillsImportOutDir   string
	skillsImportDryRun   bool
	skillsImportOverride bool
	skillsImportClassify bool
	skillsImportTaxonomy string
	skillsImportZip      bool
)

func init() {
	skillsImportCmd.Flags().StringVar(&skillsImportOutDir, "out", "",
		"server-side output directory; empty uses the server's default "+
			"($LOOM_SKILLS_DIR or ~/.loom/skills)")
	skillsImportCmd.Flags().BoolVar(&skillsImportDryRun, "dry-run", false,
		"server reports what would be written; no files written")
	skillsImportCmd.Flags().BoolVar(&skillsImportOverride, "force", false,
		"overwrite existing destination YAML files")
	skillsImportCmd.Flags().BoolVar(&skillsImportClassify, "classify", false,
		"server runs LLM classifier (server must have LOOM_CLASSIFY_PROVIDER "+
			"and the provider's standard creds configured)")
	skillsImportCmd.Flags().StringVar(&skillsImportTaxonomy, "taxonomy", "",
		"path to a custom taxonomy YAML; only consulted with --classify. "+
			"File contents are uploaded; the server validates and uses them. "+
			"See docs/architecture/skills-import.md#taxonomy for the format.")
	skillsImportCmd.Flags().BoolVar(&skillsImportZip, "zip", false,
		"zip <src-dir> and upload to the server even when the server is "+
			"on a loopback address")
}

// runSkillsImport is the cobra RunE for `loom skills import`. It
// opens a gRPC connection to the configured server, builds a
// BulkImportSkillsRequest with src_dir or zip_archive (auto-selected
// per the server address), streams the response, and renders progress
// events to stderr.
func runSkillsImport(cmd *cobra.Command, args []string) error {
	srcDir, err := filepath.Abs(filepath.Clean(args[0]))
	if err != nil {
		return fmt.Errorf("resolve src-dir: %w", err)
	}
	if _, err := os.Stat(srcDir); err != nil {
		return fmt.Errorf("src-dir %s: %w", srcDir, err)
	}

	conn, err := dialSkillsImportServer()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	client := loomv1.NewSkillsImportServiceClient(conn)

	req := &loomv1.BulkImportSkillsRequest{
		OutDir:    skillsImportOutDir,
		DryRun:    skillsImportDryRun,
		Overwrite: skillsImportOverride,
		Classify:  skillsImportClassify,
	}

	// Source delivery: src_dir for local-loopback servers, zip
	// otherwise (or when --zip is explicit).
	useZip := skillsImportZip || !serverIsLoopback(serverAddr)
	if useZip {
		zipBytes, err := zipSourceTree(srcDir)
		if err != nil {
			return fmt.Errorf("zip source: %w", err)
		}
		req.Source = &loomv1.BulkImportSkillsRequest_ZipArchive{ZipArchive: zipBytes}
	} else {
		req.Source = &loomv1.BulkImportSkillsRequest_SrcDir{SrcDir: srcDir}
	}

	if skillsImportTaxonomy != "" {
		taxBytes, err := os.ReadFile(filepath.Clean(skillsImportTaxonomy)) // #nosec G304 -- documented contract for --taxonomy
		if err != nil {
			return fmt.Errorf("read taxonomy file %s: %w", skillsImportTaxonomy, err)
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

	stream, err := client.BulkImportSkills(ctx, req)
	if err != nil {
		return describeRPCError("bulk import", err)
	}

	// Bookkeeping for the per-skill output renderer.
	var (
		prefixWidth   int
		maxNameWidth  int
		total         int
		classifyTally = map[string]int32{}
	)

	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("receive progress: %w", err)
		}

		switch e := ev.Event.(type) {
		case *loomv1.BulkImportProgress_Banner:
			b := e.Banner
			total = int(b.TotalImportable)
			prefixWidth = numWidth(total)
			maxNameWidth = int(b.MaxNameWidth)

			mode := "write"
			if b.DryRun {
				mode = "dry-run (no files written)"
			}
			classifierBanner := "off"
			if b.Classify {
				classifierBanner = "on (server-configured)"
				if skillsImportTaxonomy != "" {
					classifierBanner += " taxonomy=" + displayPath(skillsImportTaxonomy)
				}
			}
			fmt.Fprintf(os.Stderr, "==> import: %d source skills | mode=%s | output=%s | classifier=%s\n",
				b.TotalImportable, mode, displayPath(b.OutDir), classifierBanner)
			for _, s := range b.Skipped {
				fmt.Fprintf(os.Stderr, "        [skip] %s (%s)\n", s.Name, s.Reason)
			}
			for _, f := range b.Failed {
				fmt.Fprintf(os.Stderr, "        [fail] %s: %s\n", f.Name, f.Error)
			}

		case *loomv1.BulkImportProgress_Result:
			r := e.Result
			if r.ClassifyEnabled && r.ClassifyPath != "" {
				classifyTally[r.ClassifyPath]++
			}
			renderResultEvent(r, prefixWidth, maxNameWidth, total)

		case *loomv1.BulkImportProgress_Summary:
			s := e.Summary
			fmt.Fprintf(os.Stderr, "\n==> summary: %d converted, %d skipped, %d failed\n",
				s.Converted, s.Skipped, s.Failed)
			if len(s.ClassifyBuckets) > 0 {
				fmt.Fprintf(os.Stderr, "==> classifications: %s\n", formatBucketCountsProto(s.ClassifyBuckets))
			}
			if s.Failed > 0 {
				return fmt.Errorf("%d skill(s) failed to convert", s.Failed)
			}
			return nil
		}
	}
	return nil
}

// renderResultEvent prints one per-skill line in the format users
// expect from the legacy in-process importer. Tag order matches data
// flow: classify → links → metadata → outcome.
func renderResultEvent(r *loomv1.SkillResult, prefixWidth, maxNameWidth, total int) {
	linePrefix := fmt.Sprintf("[%*d/%d] %-*s",
		prefixWidth, r.Index, total, maxNameWidth, r.SkillName)

	classifyTag := ""
	switch {
	case r.ClassifySkipped:
		classifyTag = "  classify=skip(parent-index)"
	case r.ClassifyEnabled && r.ClassifyPath != "":
		classifyTag = fmt.Sprintf("  classify=%s", r.ClassifyPath)
	case r.ClassifyEnabled && r.ClassifyPath == "":
		classifyTag = "  classify=fallback(unclassified)"
	}

	linksTag := ""
	if r.LinkedSkillsTotal > 0 {
		emitted := r.LinkedSkillsResolved
		if emitted > 3 {
			emitted = 3
		}
		linksTag = fmt.Sprintf("  refs=%d/%d", emitted, r.LinkedSkillsTotal)
	}

	metaTag := fmt.Sprintf("  domain=%s  trigger=%s  keywords=%d  slash-commands=%d  refs-inlined=%d",
		r.Domain, r.TriggerMode, r.KeywordsCount, r.SlashCommandsCount, r.ReferencesInlined)

	tags := classifyTag + linksTag + metaTag

	switch r.Outcome {
	case loomv1.Outcome_OUTCOME_WROTE:
		fmt.Fprintf(os.Stderr, "%s%s  [wrote] %s (%s)\n",
			linePrefix, tags, displayPath(r.DestPath), formatSize(int(r.YamlBytes)))
	case loomv1.Outcome_OUTCOME_WOULD_WRITE:
		fmt.Fprintf(os.Stderr, "%s%s  [would-write] %s (%s)\n",
			linePrefix, tags, displayPath(r.DestPath), formatSize(int(r.YamlBytes)))
	case loomv1.Outcome_OUTCOME_SKIPPED:
		fmt.Fprintf(os.Stderr, "%s%s  [skip] exists (use --force to overwrite)\n", linePrefix, tags)
	case loomv1.Outcome_OUTCOME_FAILED:
		fmt.Fprintf(os.Stderr, "%s%s  [fail] %s\n", linePrefix, tags, r.Err)
	}

	// Dropped refs follow on an indented line so the user can see
	// exactly which cross-references won't appear in skill_refs.
	if len(r.LinkedSkillsDropped) > 0 {
		indent := prefixWidth*2 + len("[/] ") + maxNameWidth
		fmt.Fprintf(os.Stderr, "%*s  refs-dropped: %s\n",
			indent, "", strings.Join(r.LinkedSkillsDropped, ", "))
	}
}

// =============================================================================
// Source-tree zipping
// =============================================================================

// zipSourceTree walks srcDir and writes a zip archive containing
// every regular file under it. Used when the server is on a
// non-loopback address and can't read the path directly.
//
// Layout: entries are stored relative to srcDir's parent, so a
// directory layout of:
//
//	~/Projects/skills/
//	├── teradata-statistics/
//	│   ├── SKILL.md
//	│   └── references/foo.md
//
// produces zip entries like:
//
//	teradata-statistics/SKILL.md
//	teradata-statistics/references/foo.md
//
// which matches what BulkImportSkills expects (one <name>/ subdir
// per skill at the zip root).
func zipSourceTree(srcDir string) ([]byte, error) {
	st, err := os.Stat(srcDir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", srcDir)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	root := filepath.Clean(srcDir)
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		// Skip hidden files / dirs (.git, .DS_Store, etc.).
		if strings.HasPrefix(filepath.Base(path), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			// zip.Writer auto-creates directories implied by file
			// paths, but explicit directory entries help old readers.
			_, err := zw.Create(filepath.ToSlash(rel) + "/")
			return err
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(filepath.Clean(path)) // #nosec G304,G122 -- path is under user-supplied srcDir; symlink TOCTOU during user's own walk is the user's risk to take
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, f)
		_ = f.Close()
		return copyErr
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", srcDir, err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}
	return buf.Bytes(), nil
}

// =============================================================================
// Helpers shared with the CLI's other skills-* subcommands
// =============================================================================

// numWidth returns the count of digits needed to render n, used for
// padding the [N/M] progress prefix.
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

// displayPath collapses $HOME to ~ for cleaner per-line output.
func displayPath(p string) string {
	if p == "" {
		return "(server default)"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// formatBucketCountsProto joins {bucket: count} into a single readable
// line sorted by descending count then alphabetically. Used for the
// final classifications summary.
func formatBucketCountsProto(counts map[string]int32) string {
	type kv struct {
		k string
		v int32
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

// serverIsLoopback reports whether the supplied "host:port" addr
// resolves to a loopback address. Used to pick src_dir vs zip
// delivery without forcing the user to specify a flag.
//
// Falls back to true when parsing fails — better to default to the
// faster src_dir path and let the server return a clear "no such
// directory" error than to needlessly zip-and-upload on every call.
func serverIsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// addr might be just a host without a port; try parsing as
		// IP / hostname directly.
		host = addr
	}
	host = strings.Trim(host, "[]") // strip IPv6 brackets if present
	if host == "" || host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	// Hostname; let the OS resolver decide.
	addrs, err := net.LookupHost(host)
	if err != nil {
		return true // benign fallback (see comment above)
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.IsLoopback() {
			return true
		}
	}
	return false
}
