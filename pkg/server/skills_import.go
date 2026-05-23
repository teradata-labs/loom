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

package server

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/skills/importer"
	"github.com/teradata-labs/loom/pkg/skills/index"
	"github.com/teradata-labs/loom/pkg/types"
)

// SkillsImportServer implements loomv1.SkillsImportServiceServer.
//
// The server is a peer of MultiAgentServer rather than a method on it
// because skills import is a bounded subsystem with its own lifecycle:
// it owns the classifier provider, the taxonomy seed, the zip
// extraction temp directory, and the post-write router-reload trigger.
// Embedding it on MultiAgentServer would conflate skills-management
// with multi-agent coordination state.
//
// Design properties:
//
//   - Source delivery is a oneof on every request: src_dir, zip_archive,
//     or (for AddSkill) inline. Zip archives are unpacked to a per-call
//     temp directory; the temp directory is deleted before the RPC
//     returns regardless of outcome. Generated YAMLs in skills_dir are
//     the canonical artifact.
//   - Classification is opt-in via request flag. The server resolves
//     its classifier LLM provider once at construction time
//     (SetClassifyProvider). When unset, classify=true requests fail
//     fast with FAILED_PRECONDITION rather than silently degrading to
//     stateless or no-classify behavior.
//   - GraphContext is built per-call from the persisted index in
//     pkg/skills/index.Store (NOT from the in-memory library — see
//     pkg/skills/importer/graph.go for the rationale). The first call
//     after server boot may pay an inline build if the warmer hasn't
//     run yet.
//   - After successful writes, the server calls
//     Registry.ReloadAllSkillRouters so every running agent picks up
//     the new tree without a restart. router_reloaded is reported
//     truthfully on each response.
type SkillsImportServer struct {
	loomv1.UnimplementedSkillsImportServiceServer

	registry *agent.Registry
	store    index.Store
	skillLib *skills.Library // shared catalog for ClassifySkill reads + binding-resolver
	classify types.LLMProvider
	tracer   observability.Tracer
	logger   *zap.Logger

	// defaultOutDir is the destination used when a request leaves
	// out_dir empty. Resolves the same way the CLI does:
	// $LOOM_SKILLS_DIR > $HOME/.loom/skills.
	defaultOutDir string
}

// SkillsImportServerConfig bundles the dependencies required to
// construct a SkillsImportServer. Required fields are non-nil checked
// in NewSkillsImportServer.
type SkillsImportServerConfig struct {
	// Registry holds the wired router subsystems for post-write
	// reloads. Required.
	Registry *agent.Registry
	// Store is the persisted index source for GraphContext building.
	// Required. Same SQLStore the agents use; pass the registry's DB.
	Store index.Store
	// SkillLib is the read-only library used by ClassifySkill to load
	// an existing skill before re-classification. Required.
	SkillLib *skills.Library
	// DefaultOutDir is where YAMLs land when out_dir is empty in the
	// request. Required.
	DefaultOutDir string

	// Classify is the LLM provider used when a request sets
	// classify=true. Optional at construction; calls with
	// classify=true return FAILED_PRECONDITION when this is nil.
	Classify types.LLMProvider
	// Tracer is optional; defaults to no-op when nil.
	Tracer observability.Tracer
	// Logger is optional; defaults to a no-op zap logger when nil.
	Logger *zap.Logger
}

// NewSkillsImportServer constructs a SkillsImportServer. Returns an
// error when any required dependency is nil so registration failures
// surface at server boot rather than at first RPC.
func NewSkillsImportServer(cfg SkillsImportServerConfig) (*SkillsImportServer, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("skills import server: Registry is required")
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("skills import server: Store is required")
	}
	if cfg.SkillLib == nil {
		return nil, fmt.Errorf("skills import server: SkillLib is required")
	}
	if cfg.DefaultOutDir == "" {
		return nil, fmt.Errorf("skills import server: DefaultOutDir is required")
	}
	tracer := cfg.Tracer
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SkillsImportServer{
		registry:      cfg.Registry,
		store:         cfg.Store,
		skillLib:      cfg.SkillLib,
		classify:      cfg.Classify,
		tracer:        tracer,
		logger:        logger,
		defaultOutDir: cfg.DefaultOutDir,
	}, nil
}

