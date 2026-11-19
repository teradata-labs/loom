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
package mcptest

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPreparedContext_SlowDownload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 45 * time.Second
		var setupCtx context.Context
		ctx, err := preparedContext(t, timeout, func(ctx context.Context) error {
			setupCtx = ctx
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "package preparation must have a deadline")
			require.Equal(t, 2*time.Minute, time.Until(deadline))

			// A cold download can take longer than the entire handshake budget.
			// synctest advances virtual time, so this test does not wait 46s.
			time.Sleep(timeout + time.Second)
			return ctx.Err()
		})
		require.NoError(t, err)

		require.ErrorIs(t, setupCtx.Err(), context.Canceled)
		require.NoError(t, ctx.Err(), "preparation must not consume the protocol budget")
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Equal(t, timeout, time.Until(deadline))

		// The fresh deadline must still bound a stalled handshake.
		time.Sleep(timeout)
		synctest.Wait()
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	})
}

func TestPreparedContext_SetupFailure(t *testing.T) {
	wantErr := errors.New("npm setup failed")
	var setupCtx context.Context
	ctx, err := preparedContext(t, time.Second, func(ctx context.Context) error {
		setupCtx = ctx
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)
	require.Nil(t, ctx, "failed preparation must not start a protocol context")
	require.ErrorIs(t, setupCtx.Err(), context.Canceled)
}

func TestResolveEntry(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found in PATH")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "resolve-entry.cjs")
	require.NoError(t, os.WriteFile(script, resolverScript, 0600))

	fixture := func(prefix, manifest, entryName string) (string, string) {
		t.Helper()
		modules := filepath.Join(dir, prefix, "node_modules")
		binDir := filepath.Join(modules, ".bin")
		packageDir := filepath.Join(modules, Filesystem)
		require.NoError(t, os.MkdirAll(binDir, 0700))
		require.NoError(t, os.MkdirAll(packageDir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(manifest), 0600))
		entry := filepath.Join(packageDir, entryName)
		require.NoError(t, os.WriteFile(entry, nil, 0600))
		return binDir, entry
	}

	cached, cachedEntry := fixture("npm cache with spaces", `{"name":"@modelcontextprotocol/server-filesystem","bin":{"filesystem":"cached.cjs"}}`, "cached.cjs")
	local, localEntry := fixture("project", `{"name":"@modelcontextprotocol/server-filesystem","bin":"local.cjs"}`, "local.cjs")
	for _, tc := range []struct {
		name string
		bins []string
		want string
	}{
		{"selected cache entry wins", []string{cached, local}, cachedEntry},
		{"missing manifest falls back to the next directory", []string{filepath.Join(dir, "missing", "node_modules", ".bin"), local}, localEntry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), node, script, Filesystem)
			cmd.Env = append(os.Environ(), "PATH="+strings.Join(tc.bins, string(os.PathListSeparator)))
			output, err := cmd.Output()
			require.NoError(t, err)
			server, err := parseServer(output)
			require.NoError(t, err)
			want, err := os.Stat(tc.want)
			require.NoError(t, err)
			got, err := os.Stat(server.Entry)
			require.NoError(t, err)
			// Node and Go can spell the same Windows path differently.
			require.True(t, os.SameFile(want, got), "expected entry %q, got %q", tc.want, server.Entry)
		})
	}

	t.Run("invalid entry fails during preparation", func(t *testing.T) {
		bad, _ := fixture("invalid cache", `{"name":"@modelcontextprotocol/server-filesystem","bin":{"filesystem":"missing.cjs"}}`, "other.cjs")
		cmd := exec.CommandContext(t.Context(), node, script, Filesystem)
		cmd.Env = append(os.Environ(), "PATH="+bad)
		output, err := cmd.CombinedOutput()
		require.Error(t, err)
		require.Contains(t, string(output), "missing.cjs")
	})
}
