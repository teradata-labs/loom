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

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// A turn that says where it works is served there by tools built somewhere
// else: a command starts in the turn's directory, a relative working_dir is
// beneath it, and a relative path is read and written there. The same tools,
// in a turn that says nothing, behave as before: a command starts in
// LOOM_SANDBOX_DIR, and relative paths resolve where the tool was built.
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
		ctx   context.Context
		home  string
		start string
	}{
		"a turn that says where it works": {shuttle.ContextWithWorkDir(context.Background(), turn), turn, turn},
		"a turn that says nothing":        {context.Background(), built, root},
	} {
		says := "the-" + filepath.Base(tc.home) + "-copy"
		assert.Equal(t, tc.start, pwd(tc.ctx, map[string]interface{}{}), "%s: where a command started", name)
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

// Under a session's path guard, a turn's directory is allowed the way the
// sandbox directory is, even outside LOOM_DATA_DIR; a directory nobody named
// stays refused.
func TestShellGuardAllowsTheTurnsDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pwd")
	}
	// Under the package directory, not the OS temp dir: the guard allows the
	// temp dir on its own, which would let the refusal half prove nothing.
	base, err := os.MkdirTemp(".", "workdir-guard-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, err = filepath.Abs(base)
	require.NoError(t, err)
	base, err = filepath.EvalSymlinks(base)
	require.NoError(t, err)
	data, turn, other := filepath.Join(base, "data"), filepath.Join(base, "turn"), filepath.Join(base, "other")
	for _, d := range []string{data, turn, other} {
		require.NoError(t, os.MkdirAll(d, 0o750))
	}
	t.Setenv("LOOM_SANDBOX_DIR", data)

	shell := NewShellExecuteTool(data)
	shell.SetLoomDataDir(data)
	ctx := session.WithSessionID(context.Background(), "guarded")

	res, err := shell.Execute(shuttle.ContextWithWorkDir(ctx, turn), map[string]interface{}{"command": "pwd -P"})
	require.NoError(t, err)
	require.True(t, res.Success, "the turn's directory was refused: %+v", res.Error)
	assert.Equal(t, turn, strings.TrimSpace(res.Data.(map[string]interface{})["stdout"].(string)))

	if strings.HasPrefix(other, "/tmp/") || strings.HasPrefix(other, "/private/tmp/") {
		t.Skip("temp dirs are whitelisted on this host; the refusal case cannot be told apart")
	}
	res, err = shell.Execute(shuttle.ContextWithWorkDir(ctx, turn), map[string]interface{}{"command": "pwd", "working_dir": other})
	require.NoError(t, err)
	require.False(t, res.Success, "a directory nobody named was allowed")
	assert.Equal(t, "PATH_RESTRICTED", res.Error.Code)
}

// Every tool that resolves paths against a directory serves a turn in the
// turn's directory: an edit, an image read and a document parse of a relative
// path all find the file there, not where the tool was built.
func TestEveryPathToolWorksWhereTheTurnSays(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("LOOM_SANDBOX_DIR", root)
	built, turn := filepath.Join(root, "built"), filepath.Join(root, "turn")
	require.NoError(t, os.MkdirAll(built, 0o755))
	require.NoError(t, os.MkdirAll(turn, 0o755))
	ctx := shuttle.ContextWithWorkDir(context.Background(), turn)

	require.NoError(t, os.WriteFile(filepath.Join(turn, "f.txt"), []byte("hello turn"), 0o644))
	res, err := NewEditFilesTool(built).Execute(ctx, map[string]interface{}{"edits": []interface{}{
		map[string]interface{}{"path": "f.txt", "find": "hello", "replace": "bye"}}})
	require.NoError(t, err)
	require.True(t, res.Success, "%+v %v", res.Error, res.Data)
	got, err := os.ReadFile(filepath.Join(turn, "f.txt"))
	require.NoError(t, err)
	assert.Equal(t, "bye turn", string(got), "the edit did not land in the turn's directory")

	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	require.NoError(t, os.WriteFile(filepath.Join(turn, "pic.png"), png, 0o644))
	res, err = NewVisionTool(built).Execute(ctx, map[string]interface{}{"image_path": "pic.png"})
	require.NoError(t, err)
	assert.True(t, res.Success, "the image in the turn's directory was not read: %+v", res.Error)

	require.NoError(t, os.WriteFile(filepath.Join(turn, "t.csv"), []byte("a,b\n1,2\n"), 0o644))
	res, err = NewDocumentParseTool(built).Execute(ctx, map[string]interface{}{"file_path": "t.csv"})
	require.NoError(t, err)
	assert.True(t, res.Success, "the document in the turn's directory was not parsed: %+v", res.Error)
}

// A relative turn directory is made absolute once, against the process's
// working directory, so every tool reads the same place.
func TestARelativeTurnDirectoryIsMadeAbsolute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pwd")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("LOOM_SANDBOX_DIR", root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proj"), 0o755))
	t.Chdir(root)
	ctx := shuttle.ContextWithWorkDir(context.Background(), "proj")
	assert.Equal(t, filepath.Join(root, "proj"), shuttle.WorkDirFromContext(ctx))
	res, err := NewShellExecuteTool(root).Execute(ctx, map[string]interface{}{"command": "pwd -P"})
	require.NoError(t, err)
	require.True(t, res.Success, "a relative turn directory was refused: %+v", res.Error)
	assert.Equal(t, filepath.Join(root, "proj"), strings.TrimSpace(res.Data.(map[string]interface{})["stdout"].(string)))
}
