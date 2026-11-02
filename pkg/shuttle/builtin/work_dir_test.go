// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// A turn that says where it works is served there by tools built somewhere
// else: a command starts in the turn's directory, a relative working_dir is
// beneath it, and a relative path is read and written there. The same tools,
// in a turn that says nothing, work where they were built.
func TestToolsWorkWhereTheTurnSays(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pwd")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("LOOM_SANDBOX_DIR", root)
	built := filepath.Join(root, "built")
	turn := filepath.Join(root, "turn")
	for dir, says := range map[string]string{built: "built", turn: "turn"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "WHICH"), []byte("the-"+says+"-copy"), 0o644))
	}
	shell, read, write := NewShellExecuteTool(built), NewFileReadTool(built), NewFileWriteTool(built)

	pwd := func(ctx context.Context, params map[string]interface{}) string {
		params["command"] = "pwd -P"
		res, err := shell.Execute(ctx, params)
		require.NoError(t, err)
		require.True(t, res.Success, "%+v", res.Error)
		return strings.TrimSpace(res.Data.(map[string]interface{})["stdout"].(string))
	}
	reads := func(ctx context.Context, params map[string]interface{}) string {
		res, err := read.Execute(ctx, params)
		require.NoError(t, err)
		require.True(t, res.Success, "%+v", res.Error)
		return flat(res.Data)
	}

	for name, tc := range map[string]struct {
		ctx  context.Context
		home string
	}{
		"a turn that says where it works": {shuttle.ContextWithWorkDir(context.Background(), turn), turn},
		"a turn that says nothing":        {context.Background(), built},
	} {
		says := "the-" + filepath.Base(tc.home) + "-copy"
		assert.Equal(t, tc.home, pwd(tc.ctx, map[string]interface{}{}), "%s: where a command started", name)
		assert.Equal(t, filepath.Join(tc.home, "sub"), pwd(tc.ctx, map[string]interface{}{"working_dir": "sub"}),
			"%s: where a relative working_dir led", name)
		assert.Contains(t, reads(tc.ctx, map[string]interface{}{"path": "WHICH"}), says, "%s: a single read", name)
		assert.Contains(t, reads(tc.ctx, map[string]interface{}{"paths": []interface{}{"WHICH"}}), says, "%s: a multi read", name)
		assert.Contains(t, reads(tc.ctx, map[string]interface{}{"paths": []interface{}{"W*"}}), says, "%s: a glob", name)

		res, err := write.Execute(tc.ctx, map[string]interface{}{"path": "written.txt", "content": name})
		require.NoError(t, err)
		require.True(t, res.Success, "%s: %+v", name, res.Error)
		got, err := os.ReadFile(filepath.Join(tc.home, "written.txt"))
		require.NoError(t, err, "%s: the write did not land in its directory", name)
		assert.Equal(t, name, string(got))
	}
}

// flat is a tool result's text, whatever shape it came in.
func flat(data interface{}) string {
	switch d := data.(type) {
	case string:
		return d
	case map[string]interface{}:
		var b strings.Builder
		for _, v := range d {
			b.WriteString(flat(v))
			b.WriteString("\n")
		}
		return b.String()
	case []interface{}:
		var b strings.Builder
		for _, v := range d {
			b.WriteString(flat(v))
			b.WriteString("\n")
		}
		return b.String()
	}
	return ""
}
