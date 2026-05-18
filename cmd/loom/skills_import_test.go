// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/skills"
)

// writeFixtureSkill writes a SKILL.md plus optional references under root/<name>/.
func writeFixtureSkill(t *testing.T, root, name, skillBody string, refs map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillBody), 0o600))
	if len(refs) > 0 {
		refDir := filepath.Join(dir, "references")
		require.NoError(t, os.MkdirAll(refDir, 0o755))
		for fname, body := range refs {
			require.NoError(t, os.WriteFile(filepath.Join(refDir, fname), []byte(body), 0o600))
		}
	}
}

const fixtureSqlFundamentals = `---
name: teradata-sql-fundamentals
description: 'Teradata SQL syntax and fundamentals (DDL/DML, QUALIFY, SAMPLE).'
metadata:
  author: teradata
  version: "1.0"
---

# Teradata SQL Fundamentals

## When to Use

- Writing Teradata-specific SQL (QUALIFY, SAMPLE, TOP)
- Creating and modifying tables (DDL)
- INSERT/UPDATE/DELETE/MERGE operations

## Cross-references

See [teradata-architecture](../teradata-architecture/SKILL.md) for primary index choice.
Also load ` + "`teradata-statistics`" + ` when query plans need stats work.

## CREATE TABLE

` + "```sql" + `
CREATE MULTISET TABLE foo (id INTEGER) PRIMARY INDEX (id);
` + "```" + `
`

const fixtureSqlReference = `# Transaction and Session

ANSI vs Teradata mode differences. SET SESSION MODE.
`

const fixtureSkillIndex = `---
name: teradata-skill-index
description: 'Navigator for Teradata skills.'
metadata:
  author: teradata
  version: "1.0"
---

# Teradata Skill Index

## When to Use

- Use this index FIRST when unsure which Teradata skill to load.

## Routing

| Topic | Skill |
|---|---|
| DDL/DML | ` + "`teradata-sql-fundamentals`" + ` |
| Stats | ` + "`teradata-statistics`" + ` |
| Indexes | ` + "`teradata-architecture`" + ` |
`

const fixtureAgentSkillBuilder = `---
name: agent-skill-builder
description: 'Build SKILL.md files.'
metadata:
  author: skill-builder
  version: "1.0"
---

# Agent Skill Builder

Used to author new skills.
`

