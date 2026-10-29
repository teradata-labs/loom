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
package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomconfig "github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// openHITLStore is the CLI's half of the one contract that makes a hold
// answerable: it must open the SAME store `looms serve` writes. These tests
// pin the branch selection and the SQLite path agreement; the postgres
// store's behavior is pinned in pkg/storage/postgres.

func TestOpenHITLStore_SQLiteBranch(t *testing.T) {
	origConfig, origPath := config, hitlCLIDBPath
	t.Cleanup(func() { config, hitlCLIDBPath = origConfig, origPath })

	config = nil // no config file: the default backend is SQLite
	hitlCLIDBPath = filepath.Join(t.TempDir(), "hitl.db")

	store, closeStore, ctx, err := openHITLStore(context.Background(), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer closeStore()

	// The store is real and writable: a round-trip proves the CLI opened a
	// working SQLite store at the flag path.
	req := &shuttle.HumanRequest{
		ID: "r1", AgentID: "a", SessionID: "s", Question: "q",
		RequestType: "approval", Priority: "normal", Status: "pending",
		Kind: "approval", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
	}
	require.NoError(t, store.Store(ctx, req))
	got, err := store.Get(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, "approval", got.Kind)
}

// The serve process derives its SQLite path as GetLoomDataDir()/hitl.db; the
// CLI's --db default must name the SAME file, or the operator reads an empty
// database while the server's holds sit unanswered in another.
func TestHITLSQLitePath_CLIAndServeAgree(t *testing.T) {
	servePath := filepath.Join(loomconfig.GetLoomDataDir(), "hitl.db")
	cliDefault := hitlListCmd.Flags().Lookup("db")
	require.NotNil(t, cliDefault)
	require.Equal(t, servePath, cliDefault.DefValue,
		"the CLI's default --db and serve's hitl.db derivation must name one file")
}

func TestOpenHITLStore_PostgresRequiresUser(t *testing.T) {
	origConfig, origUser := config, hitlUser
	t.Cleanup(func() { config, hitlUser = origConfig, origUser })

	config = &Config{}
	config.Storage.Backend = "postgres"
	hitlUser = ""

	_, _, _, err := openHITLStore(context.Background(), observability.NewNoOpTracer())
	require.Error(t, err,
		"postgres operations are tenant-scoped: the CLI must refuse to open the store with no --user rather than silently seeing nothing")
	require.Contains(t, err.Error(), "--user")
}

// The expire command is the operator's retirement route: it closes a stranded
// pending row terminally and never overwrites a decision. Driven through the
// real command body over a real SQLite store.
func TestRunHitlExpire_RetiresStrandedRow(t *testing.T) {
	origConfig, origPath := config, hitlCLIDBPath
	t.Cleanup(func() { config, hitlCLIDBPath = origConfig, origPath })
	config = nil
	hitlCLIDBPath = filepath.Join(t.TempDir(), "hitl.db")

	// Seed a stranded row: pending, expired, waiter long gone.
	store, err := shuttle.NewSQLiteHumanRequestStore(shuttle.SQLiteConfig{Path: hitlCLIDBPath})
	require.NoError(t, err)
	require.NoError(t, store.Store(context.Background(), &shuttle.HumanRequest{
		ID: "stranded-1", AgentID: "a", SessionID: "s", Question: "q",
		RequestType: "approval", Priority: "normal", Status: "pending",
		Kind: "approval", CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-30 * time.Minute),
	}))
	require.NoError(t, store.Close())

	runHitlExpire(hitlExpireCmd, []string{"stranded-1"})

	after, err := shuttle.NewSQLiteHumanRequestStore(shuttle.SQLiteConfig{Path: hitlCLIDBPath})
	require.NoError(t, err)
	defer func() { _ = after.Close() }()
	got, err := after.Get(context.Background(), "stranded-1")
	require.NoError(t, err)
	require.Equal(t, "timeout", got.Status, "the stranded row is terminally retired")
	require.Contains(t, got.RespondedBy, "operator:", "the closing actor is the operator")
}

func TestHITLListLabel(t *testing.T) {
	assert.Equal(t, `shell_execute command=rm notes.txt`, hitlListLabel(&shuttle.HumanRequest{
		Question: `Approve tool call "shell_execute"?`, Summary: `shell_execute command=rm notes.txt`}),
		"an approval lists its call digest, not the generic question")
	assert.Equal(t, "What is the deadline?", hitlListLabel(&shuttle.HumanRequest{Question: "What is the deadline?"}))
	long := hitlListLabel(&shuttle.HumanRequest{Question: strings.Repeat("é", 100)})
	assert.Equal(t, 70, len([]rune(long)), "cut on a rune boundary")
	assert.True(t, strings.HasSuffix(long, "..."))
}

func TestWriteHITLParams(t *testing.T) {
	var buf bytes.Buffer
	writeHITLParams(&buf, &shuttle.HumanRequest{Params: map[string]interface{}{"command": "rm notes.txt && echo removed", "timeout_seconds": 3}})
	out := buf.String()
	assert.Contains(t, out, "Parameters:")
	assert.Contains(t, out, `"command": "rm notes.txt && echo removed"`)
	assert.Contains(t, out, `"timeout_seconds": 3`)
	assert.NotContains(t, out, "too large")

	buf.Reset()
	writeHITLParams(&buf, &shuttle.HumanRequest{Params: map[string]interface{}{"a": 1}, ParamsTruncated: true})
	assert.Contains(t, buf.String(), "too large for the approval record")

	buf.Reset()
	writeHITLParams(&buf, &shuttle.HumanRequest{})
	assert.Empty(t, buf.String(), "a question carries no parameters")
}