// =============================================================================
// BulkImportSkills
// =============================================================================

// BulkImportSkills implements loomv1.SkillsImportService/BulkImportSkills.
func (s *SkillsImportServer) BulkImportSkills(
	req *loomv1.BulkImportSkillsRequest,
	stream loomv1.SkillsImportService_BulkImportSkillsServer,
) error {
	ctx := stream.Context()

	srcDir, cleanup, err := s.materializeBulkSource(req)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "bulk source: %v", err)
	}
	defer cleanup()

	if req.Classify && s.classify == nil {
		return status.Error(codes.FailedPrecondition,
			"request set classify=true but server has no classify provider configured")
	}

	taxonomy, err := s.taxonomyForRequest(req.TaxonomyOverride)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "taxonomy: %v", err)
	}

	outDir := req.OutDir
	if outDir == "" {
		outDir = s.defaultOutDir
	}

	graph, err := s.graphContextForClassify(ctx, req.Classify)
	if err != nil {
		return status.Errorf(codes.Internal, "graph context: %v", err)
	}

	cfg := importer.DirConfig{
		SourceDir: srcDir,
		ProcessOptions: importer.ProcessOptions{
			OutDir:     outDir,
			DryRun:     req.DryRun,
			Overwrite:  req.Overwrite,
			Classifier: s.classifierIfRequested(req.Classify),
			Taxonomy:   taxonomy,
			Graph:      graph,
			OnResult: func(r importer.SkillResult) {
				_ = stream.Send(&loomv1.BulkImportProgress{
					Event: &loomv1.BulkImportProgress_Result{
						Result: skillResultToProto(r),
					},
				})
			},
		},
		OnBanner: func(d importer.DiscoveryReport) {
			_ = stream.Send(&loomv1.BulkImportProgress{
				Event: &loomv1.BulkImportProgress_Banner{
					Banner: discoveryReportToProto(d),
				},
			})
		},
	}

	classifyTally := map[string]int32{}
	cfg.OnResult = func(r importer.SkillResult) {
		if r.ClassifyEnabled && r.ClassifyPath != "" {
			classifyTally[r.ClassifyPath]++
		}
		_ = stream.Send(&loomv1.BulkImportProgress{
			Event: &loomv1.BulkImportProgress_Result{
				Result: skillResultToProto(r),
			},
		})
	}

	totals, err := importer.RunFromDir(ctx, cfg)
	if err != nil {
		return status.Errorf(codes.Internal, "run import: %v", err)
	}

	// Trigger a router reload so running agents see the new tree.
	// Only meaningful when at least one skill was actually written
	// (DryRun + zero-Converted produces nothing to reload).
	if !req.DryRun && totals.Converted > 0 {
		reloaded, failed := s.registry.ReloadAllSkillRouters(ctx)
		s.logger.Info("Bulk import: router reload",
			zap.Int("reloaded", reloaded),
			zap.Int("failed", failed))
	}

	return stream.Send(&loomv1.BulkImportProgress{
		Event: &loomv1.BulkImportProgress_Summary{
			Summary: &loomv1.SummaryInfo{
				Converted:       int32(totals.Converted), // #nosec G115 -- counts are bounded by file count
				Skipped:         int32(totals.Skipped),   // #nosec G115 -- bounded by file count
				Failed:          int32(totals.Failed),    // #nosec G115 -- bounded by file count
				ClassifyBuckets: classifyTally,
			},
		},
	})
}

// =============================================================================
// AddSkill
// =============================================================================

