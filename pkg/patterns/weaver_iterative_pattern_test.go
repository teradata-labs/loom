// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package patterns

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The weaver's iterative pattern is prompt material: its template, error
// table and best practices must teach one shape, the one the YAML loader
// reads (a restart_policy map, stage ids as triggers, no restart_topic).
func TestWeaverIterativePattern_TeachesTheLoaderShape(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "patterns", "weaver", "iterative_workflow.yaml"))
	require.NoError(t, err)
	var p Pattern
	require.NoError(t, yaml.Unmarshal(data, &p))

	assert.NotContains(t, p.BestPractices, "restart_policy: on_failure restarts")
	assert.NotContains(t, p.BestPractices, "are keywords")
	assert.Contains(t, p.BestPractices, "restart_policy is a map")
	assert.Contains(t, p.BestPractices, "stage ids")

	workflow := p.Templates["workflow"].SQL
	assert.Contains(t, workflow, "restart_policy:\n    enabled: true")
	assert.Contains(t, workflow, "restartable_stages:")
	assert.NotContains(t, workflow, "restart_topic:", "an omitted topic gives each run its own")
	names := map[string]bool{}
	for _, prm := range p.Parameters {
		names[prm.Name] = true
	}
	for _, want := range []string{"name", "initial_prompt", "max_iterations"} {
		assert.True(t, names[want], "parameter %q declared", want)
	}
}