func TestHITLListLabel_ControlCharsStayInOneCell(t *testing.T) {
	label := hitlListLabel(&shuttle.HumanRequest{
		Question: `Approve tool call "shell_execute"?`,
		Summary:  "shell_execute command=echo first\necho second\r\tdone",
	})
	assert.NotContains(t, label, "\n")
	assert.NotContains(t, label, "\r")
	assert.NotContains(t, label, "\t", "a tab would shift the tabwriter columns")
	assert.Equal(t, `shell_execute command=echo first\necho second\r\tdone`, label)
}

// `hitl show` prints the call digest with control characters escaped: the
// digest comes from the model's arguments, and a raw newline or terminal
// escape in it could draw a forged "Parameters:" block above the real one.
// Driven through the real command body over a real SQLite store.
func TestRunHitlShow_CallDigestControlCharsEscaped(t *testing.T) {
	origConfig, origPath := config, hitlCLIDBPath
	t.Cleanup(func() { config, hitlCLIDBPath = origConfig, origPath })
	config = nil
	hitlCLIDBPath = filepath.Join(t.TempDir(), "hitl.db")

	store, err := shuttle.NewSQLiteHumanRequestStore(shuttle.SQLiteConfig{Path: hitlCLIDBPath})
	require.NoError(t, err)
	require.NoError(t, store.Store(context.Background(), &shuttle.HumanRequest{
		ID: "forge-1", AgentID: "a", SessionID: "s",
		Question:    `Approve tool call "shell_execute"?`,
		Summary:     "shell_execute command=ls\nParameters: {\"command\": \"ls\"}\n\x1b[2Jrm -rf /",
		Params:      map[string]interface{}{"command": "ls\nParameters: {\"command\": \"ls\"}\n\x1b[2Jrm -rf /"},
		RequestType: "approval", Priority: "normal", Status: "pending",
		Kind: "approval", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, store.Close())

	out := captureStdout(t, func() { runHitlShow(hitlShowCmd, []string{"forge-1"}) })

	assert.NotContains(t, out, "\x1b", "a terminal escape reaches the screen raw")
	for _, line := range strings.Split(out, "\n") {
		assert.False(t, strings.HasPrefix(line, `Parameters: {"command": "ls"}`),
			"the digest drew a forged Parameters line: %q", line)
	}
	assert.Contains(t, out, `Call:           shell_execute command=ls\nParameters: {"command": "ls"}\n\u001b[2Jrm -rf /`+"\n",
		"the whole digest stays on the Call line, escaped")
	assert.Contains(t, out, "Parameters:\n", "the real parameters block still prints")
}

// `hitl list` keeps every request on one table row: an approval is labelled
// by its call digest with control characters escaped, and a long label is
// cut to 70 characters on a rune boundary. Driven through the real command
// body over a real SQLite store.
func TestRunHitlList_OneRowPerRequestWithEscapedDigest(t *testing.T) {
	origConfig, origPath := config, hitlCLIDBPath
	origSession, origAgent := hitlSessionID, hitlAgentID
	t.Cleanup(func() {
		config, hitlCLIDBPath = origConfig, origPath
		hitlSessionID, hitlAgentID = origSession, origAgent
	})
	config = nil
	hitlSessionID, hitlAgentID = "", ""
	hitlCLIDBPath = filepath.Join(t.TempDir(), "hitl.db")

	store, err := shuttle.NewSQLiteHumanRequestStore(shuttle.SQLiteConfig{Path: hitlCLIDBPath})
	require.NoError(t, err)
	for _, req := range []*shuttle.HumanRequest{
		{
			ID: "approval-1", AgentID: "a", SessionID: "s",
			Question:    `Approve tool call "shell_execute"?`,
			Summary:     "shell_execute command=echo first\necho second\x1b[2J",
			RequestType: "approval", Priority: "normal", Status: "pending", Kind: "approval",
		},
		{
			ID: "question-1", AgentID: "a", SessionID: "s",
			Question:    strings.Repeat("é", 100),
			RequestType: "question", Priority: "normal", Status: "pending", Kind: "question",
		},
	} {
		req.CreatedAt, req.ExpiresAt = time.Now(), time.Now().Add(time.Hour)
		require.NoError(t, store.Store(context.Background(), req))
	}
	require.NoError(t, store.Close())

	out := captureStdout(t, func() { runHitlList(hitlListCmd, nil) })

	assert.NotContains(t, out, "\x1b", "a terminal escape reaches the screen raw")
	rows := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		for _, id := range []string{"approval-1", "question-1"} {
			if strings.HasPrefix(line, id) {
				rows[id] = line
			}
		}
	}
	require.Len(t, rows, 2, "each request is listed:\n%s", out)
	assert.Contains(t, rows["approval-1"], `shell_execute command=echo first\necho second\u001b[2J`,
		"the whole digest stays on the request's row, escaped")
	assert.NotContains(t, out, "\necho second", "the digest's newline split the row")
	label := rows["question-1"][strings.Index(rows["question-1"], "é"):]
	assert.Equal(t, 70, len([]rune(label)), "the label is cut to 70 runes")
	assert.True(t, strings.HasSuffix(label, "..."))
	assert.Contains(t, out, "Total: 2 pending request(s)")
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	fn()
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

func TestWriteHITLParams_WarnsWhenEveryParameterWasDropped(t *testing.T) {
	for name, params := range map[string]map[string]interface{}{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			writeHITLParams(&buf, &shuttle.HumanRequest{Params: params, ParamsTruncated: true})
			assert.Contains(t, buf.String(), "too large for the approval record")
		})
	}
}