// AddSkill implements loomv1.SkillsImportService/AddSkill.
func (s *SkillsImportServer) AddSkill(
	ctx context.Context,
	req *loomv1.AddSkillRequest,
) (*loomv1.AddSkillResponse, error) {
	if req.Classify && s.classify == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"request set classify=true but server has no classify provider configured")
	}

	skill, knownNames, cleanup, err := s.materializeSingleSource(req)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "single source: %v", err)
	}
	defer cleanup()

	taxonomy, err := s.taxonomyForRequest(req.TaxonomyOverride)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "taxonomy: %v", err)
	}

	outDir := req.OutDir
	if outDir == "" {
		outDir = s.defaultOutDir
	}
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "create out dir: %v", err)
	}

	graph, err := s.graphContextForClassify(ctx, req.Classify)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "graph context: %v", err)
	}

	opts := importer.ProcessOptions{
		OutDir:     outDir,
		Overwrite:  req.Overwrite,
		Classifier: s.classifierIfRequested(req.Classify),
		Taxonomy:   taxonomy,
		Graph:      graph,
	}
	result := importer.ProcessSkill(ctx, 1, 1, skill, knownNames, opts)

	routerReloaded := false
	if result.Outcome == importer.OutcomeWrote {
		reloaded, failed := s.registry.ReloadAllSkillRouters(ctx)
		s.logger.Info("Add skill: router reload",
			zap.String("skill", skill.Name),
			zap.Int("reloaded", reloaded),
			zap.Int("failed", failed))
		routerReloaded = reloaded > 0 && failed == 0
	}

	return &loomv1.AddSkillResponse{
		Result:         skillResultToProto(result),
		RouterReloaded: routerReloaded,
	}, nil
}

// =============================================================================
// ClassifySkill
// =============================================================================

// ClassifySkill implements loomv1.SkillsImportService/ClassifySkill.
func (s *SkillsImportServer) ClassifySkill(
	ctx context.Context,
	req *loomv1.ClassifySkillRequest,
) (*loomv1.ClassifySkillResponse, error) {
	if req.SkillName == "" {
		return nil, status.Error(codes.InvalidArgument, "skill_name is required")
	}
	if !importer.IsSafeSkillName(req.SkillName) {
		return nil, status.Errorf(codes.InvalidArgument,
			"unsafe skill name %q", req.SkillName)
	}
	if s.classify == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"server has no classify provider configured")
	}

	existing, err := s.skillLib.Load(req.SkillName)
	if err != nil {
		return nil, status.Errorf(codes.NotFound,
			"skill %q not found in catalog: %v", req.SkillName, err)
	}
	previousPath := existing.ParentIndexPath

	taxonomy, err := s.taxonomyForRequest(req.TaxonomyOverride)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "taxonomy: %v", err)
	}

	graph, err := s.graphContextForClassify(ctx, true)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "graph context: %v", err)
	}

	domain := chooseDomainFromExistingSkill(existing)

	// Convert the existing loaded skill to importer.Skill shape so the
	// classifier sees the same description + when-to-use the importer
	// would.
	impSkill := skillToImporterSkill(existing)

	chosen, cerr := importer.ClassifyAgainstGraph(
		ctx,
		s.classify,
		impSkill,
		domain,
		taxonomy,
		*graph,
	)
	if cerr != nil {
		return nil, status.Errorf(codes.Unavailable,
			"classifier failed: %v", cerr)
	}
	if chosen == "" {
		return nil, status.Error(codes.Unavailable,
			"classifier returned empty path (LLM error or validator reject)")
	}

	// Write the updated YAML back.
	existing.ParentIndexPath = chosen
	if err := s.skillLib.WriteSkill(existing); err != nil {
		return nil, status.Errorf(codes.Internal, "write skill yaml: %v", err)
	}

	reloaded, failed := s.registry.ReloadAllSkillRouters(ctx)
	s.logger.Info("Classify skill: router reload",
		zap.String("skill", req.SkillName),
		zap.String("from", previousPath),
		zap.String("to", chosen),
		zap.Int("reloaded", reloaded),
		zap.Int("failed", failed))

	return &loomv1.ClassifySkillResponse{
		ParentIndexPath: chosen,
		PreviousPath:    previousPath,
		RouterReloaded:  reloaded > 0 && failed == 0,
	}, nil
}

// =============================================================================
// Source materialization
// =============================================================================

