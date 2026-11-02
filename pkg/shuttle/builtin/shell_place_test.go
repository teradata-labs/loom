// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build !windows

package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/session"
)

// A tool built for a folder runs commands in that folder and in what it was
// allowed besides — whatever LOOM_SANDBOX_DIR says, because two agents for two
// workspaces live in one process and the variable is one for all of them.
func TestAPlacedShellRunsOnlyWhereItWasPlaced(t *testing.T) {
	root := t.TempDir()
	mine, theirs, extra := filepath.Join(root, "mine"), filepath.Join(root, "theirs"), filepath.Join(root, "extra")
	for _, d := range []string{filepath.Join(mine, "repo"), theirs, extra, mine + "-sibling"} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	t.Setenv("LOOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("LOOM_SANDBOX_DIR", theirs)
	ctx := session.WithSessionID(context.Background(), "s1")
	runs := func(tool *ShellExecuteTool, dir string) bool {
		res, err := tool.Execute(ctx, map[string]interface{}{"command": "pwd", "working_dir": dir})
		require.NoError(t, err)
		return res.Success
	}

	tool := NewShellExecuteTool(mine)
	require.True(t, runs(tool, filepath.Join(mine, "repo")), "refused in its own folder")
	require.False(t, runs(tool, theirs), "ran in the folder LOOM_SANDBOX_DIR names, which is not its own")
	require.False(t, runs(tool, mine+"-sibling"), "a folder whose name starts with its own is not its own")
	require.False(t, runs(tool, extra), "ran in a folder it was not allowed")

	tool.Allow(extra)
	require.True(t, runs(tool, extra), "refused in a folder it was allowed")
}
