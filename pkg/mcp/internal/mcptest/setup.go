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

// Package mcptest prepares reference servers for MCP tests.
package mcptest

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	Filesystem = "@modelcontextprotocol/server-filesystem"
	Memory     = "@modelcontextprotocol/server-memory"
)

//go:embed resolve-entry.cjs
var resolverScript []byte

// Server is the resolved Node interpreter and server entry file.
type Server struct {
	Command string `json:"command"`
	Entry   string `json:"entry"`
}

// Args returns the entry file followed by the server's own arguments.
func (s Server) Args(args ...string) []string {
	return append([]string{s.Entry}, args...)
}

// PrepareServer completes npm setup before starting the protocol deadline.
// Launch the returned server directly so npm startup stays outside that deadline.
func PrepareServer(t *testing.T, packageName string, timeout time.Duration) (context.Context, Server) {
	t.Helper()
	// Pass a script file, so npm's command shell does not have to quote JS code.
	script := filepath.Join(t.TempDir(), "resolve-entry.cjs")
	require.NoError(t, os.WriteFile(script, resolverScript, 0600))

	var server Server
	ctx, err := preparedContext(t, timeout, func(ctx context.Context) error {
		var err error
		server, err = resolveServer(ctx, packageName, script)
		return err
	})
	require.NoError(t, err, "prepare MCP server %s", packageName)
	return ctx, server
}

func resolveServer(ctx context.Context, packageName, script string) (Server, error) {
	// #nosec G204 -- Callers use fixed reference packages and an embedded test script.
	cmd := exec.CommandContext(ctx, "npx", "--yes", "--package="+packageName, "--", "node", script, packageName)
	cmd.WaitDelay = 5 * time.Second
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return Server{}, errors.Join(ctx.Err(), err)
	}
	return parseServer(output)
}

func parseServer(output []byte) (Server, error) {
	var server Server
	if err := json.Unmarshal(output, &server); err != nil {
		return Server{}, fmt.Errorf("decode npm resolver output: %w", err)
	}
	if !filepath.IsAbs(server.Command) || !filepath.IsAbs(server.Entry) {
		return Server{}, errors.New("npm resolver did not return absolute Node and entry paths")
	}
	return server, nil
}

// preparedContext gives setup its own deadline. Both phases follow the test
// lifetime, and a failed preparation never starts the protocol timer.
func preparedContext(t *testing.T, timeout time.Duration, prepare func(context.Context) error) (context.Context, error) {
	t.Helper()
	setupCtx, cancelSetup := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancelSetup()
	if err := prepare(setupCtx); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	t.Cleanup(cancel)
	return ctx, nil
}