// materializeBulkSource resolves the request's source oneof to a
// directory path importer.RunFromDir can read from. Returns the path,
// a cleanup function (which must be called), and any error.
//
// For src_dir: returns the path verbatim and a no-op cleanup.
// For zip_archive: extracts to a temp dir under os.TempDir; cleanup
// removes the temp dir.
func (s *SkillsImportServer) materializeBulkSource(req *loomv1.BulkImportSkillsRequest) (string, func(), error) {
	switch src := req.Source.(type) {
	case *loomv1.BulkImportSkillsRequest_SrcDir:
		if src.SrcDir == "" {
			return "", noopCleanup, fmt.Errorf("src_dir is empty")
		}
		return filepath.Clean(src.SrcDir), noopCleanup, nil
	case *loomv1.BulkImportSkillsRequest_ZipArchive:
		return s.extractZipToTempDir(src.ZipArchive, "loom-bulk-import-")
	case nil:
		return "", noopCleanup, fmt.Errorf("source is required (src_dir or zip_archive)")
	default:
		return "", noopCleanup, fmt.Errorf("unknown source variant %T", src)
	}
}

// materializeSingleSource resolves the AddSkill request source to a
// parsed importer.Skill plus a known-names set for cross-skill ref
// resolution. Returns the skill, known-names, a cleanup function, and
// any error.
//
// For skill_dir: ReadSkill the directory directly.
// For zip_archive: extract to temp, then ReadSkill the unique
// subdirectory inside.
// For inline: reconstruct the skill from frontmatter + body +
// references map directly.
//
// knownNames is populated from the catalog so cross-skill refs in the
// new skill's body can resolve. The existing skills in the catalog
// are the authoritative known set.
func (s *SkillsImportServer) materializeSingleSource(
	req *loomv1.AddSkillRequest,
) (*importer.Skill, map[string]bool, func(), error) {
	known := s.knownNamesFromCatalog()

	switch src := req.Source.(type) {
	case *loomv1.AddSkillRequest_SkillDir:
		if src.SkillDir == "" {
			return nil, nil, noopCleanup, fmt.Errorf("skill_dir is empty")
		}
		skill, err := importer.ReadSkill(filepath.Clean(src.SkillDir))
		if err != nil {
			return nil, nil, noopCleanup, fmt.Errorf("read skill: %w", err)
		}
		if !importer.IsSafeSkillName(skill.Name) {
			return nil, nil, noopCleanup, fmt.Errorf("unsafe skill name %q", skill.Name)
		}
		known[skill.Name] = true
		return skill, known, noopCleanup, nil

	case *loomv1.AddSkillRequest_ZipArchive:
		dir, cleanup, err := s.extractZipToTempDir(src.ZipArchive, "loom-add-skill-")
		if err != nil {
			return nil, nil, noopCleanup, err
		}
		// Expect exactly one <name>/ inside.
		entries, err := os.ReadDir(dir)
		if err != nil {
			cleanup()
			return nil, nil, noopCleanup, fmt.Errorf("read extracted zip: %w", err)
		}
		var skillDir string
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if skillDir != "" {
				cleanup()
				return nil, nil, noopCleanup, fmt.Errorf("zip contains more than one top-level directory")
			}
			skillDir = filepath.Join(dir, e.Name())
		}
		if skillDir == "" {
			cleanup()
			return nil, nil, noopCleanup, fmt.Errorf("zip contains no top-level directory with a SKILL.md")
		}
		skill, err := importer.ReadSkill(skillDir)
		if err != nil {
			cleanup()
			return nil, nil, noopCleanup, fmt.Errorf("read skill from zip: %w", err)
		}
		if !importer.IsSafeSkillName(skill.Name) {
			cleanup()
			return nil, nil, noopCleanup, fmt.Errorf("unsafe skill name %q", skill.Name)
		}
		known[skill.Name] = true
		return skill, known, cleanup, nil

	case *loomv1.AddSkillRequest_Inline:
		if src.Inline == nil {
			return nil, nil, noopCleanup, fmt.Errorf("inline source is nil")
		}
		// Build a temp dir mirroring the on-disk layout so the same
		// ReadSkill code path runs. Keeps a single source of truth
		// for SKILL.md parsing instead of duplicating logic for
		// inline sources.
		dir, cleanup, err := s.inlineToTempDir(src.Inline)
		if err != nil {
			return nil, nil, noopCleanup, err
		}
		skill, err := importer.ReadSkill(dir)
		if err != nil {
			cleanup()
			return nil, nil, noopCleanup, fmt.Errorf("read inline skill: %w", err)
		}
		if !importer.IsSafeSkillName(skill.Name) {
			cleanup()
			return nil, nil, noopCleanup, fmt.Errorf("unsafe skill name %q", skill.Name)
		}
		known[skill.Name] = true
		return skill, known, cleanup, nil

	case nil:
		return nil, nil, noopCleanup, fmt.Errorf("source is required (skill_dir, zip_archive, or inline)")
	default:
		return nil, nil, noopCleanup, fmt.Errorf("unknown source variant %T", src)
	}
}

