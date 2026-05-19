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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/skills/index"
	"github.com/teradata-labs/loom/pkg/types"
)

// =============================================================================
// Fixtures
// =============================================================================

const fixtureSkillSrc = `---
name: test-fixture-skill
description: 'Test fixture skill for SkillsImportService unit tests.'
metadata:
  author: test
  version: "1.0"
---

# Test Fixture Skill

## When to Use

- Whenever a server-side unit test needs a real importer.Skill.

## Body

Some test body content.
`

// writeFixtureDir creates <root>/<name>/SKILL.md for tests.
func writeFixtureDir(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600))
	return dir
}

// buildFixtureZip writes name/SKILL.md into a zip and returns the bytes.
func buildFixtureZip(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name + "/SKILL.md")
	require.NoError(t, err)
	_, err = w.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// buildHostileZip produces a zip that tries to escape the temp dir
// via a path-traversal entry. Used to verify zip-slip rejection.
func buildHostileZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../etc/escape.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("hostile"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// makeServer returns a SkillsImportServer wired against a temp
// skills_dir with no classifier configured. Suitable for tests that
// don't exercise the LLM path.
func makeServer(t *testing.T) *SkillsImportServer {
	t.Helper()
	skillsDir := t.TempDir()

	// Registry is required by the server constructor; build a minimal
	// one against an in-memory SQLite-equivalent. We use the registry's
	// own NewRegistry but with a tempdir config, then close in cleanup.
	regCfg := agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    filepath.Join(t.TempDir(), "registry.db"),
		Logger:    nil,
	}
	reg, err := agent.NewRegistry(regCfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })

	lib := skills.NewLibrary(skills.WithSearchPaths(skillsDir))
	store := index.NewMemoryStore()

	srv, err := NewSkillsImportServer(SkillsImportServerConfig{
		Registry:      reg,
		Store:         store,
		SkillLib:      lib,
		DefaultOutDir: skillsDir,
	})
	require.NoError(t, err)
	return srv
}

// =============================================================================
// Constructor validation
// =============================================================================

