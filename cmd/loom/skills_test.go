// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSkillsMigrate_LegacyAgentSchema(t *testing.T) {
	in := `
agent:
  name: test-agent
  description: legacy shape
  skills:
    enabled: true
    enabled_skills: [sql-optimization, code-review]
    disabled_skills: [archive-skill]
    skills_dir: ./skills
`
	dir := t.TempDir()
	src := filepath.Join(dir, "agent.yaml")
	require.NoError(t, os.WriteFile(src, []byte(in), 0o600))

	// Capture stdout.
	r, w, err := os.Pipe()
	require.NoError(t, err)
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	err = runSkillsMigrate(nil, []string{src})
	require.NoError(t, err)
	require.NoError(t, w.Close())

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	out := buf.String()
	assert.Contains(t, out, "bindings:")
	assert.Contains(t, out, "name: sql-optimization")
	assert.Contains(t, out, "name: code-review")
	assert.Contains(t, out, "mode: EAGER")
	assert.Contains(t, out, "_migration_note")

	// Round-trip: the output is parseable YAML and preserves enabled_skills
	// as a fallback for older readers.
	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &parsed))
	skills := parsed["agent"].(map[string]interface{})["skills"].(map[string]interface{})
	require.Contains(t, skills, "bindings")
	require.Contains(t, skills, "enabled_skills",
		"legacy enabled_skills must be preserved for fallback consumers")
}

func TestSkillsMigrate_K8sSchema(t *testing.T) {
	in := `
apiVersion: loom/v1
kind: Agent
metadata:
  name: test-agent
spec:
  skills:
    enabled: true
    enabled_skills: [pr-feedback]
`
	dir := t.TempDir()
	src := filepath.Join(dir, "agent.yaml")
	require.NoError(t, os.WriteFile(src, []byte(in), 0o600))

	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	err := runSkillsMigrate(nil, []string{src})
	require.NoError(t, err)
	require.NoError(t, w.Close())

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	out := buf.String()
	assert.Contains(t, out, "bindings:")
	assert.Contains(t, out, "name: pr-feedback")
}

func TestSkillsMigrate_NoEnabledSkillsAnnotates(t *testing.T) {
	in := `
agent:
  name: test
  skills:
    enabled: true
    disabled_skills: [archive]
`
	dir := t.TempDir()
	src := filepath.Join(dir, "agent.yaml")
	require.NoError(t, os.WriteFile(src, []byte(in), 0o600))

	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	err := runSkillsMigrate(nil, []string{src})
	require.NoError(t, err)
	require.NoError(t, w.Close())

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	out := buf.String()
	assert.Contains(t, out, "_migration_note",
		"no enabled_skills must produce a migration_note explaining the LAZY default")
	assert.NotContains(t, out, "bindings:",
		"empty enabled_skills must NOT synthesize a bindings list")
}

func TestSkillsMigrate_PreservesExistingBindings(t *testing.T) {
	in := `
agent:
  name: test
  skills:
    enabled: true
    enabled_skills: [old-skill]
    bindings:
      - name: hand-authored
        mode: LAZY
`
	dir := t.TempDir()
	src := filepath.Join(dir, "agent.yaml")
	require.NoError(t, os.WriteFile(src, []byte(in), 0o600))

	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	err := runSkillsMigrate(nil, []string{src})
	require.NoError(t, err)
	require.NoError(t, w.Close())

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	out := buf.String()

	// Hand-authored binding must be preserved verbatim.
	assert.Contains(t, out, "name: hand-authored")
	assert.Contains(t, out, "mode: LAZY")
	// Migration must NOT clobber existing bindings with synthesized EAGER
	// entries from enabled_skills.
	assert.NotContains(t, out, "name: old-skill")
}

func TestSkillsMigrate_MissingFileErrors(t *testing.T) {
	err := runSkillsMigrate(nil, []string{"/no/such/file.yaml"})
	assert.Error(t, err)
}

func TestSkillsMigrate_NoSkillsBlockErrors(t *testing.T) {
	in := `
agent:
  name: test
`
	dir := t.TempDir()
	src := filepath.Join(dir, "agent.yaml")
	require.NoError(t, os.WriteFile(src, []byte(in), 0o600))

	err := runSkillsMigrate(nil, []string{src})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no skills block")
}
