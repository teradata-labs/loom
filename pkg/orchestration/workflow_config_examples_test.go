// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLoadWorkflowFromYAML_ShippedOrchestrationExamples parses every shipped
// orchestration example through the canonical converter — the path
// `looms workflow validate` and ExecuteWorkflow use. This is the runnability
// side of the shipped-example contract: pkg/agent's example sweep proves the
// registry can load these files, and this sweep proves the converter can
// execute them, so an example can't be registry-loadable but not runnable.
func TestLoadWorkflowFromYAML_ShippedOrchestrationExamples(t *testing.T) {
	examplesDir := filepath.Join("..", "..", "examples", "reference", "workflows", "orchestration-patterns")

	var files []string
	err := filepath.WalkDir(examplesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".yaml" {
			files = append(files, path)
		}
		return nil
	})
	require.NoError(t, err, "examples directory missing (update the path)")
	require.NotEmpty(t, files)

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			pattern, err := LoadWorkflowFromYAML(file)
			require.NoError(t, err, "shipped example failed canonical conversion: %s", file)
			require.NotNil(t, pattern.GetPattern(), "no pattern produced for %s", file)
		})
	}
}