// =============================================================================
// Zip extraction (security-aware)
// =============================================================================

// extractZipToTempDir unzips the supplied bytes into a fresh temp
// directory under os.TempDir. Returns the absolute path of the temp
// directory plus a cleanup function that removes it.
//
// Security:
//   - Rejects entries with absolute paths or "..", which would let a
//     hostile zip escape the temp dir. (zip slip — CVE-2018-1002200
//     class.)
//   - Rejects entries whose decompressed name is empty.
//   - Caps total decompressed size at 64MB so a zip-bomb can't OOM
//     the server.
func (s *SkillsImportServer) extractZipToTempDir(data []byte, prefix string) (string, func(), error) {
	const maxBytes = 64 * 1024 * 1024
	if len(data) == 0 {
		return "", noopCleanup, fmt.Errorf("zip_archive is empty")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", noopCleanup, fmt.Errorf("read zip: %w", err)
	}

	tempDir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", noopCleanup, fmt.Errorf("mkdtemp: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tempDir) }

	var totalBytes int64
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if name == "." || name == "" {
			continue
		}
		// Reject absolute archive paths.
		if filepath.IsAbs(name) {
			cleanup()
			return "", noopCleanup, fmt.Errorf("zip entry %q escapes archive root", f.Name)
		}

		dest := filepath.Join(tempDir, name)
		// Resolve destination relative to tempDir and ensure it does not escape.
		rel, err := filepath.Rel(tempDir, dest)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			cleanup()
			return "", noopCleanup, fmt.Errorf("zip entry %q escapes archive root", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o750); err != nil {
				cleanup()
				return "", noopCleanup, fmt.Errorf("mkdir %s: %w", dest, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			cleanup()
			return "", noopCleanup, fmt.Errorf("mkdir parent of %s: %w", dest, err)
		}

		rc, err := f.Open()
		if err != nil {
			cleanup()
			return "", noopCleanup, fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}
		out, err := os.Create(dest) // #nosec G304 -- dest is under tempDir, validated above
		if err != nil {
			_ = rc.Close()
			cleanup()
			return "", noopCleanup, fmt.Errorf("create %s: %w", dest, err)
		}
		// Cap bytes to prevent zip bomb.
		written, err := copyWithLimit(out, rc, maxBytes-totalBytes)
		_ = rc.Close()
		_ = out.Close()
		if err != nil {
			cleanup()
			return "", noopCleanup, fmt.Errorf("extract %s: %w", f.Name, err)
		}
		totalBytes += written
		if totalBytes >= maxBytes {
			cleanup()
			return "", noopCleanup, fmt.Errorf("zip exceeds %d bytes uncompressed", maxBytes)
		}
	}
	return tempDir, cleanup, nil
}

