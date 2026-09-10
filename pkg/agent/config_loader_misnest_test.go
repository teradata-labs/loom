// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestLoadConfig_MisnestedTaskBoardIsReported pins the silent-off-switch
// finding: task_board is read at agent.memory.task_board and nowhere else, and
// yaml.Unmarshal is not strict, so a block one level too high was dropped with
// no signal while implicit recording — default ON, durable rows — stayed on.
// The loader now names the stray block. It still loads the agent: the block is
// inert either way, and refusing configs that load today would be a break.
func TestLoadConfig_MisnestedTaskBoardIsReported(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		foundAt string
	}{
		{
			name: "document root (the shape the reference doc showed)",
			yaml: `
agent:
  name: misnested-root
  memory:
    type: memory
task_board:
  implicit_tasks:
    mode: disabled
`,
			foundAt: "task_board",
		},
		{
			name: "under agent but not under memory",
			yaml: `
agent:
  name: misnested-agent
  memory:
    type: memory
  task_board:
    implicit_tasks:
      mode: disabled
`,
			foundAt: "agent.task_board",
		},
		{
			name: "k8s style, under spec but not under spec.memory",
			yaml: `
apiVersion: loom.teradata.com/v1
kind: Agent
metadata:
  name: misnested-spec
spec:
  memory:
    type: memory
  task_board:
    implicit_tasks:
      mode: disabled
`,
			foundAt: "spec.task_board",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.WarnLevel)
			restore := zap.ReplaceGlobals(zap.New(core))
			defer restore()

			cfg, err := LoadConfigFromString(tc.yaml)
			require.NoError(t, err, "a stray block must not refuse the load")
			require.NotNil(t, cfg)
			require.Nil(t, cfg.Memory.GetTaskBoard(),
				"rig sanity: the misnested block really is dropped — that is the whole problem")

			entries := logs.FilterMessageSnippet("task_board is configured where the loader does not read it").All()
			require.Len(t, entries, 1, "exactly one warning for one stray block")
			fields := entries[0].ContextMap()
			require.Equal(t, tc.foundAt, fields["found_at"])
			require.Equal(t, true, fields["implicit_tasks_present"],
				"the warning says whether the dropped block carried the off switch")
		})
	}

	t.Run("correctly nested block is silent", func(t *testing.T) {
		core, logs := observer.New(zapcore.WarnLevel)
		restore := zap.ReplaceGlobals(zap.New(core))
		defer restore()

		cfg, err := LoadConfigFromString(`
agent:
  name: nested-right
  memory:
    type: memory
    task_board:
      implicit_tasks:
        mode: disabled
`)
		require.NoError(t, err)
		require.NotNil(t, cfg.Memory.GetTaskBoard(), "the correct nesting is read")
		require.Empty(t, logs.FilterMessageSnippet("task_board is configured where the loader does not read it").All(),
			"a block in the right place must not be flagged")
	})
}