func TestRunSkillsImport_HappyPath(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()

	writeFixtureSkill(t, src, "teradata-sql-fundamentals", fixtureSqlFundamentals,
		map[string]string{"transaction-and-session.md": fixtureSqlReference})
	writeFixtureSkill(t, src, "teradata-skill-index", fixtureSkillIndex, nil)
	writeFixtureSkill(t, src, "agent-skill-builder", fixtureAgentSkillBuilder, nil)

	prevOut := skillsImportOutDir
	prevForce := skillsImportOverride
	skillsImportOutDir = out
	skillsImportOverride = true
	t.Cleanup(func() {
		skillsImportOutDir = prevOut
		skillsImportOverride = prevForce
	})

	require.NoError(t, runSkillsImport(nil, []string{src}))

	// agent-skill-builder must be skipped, the other two must exist.
	assertFileExists(t, filepath.Join(out, "teradata-sql-fundamentals.yaml"))
	assertFileExists(t, filepath.Join(out, "teradata-skill-index.yaml"))
	assertFileMissing(t, filepath.Join(out, "agent-skill-builder.yaml"))

	// Each generated YAML must round-trip through the real loader.
	sql, err := skills.LoadSkill(filepath.Join(out, "teradata-sql-fundamentals.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "teradata-sql-fundamentals", sql.Name)
	assert.Equal(t, "teradata", sql.Domain)
	assert.Contains(t, sql.Trigger.SlashCommands, "/teradata-sql-fundamentals")
	// References were inlined.
	assert.Contains(t, sql.Prompt.Instructions, "## Reference: Transaction And Session")
	assert.Contains(t, sql.Prompt.Instructions, "ANSI vs Teradata mode differences")
	// Cross-skill markdown link became skill_refs (capped at 2).
	assert.LessOrEqual(t, len(sql.SkillRefs), 2)
	assert.Contains(t, sql.SkillRefs, "teradata-architecture")
	assert.Contains(t, sql.SkillRefs, "teradata-statistics")

	idx, err := skills.LoadSkill(filepath.Join(out, "teradata-skill-index.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "meta-agent", idx.Domain)
	assert.Equal(t, skills.SkillActivationMode("ALWAYS"), idx.Trigger.Mode)
	assert.Contains(t, idx.Trigger.SlashCommands, "/skill-index")
	// Parent index leaves skill_refs empty (loader caps at 2; routing table
	// is in the prompt body instead).
	assert.Empty(t, idx.SkillRefs)
	// The inlined "Linked Skills" block lists each skill referenced by the
	// routing table.
	assert.Contains(t, idx.Prompt.Instructions, "## Linked Skills (Loom Catalog)")
	for _, n := range []string{"teradata-sql-fundamentals", "teradata-statistics", "teradata-architecture"} {
		assert.Contains(t, idx.Prompt.Instructions, n)
	}
}

func TestRunSkillsImport_DryRunWritesNothing(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()
	writeFixtureSkill(t, src, "teradata-sql-fundamentals", fixtureSqlFundamentals, nil)

	prevOut := skillsImportOutDir
	prevDry := skillsImportDryRun
	skillsImportOutDir = out
	skillsImportDryRun = true
	t.Cleanup(func() {
		skillsImportOutDir = prevOut
		skillsImportDryRun = prevDry
	})

	require.NoError(t, runSkillsImport(nil, []string{src}))

	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	assert.Empty(t, entries, "dry-run must not write any files")
}

func TestRunSkillsImport_RefusesToOverwriteWithoutForce(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()
	writeFixtureSkill(t, src, "teradata-sql-fundamentals", fixtureSqlFundamentals, nil)

	dst := filepath.Join(out, "teradata-sql-fundamentals.yaml")
	require.NoError(t, os.WriteFile(dst, []byte("apiVersion: loom/v1\n# placeholder\n"), 0o600))
	original, err := os.ReadFile(dst)
	require.NoError(t, err)

	prevOut := skillsImportOutDir
	prevForce := skillsImportOverride
	skillsImportOutDir = out
	skillsImportOverride = false
	t.Cleanup(func() {
		skillsImportOutDir = prevOut
		skillsImportOverride = prevForce
	})

	require.NoError(t, runSkillsImport(nil, []string{src}))

	current, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, original, current, "existing file must remain untouched without --force")
}

func TestSplitFrontmatter(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		raw := []byte("---\nname: foo\ndescription: bar\nmetadata:\n  author: a\n  version: \"1.0\"\n---\n\n# Body\n\nhello\n")
		fm, body, err := splitFrontmatter(raw)
		require.NoError(t, err)
		assert.Equal(t, "foo", fm.Name)
		assert.Equal(t, "bar", fm.Description)
		assert.Equal(t, "a", fm.Metadata.Author)
		assert.True(t, strings.HasPrefix(body, "# Body"))
	})
	t.Run("missing frontmatter", func(t *testing.T) {
		_, _, err := splitFrontmatter([]byte("# just a heading\n"))
		assert.Error(t, err)
	})
}

func TestExtractLinkedSkillNames(t *testing.T) {
	body := `Some text.

See [arch](../teradata-architecture/SKILL.md) and ` + "`teradata-statistics`" + ` for context.
Also [self-ref](../teradata-foo/SKILL.md#section).
`
	got := extractLinkedSkillNames(body, "teradata-foo")
	assert.Equal(t, []string{"teradata-architecture", "teradata-statistics"}, got)
}

func TestParseWhenToUseBullets(t *testing.T) {
	body := `# Title

## When to Use

- First bullet
- Second bullet
* Third bullet

## Next section

- Should not appear
`
	bullets := parseWhenToUseBullets(body)
	assert.Equal(t, []string{"First bullet", "Second bullet", "Third bullet"}, bullets)
}

func TestRenderSkillYAML_RoundTripsThroughLoader(t *testing.T) {
	imp := &importedSkill{
		Name:         "teradata-sql-fundamentals",
		Description:  "Teradata SQL fundamentals.",
		Author:       "teradata",
		Version:      "1.0",
		Body:         "# Body\n\n## When to Use\n\n- Writing SQL\n",
		LinkedSkills: []string{"teradata-architecture"},
	}
	bs, err := renderSkillYAML(imp)
	require.NoError(t, err)

	tmp := filepath.Join(t.TempDir(), "out.yaml")
	require.NoError(t, os.WriteFile(tmp, bs, 0o600))
	got, err := skills.LoadSkill(tmp)
	require.NoError(t, err)
	assert.Equal(t, "teradata-sql-fundamentals", got.Name)
	assert.Equal(t, "teradata", got.Domain)
	assert.Equal(t, []string{"teradata-architecture"}, got.SkillRefs)
}

func TestNormalizeCrossSkillLinks(t *testing.T) {
	in := "See [arch](../teradata-architecture/SKILL.md) and [stats](../teradata-statistics/SKILL.md#section)."
	out := normalizeCrossSkillLinks(in)
	assert.Contains(t, out, "[arch](skill:teradata-architecture)")
	assert.Contains(t, out, "[stats](skill:teradata-statistics)")
	assert.NotContains(t, out, "../teradata-")
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file %s to exist: %v", path, err)
	}
}

func assertFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected file %s to be missing", path)
	}
}