// inlineToTempDir materializes an InlineSkill record into a directory
// of the on-disk layout (SKILL.md + references/*.md) so the standard
// ReadSkill code path can parse it. Avoids a second source-format
// parser.
func (s *SkillsImportServer) inlineToTempDir(inline *loomv1.InlineSkill) (string, func(), error) {
	if inline.Name == "" {
		return "", noopCleanup, fmt.Errorf("inline skill missing name")
	}
	if !importer.IsSafeSkillName(inline.Name) {
		return "", noopCleanup, fmt.Errorf("unsafe inline skill name %q", inline.Name)
	}
	if inline.SkillMd == "" {
		return "", noopCleanup, fmt.Errorf("inline skill missing skill_md")
	}

	parent, err := os.MkdirTemp("", "loom-inline-skill-")
	if err != nil {
		return "", noopCleanup, fmt.Errorf("mkdtemp: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(parent) }

	skillDir := filepath.Join(parent, inline.Name)
	if err := os.MkdirAll(skillDir, 0o750); err != nil {
		cleanup()
		return "", noopCleanup, fmt.Errorf("mkdir skill dir: %w", err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(inline.SkillMd), 0o600); err != nil {
		cleanup()
		return "", noopCleanup, fmt.Errorf("write SKILL.md: %w", err)
	}
	if len(inline.References) > 0 {
		refDir := filepath.Join(skillDir, "references")
		if err := os.MkdirAll(refDir, 0o750); err != nil {
			cleanup()
			return "", noopCleanup, fmt.Errorf("mkdir references: %w", err)
		}
		for fname, body := range inline.References {
			// Defense-in-depth: reject names that aren't bare *.md.
			if filepath.Base(fname) != fname || !strings.HasSuffix(fname, ".md") {
				cleanup()
				return "", noopCleanup, fmt.Errorf("inline reference %q must be a bare *.md filename", fname)
			}
			refPath := filepath.Join(refDir, fname)
			if err := os.WriteFile(refPath, []byte(body), 0o600); err != nil {
				cleanup()
				return "", noopCleanup, fmt.Errorf("write reference %s: %w", fname, err)
			}
		}
	}
	return skillDir, cleanup, nil
}

// =============================================================================
// Helpers
// =============================================================================

// classifierIfRequested returns the server's classifier provider when
// the request opted in, nil otherwise. Avoids surfacing the provider
// to non-classify imports even though the server holds it.
func (s *SkillsImportServer) classifierIfRequested(classify bool) types.LLMProvider {
	if classify {
		return s.classify
	}
	return nil
}

// taxonomyForRequest resolves the request's taxonomy_override bytes to
// an importer.Taxonomy, falling back to DefaultTaxonomy when empty.
// Validates malformed YAML at request time so the LLM never sees a
// broken taxonomy.
func (s *SkillsImportServer) taxonomyForRequest(override []byte) (importer.Taxonomy, error) {
	if len(override) == 0 {
		return importer.DefaultTaxonomy(), nil
	}
	return importer.ParseTaxonomy(override)
}

// graphContextForClassify builds a GraphContext from the persisted
// index when classification was requested. Returns a non-nil pointer
// to an empty GraphContext when classify is false (consistent shape
// for ProcessOptions.Graph).
func (s *SkillsImportServer) graphContextForClassify(ctx context.Context, classify bool) (*importer.GraphContext, error) {
	if !classify {
		empty := importer.GraphContext{}
		return &empty, nil
	}
	g, err := importer.BuildGraphContext(ctx, s.store)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// knownNamesFromCatalog returns the set of skill names currently in
// the catalog. Used by ProcessSkill to resolve cross-skill refs in a
// newly-added skill against the existing catalog.
func (s *SkillsImportServer) knownNamesFromCatalog() map[string]bool {
	all := s.skillLib.List()
	known := make(map[string]bool, len(all))
	for _, sk := range all {
		known[sk.Name] = true
	}
	return known
}

// chooseDomainFromExistingSkill mirrors importer.ChooseDomain on a
// loaded skills.Skill (vs the importer.Skill the importer holds).
func chooseDomainFromExistingSkill(s *skills.Skill) string {
	if s.Domain != "" {
		return s.Domain
	}
	return "general"
}

// skillToImporterSkill converts a loaded skills.Skill to the
// importer.Skill shape the classifier expects. Description and
// when-to-use bullets are the relevant fields for classification.
func skillToImporterSkill(s *skills.Skill) *importer.Skill {
	imp := &importer.Skill{
		Name:        s.Name,
		Description: s.Description,
	}
	// when-to-use bullets are not directly accessible from a loaded
	// skill — they were inlined into Prompt.Instructions at
	// import time. Leaving WhenToUse empty here means the
	// classifier's prompt drops the "when-to-use" section for
	// re-classification calls. Acceptable: description alone is
	// usually the strongest signal anyway.
	return imp
}

// noopCleanup is a placeholder cleanup function for code paths that
// don't allocate any temporary resources.
func noopCleanup() {}

// copyWithLimit copies from src to dst stopping at limit bytes.
// Returns the number of bytes copied. When the source exceeds limit,
// returns an error. EOF on src is the normal terminator and returns
// (n, nil).
func copyWithLimit(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("limit must be positive")
	}
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if total+int64(n) > limit {
				return total, fmt.Errorf("source exceeds %d byte limit", limit)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return total, nil
			}
			return total, err
		}
	}
}

// =============================================================================
// Proto conversion
// =============================================================================

// skillResultToProto converts importer.SkillResult to the proto
// SkillResult shape. The Skill field is dropped (the proto carries
// only scalar bits).
func skillResultToProto(r importer.SkillResult) *loomv1.SkillResult {
	out := &loomv1.SkillResult{
		Index:                int32(r.Index), // #nosec G115 -- bounded by file count
		Total:                int32(r.Total), // #nosec G115 -- bounded by file count
		Outcome:              outcomeToProto(r.Outcome),
		DestPath:             r.DestPath,
		YamlBytes:            int32(r.YAMLBytes), // #nosec G115 -- capped well under int32
		Domain:               r.Domain,
		TriggerMode:          r.TriggerMode,
		ClassifyEnabled:      r.ClassifyEnabled,
		ClassifyPath:         r.ClassifyPath,
		ClassifySkipped:      r.ClassifySkipped,
		LinkedSkillsTotal:    int32(r.LinkedSkillsTotal),    // #nosec G115 -- bounded by file count
		LinkedSkillsResolved: int32(r.LinkedSkillsResolved), // #nosec G115 -- bounded by file count
		LinkedSkillsDropped:  r.LinkedSkillsDropped,
		ReferencesInlined:    int32(r.ReferencesInlined),  // #nosec G115 -- bounded by file count
		KeywordsCount:        int32(r.KeywordsCount),      // #nosec G115 -- bounded by importer.MaxKeywords
		SlashCommandsCount:   int32(r.SlashCommandsCount), // #nosec G115 -- small constant
	}
	if r.Skill != nil {
		out.SkillName = r.Skill.Name
	}
	if r.ClassifyError != nil {
		out.ClassifyError = r.ClassifyError.Error()
	}
	if r.Err != nil {
		out.Err = r.Err.Error()
	}
	return out
}

// discoveryReportToProto converts importer.DiscoveryReport to the
// BannerInfo proto shape.
func discoveryReportToProto(d importer.DiscoveryReport) *loomv1.BannerInfo {
	skipped := make([]*loomv1.SkipNote, 0, len(d.Skipped))
	for _, s := range d.Skipped {
		skipped = append(skipped, &loomv1.SkipNote{Name: s.Name, Reason: s.Reason})
	}
	failed := make([]*loomv1.FailNote, 0, len(d.Failed))
	for _, f := range d.Failed {
		errMsg := ""
		if f.Err != nil {
			errMsg = f.Err.Error()
		}
		failed = append(failed, &loomv1.FailNote{Name: f.Name, Error: errMsg})
	}
	return &loomv1.BannerInfo{
		SourceDir:       d.SourceDir,
		OutDir:          d.OutDir,
		DryRun:          d.DryRun,
		Overwrite:       d.Overwrite,
		Classify:        d.Classify,
		TotalImportable: int32(d.TotalImportable), // #nosec G115 -- bounded by file count
		Skipped:         skipped,
		Failed:          failed,
		MaxNameWidth:    int32(d.MaxNameWidth), // #nosec G115 -- small int
	}
}

// outcomeToProto maps the Go enum to its proto equivalent.
func outcomeToProto(o importer.Outcome) loomv1.Outcome {
	switch o {
	case importer.OutcomeWrote:
		return loomv1.Outcome_OUTCOME_WROTE
	case importer.OutcomeWouldWrite:
		return loomv1.Outcome_OUTCOME_WOULD_WRITE
	case importer.OutcomeSkipped:
		return loomv1.Outcome_OUTCOME_SKIPPED
	case importer.OutcomeFailed:
		return loomv1.Outcome_OUTCOME_FAILED
	default:
		return loomv1.Outcome_OUTCOME_UNSPECIFIED
	}
}