func TestNewSkillsImportServer_RequiredDeps(t *testing.T) {
	skillsDir := t.TempDir()
	regCfg := agent.RegistryConfig{
		ConfigDir: t.TempDir(),
		DBPath:    filepath.Join(t.TempDir(), "registry.db"),
	}
	reg, err := agent.NewRegistry(regCfg)
	require.NoError(t, err)
	defer func() { _ = reg.Close() }()

	cases := []struct {
		name string
		cfg  SkillsImportServerConfig
		err  string
	}{
		{
			name: "missing registry",
			cfg: SkillsImportServerConfig{
				Store:         index.NewMemoryStore(),
				SkillLib:      skills.NewLibrary(),
				DefaultOutDir: skillsDir,
			},
			err: "Registry is required",
		},
		{
			name: "missing store",
			cfg: SkillsImportServerConfig{
				Registry:      reg,
				SkillLib:      skills.NewLibrary(),
				DefaultOutDir: skillsDir,
			},
			err: "Store is required",
		},
		{
			name: "missing skill lib",
			cfg: SkillsImportServerConfig{
				Registry:      reg,
				Store:         index.NewMemoryStore(),
				DefaultOutDir: skillsDir,
			},
			err: "SkillLib is required",
		},
		{
			name: "missing default out dir",
			cfg: SkillsImportServerConfig{
				Registry: reg,
				Store:    index.NewMemoryStore(),
				SkillLib: skills.NewLibrary(),
			},
			err: "DefaultOutDir is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSkillsImportServer(tc.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}
}

// =============================================================================
// AddSkill: source variants
// =============================================================================

func TestAddSkill_FromDir(t *testing.T) {
	srv := makeServer(t)
	srcRoot := t.TempDir()
	skillDir := writeFixtureDir(t, srcRoot, "test-fixture-skill", fixtureSkillSrc)

	resp, err := srv.AddSkill(context.Background(), &loomv1.AddSkillRequest{
		Source: &loomv1.AddSkillRequest_SkillDir{SkillDir: skillDir},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Result)
	assert.Equal(t, loomv1.Outcome_OUTCOME_WROTE, resp.Result.Outcome,
		"unexpected outcome: %s (err=%s)", resp.Result.Outcome, resp.Result.Err)
	assert.Equal(t, "test-fixture-skill", resp.Result.SkillName)

	// File landed at default out dir.
	dst := filepath.Join(srv.defaultOutDir, "test-fixture-skill.yaml")
	_, err = os.Stat(dst)
	assert.NoError(t, err, "rendered YAML must exist at %s", dst)
}

func TestAddSkill_FromZip(t *testing.T) {
	srv := makeServer(t)
	zipBytes := buildFixtureZip(t, "test-fixture-skill", fixtureSkillSrc)

	resp, err := srv.AddSkill(context.Background(), &loomv1.AddSkillRequest{
		Source: &loomv1.AddSkillRequest_ZipArchive{ZipArchive: zipBytes},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Result)
	assert.Equal(t, loomv1.Outcome_OUTCOME_WROTE, resp.Result.Outcome,
		"unexpected outcome: %s (err=%s)", resp.Result.Outcome, resp.Result.Err)
}

func TestAddSkill_FromInline(t *testing.T) {
	srv := makeServer(t)

	resp, err := srv.AddSkill(context.Background(), &loomv1.AddSkillRequest{
		Source: &loomv1.AddSkillRequest_Inline{
			Inline: &loomv1.InlineSkill{
				Name:    "test-fixture-skill",
				SkillMd: fixtureSkillSrc,
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Result)
	assert.Equal(t, loomv1.Outcome_OUTCOME_WROTE, resp.Result.Outcome,
		"unexpected outcome: %s (err=%s)", resp.Result.Outcome, resp.Result.Err)
}

func TestAddSkill_RejectsHostileZip(t *testing.T) {
	srv := makeServer(t)
	zipBytes := buildHostileZip(t)

	_, err := srv.AddSkill(context.Background(), &loomv1.AddSkillRequest{
		Source: &loomv1.AddSkillRequest_ZipArchive{ZipArchive: zipBytes},
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "escapes archive root")
}

func TestAddSkill_RejectsEmptyZip(t *testing.T) {
	srv := makeServer(t)
	_, err := srv.AddSkill(context.Background(), &loomv1.AddSkillRequest{
		Source: &loomv1.AddSkillRequest_ZipArchive{ZipArchive: []byte{}},
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "empty")
}

func TestAddSkill_RejectsNoSource(t *testing.T) {
	srv := makeServer(t)
	_, err := srv.AddSkill(context.Background(), &loomv1.AddSkillRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestAddSkill_RejectsUnsafeInlineName(t *testing.T) {
	srv := makeServer(t)
	_, err := srv.AddSkill(context.Background(), &loomv1.AddSkillRequest{
		Source: &loomv1.AddSkillRequest_Inline{
			Inline: &loomv1.InlineSkill{
				Name:    "../etc/passwd",
				SkillMd: fixtureSkillSrc,
			},
		},
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestAddSkill_RejectsClassifyWithoutProvider(t *testing.T) {
	srv := makeServer(t)
	srcRoot := t.TempDir()
	skillDir := writeFixtureDir(t, srcRoot, "test-fixture-skill", fixtureSkillSrc)

	_, err := srv.AddSkill(context.Background(), &loomv1.AddSkillRequest{
		Source:   &loomv1.AddSkillRequest_SkillDir{SkillDir: skillDir},
		Classify: true,
	})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// =============================================================================
// AddSkill with classifier
// =============================================================================

// stubClassifyLLM returns canned responses to LLM Chat calls. Used to
// drive the classifier without standing up a real LLM provider.
type stubClassifyLLM struct {
	response string
	calls    int
}

func (s *stubClassifyLLM) Chat(_ context.Context, _ []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	s.calls++
	return &types.LLMResponse{Content: s.response}, nil
}
func (s *stubClassifyLLM) Name() string  { return "stub-classify" }
func (s *stubClassifyLLM) Model() string { return "stub-classify-model" }

func TestAddSkill_WithClassifier_AssignsParentIndexPath(t *testing.T) {
	srv := makeServer(t)
	// Inject a stub classifier that returns a valid path under
	// the skill's domain. The fixture skill has no domain prefix
	// so importer.ChooseDomain returns "general" — pick a path
	// rooted there.
	stub := &stubClassifyLLM{response: `{"path":"general/test-bucket","reason":"stub"}`}
	srv.classify = stub

	srcRoot := t.TempDir()
	skillDir := writeFixtureDir(t, srcRoot, "test-fixture-skill", fixtureSkillSrc)

	resp, err := srv.AddSkill(context.Background(), &loomv1.AddSkillRequest{
		Source:   &loomv1.AddSkillRequest_SkillDir{SkillDir: skillDir},
		Classify: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, stub.calls)
	require.NotNil(t, resp.Result)
	assert.Equal(t, "general/test-bucket", resp.Result.ClassifyPath)
	assert.True(t, resp.Result.ClassifyEnabled)
	assert.Empty(t, resp.Result.ClassifyError)
}

// =============================================================================
// ClassifySkill
// =============================================================================

func TestClassifySkill_RejectsUnsafeName(t *testing.T) {
	srv := makeServer(t)
	srv.classify = &stubClassifyLLM{response: `{"path":"general/x","reason":"stub"}`}

	_, err := srv.ClassifySkill(context.Background(), &loomv1.ClassifySkillRequest{
		SkillName: "../etc/passwd",
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestClassifySkill_RejectsMissingClassifier(t *testing.T) {
	srv := makeServer(t)
	_, err := srv.ClassifySkill(context.Background(), &loomv1.ClassifySkillRequest{
		SkillName: "test-fixture-skill",
	})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestClassifySkill_RejectsMissingSkill(t *testing.T) {
	srv := makeServer(t)
	srv.classify = &stubClassifyLLM{response: `{"path":"general/x","reason":"stub"}`}

	_, err := srv.ClassifySkill(context.Background(), &loomv1.ClassifySkillRequest{
		SkillName: "nonexistent-skill",
	})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// =============================================================================
// BulkImport: streaming progress
// =============================================================================

// fakeBulkStream collects events into an in-memory slice for assertions.
type fakeBulkStream struct {
	loomv1.SkillsImportService_BulkImportSkillsServer
	ctx    context.Context
	events []*loomv1.BulkImportProgress
}

func (f *fakeBulkStream) Context() context.Context { return f.ctx }
func (f *fakeBulkStream) Send(e *loomv1.BulkImportProgress) error {
	f.events = append(f.events, e)
	return nil
}

func TestBulkImportSkills_StreamsBannerResultSummary(t *testing.T) {
	srv := makeServer(t)
	srcRoot := t.TempDir()
	writeFixtureDir(t, srcRoot, "test-fixture-skill", fixtureSkillSrc)

	stream := &fakeBulkStream{ctx: context.Background()}
	err := srv.BulkImportSkills(&loomv1.BulkImportSkillsRequest{
		Source: &loomv1.BulkImportSkillsRequest_SrcDir{SrcDir: srcRoot},
	}, stream)
	require.NoError(t, err)

	// Expect: 1 banner + 1 result + 1 summary = 3 events.
	require.Len(t, stream.events, 3, "expected banner + result + summary")

	banner := stream.events[0].GetBanner()
	require.NotNil(t, banner, "first event must be banner")
	assert.Equal(t, int32(1), banner.TotalImportable)

	result := stream.events[1].GetResult()
	require.NotNil(t, result, "second event must be result")
	assert.Equal(t, "test-fixture-skill", result.SkillName)
	assert.Equal(t, loomv1.Outcome_OUTCOME_WROTE, result.Outcome)

	summary := stream.events[2].GetSummary()
	require.NotNil(t, summary, "third event must be summary")
	assert.Equal(t, int32(1), summary.Converted)
	assert.Equal(t, int32(0), summary.Failed)
}

func TestBulkImportSkills_DryRunDoesNotWrite(t *testing.T) {
	srv := makeServer(t)
	srcRoot := t.TempDir()
	writeFixtureDir(t, srcRoot, "test-fixture-skill", fixtureSkillSrc)

	stream := &fakeBulkStream{ctx: context.Background()}
	err := srv.BulkImportSkills(&loomv1.BulkImportSkillsRequest{
		Source: &loomv1.BulkImportSkillsRequest_SrcDir{SrcDir: srcRoot},
		DryRun: true,
	}, stream)
	require.NoError(t, err)

	dst := filepath.Join(srv.defaultOutDir, "test-fixture-skill.yaml")
	_, err = os.Stat(dst)
	assert.True(t, os.IsNotExist(err), "dry run must not write")

	// Result should be WOULD_WRITE.
	result := stream.events[1].GetResult()
	require.NotNil(t, result)
	assert.Equal(t, loomv1.Outcome_OUTCOME_WOULD_WRITE, result.Outcome)
}

func TestBulkImportSkills_RejectsClassifyWithoutProvider(t *testing.T) {
	srv := makeServer(t)
	srcRoot := t.TempDir()
	writeFixtureDir(t, srcRoot, "test-fixture-skill", fixtureSkillSrc)

	stream := &fakeBulkStream{ctx: context.Background()}
	err := srv.BulkImportSkills(&loomv1.BulkImportSkillsRequest{
		Source:   &loomv1.BulkImportSkillsRequest_SrcDir{SrcDir: srcRoot},
		Classify: true,
	}, stream)
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestBulkImportSkills_PropagatesTaxonomyError(t *testing.T) {
	srv := makeServer(t)
	srv.classify = &stubClassifyLLM{response: `{"path":"general/x","reason":"stub"}`}

	srcRoot := t.TempDir()
	writeFixtureDir(t, srcRoot, "test-fixture-skill", fixtureSkillSrc)

	stream := &fakeBulkStream{ctx: context.Background()}
	err := srv.BulkImportSkills(&loomv1.BulkImportSkillsRequest{
		Source:           &loomv1.BulkImportSkillsRequest_SrcDir{SrcDir: srcRoot},
		Classify:         true,
		TaxonomyOverride: []byte("not: [valid yaml"),
	}, stream)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}
