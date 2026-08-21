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
package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
)

// TestNewShuttleMCPManagerSentinelTranslation verifies the shared adapter's
// policy (previously duplicated in cmd/looms and pkg/agent): a server missing
// from configuration surfaces shuttle.ErrMCPServerNotFound so the executor
// evicts the stale index entry, while a server that is configured but not
// connected reports a plain error, keeping the entry for when it comes back.
func TestNewShuttleMCPManagerSentinelTranslation(t *testing.T) {
	mgr, err := manager.NewManager(manager.Config{
		Servers: map[string]manager.ServerConfig{
			// Configured but never started: GetClient fails without the
			// removed-from-configuration sentinel.
			"configured": {Enabled: true, Transport: "stdio", Command: "echo"},
		},
		ClientInfo: manager.ClientInfo{Name: "test", Version: "0.1.0"},
	}, zap.NewNop())
	require.NoError(t, err)

	adapter := NewShuttleMCPManager(mgr)

	// Not configured at all: the stale-index sentinel must surface.
	_, err = adapter.GetClient("removed-from-config")
	require.Error(t, err)
	assert.ErrorIs(t, err, shuttle.ErrMCPServerNotFound,
		"unconfigured server must translate to the eviction sentinel (issue #334)")

	// Configured but not connected: transient — no sentinel, entry kept.
	_, err = adapter.GetClient("configured")
	require.Error(t, err)
	assert.NotErrorIs(t, err, shuttle.ErrMCPServerNotFound,
		"configured-but-disconnected server must not trigger eviction")
}
