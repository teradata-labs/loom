// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !windows

package transport

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// A server that ignores its stdin closing is killed once the grace period
// ends. Close must then log how the killed process ended, at the level a
// default install shows.
func TestStdioTransport_CloseLogsExitStatusAfterForceKill(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	tr, err := NewStdioTransport(StdioConfig{
		Command: "/bin/sh",
		Args:    []string{"-c", "exec sleep 60"}, // never reads stdin, so closing it does not end it
		Logger:  zap.New(core),
	})
	require.NoError(t, err)
	tr.closeGrace = 200 * time.Millisecond

	start := time.Now()
	require.NoError(t, tr.Close())
	assert.Less(t, time.Since(start), 30*time.Second, "Close must kill the server, not wait for it")

	require.Len(t, logs.FilterMessage("MCP server did not exit cleanly, killing process").All(), 1)
	status := logs.FilterMessage("MCP server exit status after kill").All()
	require.Len(t, status, 1, "the exit status of the killed server must be logged at Info")
	assert.Equal(t, zap.InfoLevel, status[0].Level)
	logged, ok := status[0].ContextMap()["error"].(string)
	require.True(t, ok, "the entry must carry the wait error")
	assert.Contains(t, logged, "killed")
}

// A server that exits when its stdin closes is not killed, and no kill status
// is logged.
func TestStdioTransport_CloseDoesNotKillAServerThatExits(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	tr, err := NewStdioTransport(StdioConfig{
		Command: "/bin/cat", // exits on EOF
		Logger:  zap.New(core),
	})
	require.NoError(t, err)

	require.NoError(t, tr.Close())
	assert.Len(t, logs.FilterMessage("MCP server exited cleanly").All(), 1)
	assert.Empty(t, logs.FilterMessage("MCP server did not exit cleanly, killing process").All())
	assert.Empty(t, logs.FilterMessage("MCP server exit status after kill").All())
}
