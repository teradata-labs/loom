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
// Tests for CacheableResult stamping and deterministic ordering (2026-07-28,
// SEP-2549 / Phase 3).
package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// unorderedToolProvider returns tools in authored (non-alphabetical) order.
type unorderedToolProvider struct{}

func (p *unorderedToolProvider) ListTools(_ context.Context) ([]protocol.Tool, error) {
	return []protocol.Tool{
		{Name: "zeta_tool", Description: "z", InputSchema: map[string]interface{}{"type": "object"}},
		{Name: "alpha_tool", Description: "a", InputSchema: map[string]interface{}{"type": "object"}},
		{Name: "mid_tool", Description: "m", InputSchema: map[string]interface{}{"type": "object"}},
	}, nil
}

func (p *unorderedToolProvider) CallTool(_ context.Context, name string, _ map[string]interface{}) (*protocol.CallToolResult, error) {
	return &protocol.CallToolResult{}, nil
}

func toolsList(t *testing.T, s *MCPServer) ([]byte, protocol.ToolListResult) {
	t.Helper()
	raw, err := s.HandleMessage(context.Background(), legacyReq(1, "tools/list"))
	require.NoError(t, err)
	var resp struct {
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	var result protocol.ToolListResult
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	return resp.Result, result
}

func TestToolsListSortedAndStamped(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil, WithToolProvider(&unorderedToolProvider{}))

	serialized, result := toolsList(t, s)

	require.Len(t, result.Tools, 3)
	assert.Equal(t, "alpha_tool", result.Tools[0].Name)
	assert.Equal(t, "mid_tool", result.Tools[1].Name)
	assert.Equal(t, "zeta_tool", result.Tools[2].Name)
	assert.Equal(t, int64(DefaultListTTLMs), result.TTLMs)
	assert.Equal(t, "private", result.CacheScope, "identity-varying lists must never default to public")

	// Byte-stability: repeated calls serialize identically (the downstream
	// prompt-cache guarantee).
	serialized2, _ := toolsList(t, s)
	assert.Equal(t, string(serialized), string(serialized2))
}

func TestListTTLOverrideAndPublicOptIn(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil,
		WithToolProvider(&unorderedToolProvider{}),
		WithListTTL(5*time.Minute),
		WithPublicCacheScope(),
	)

	_, result := toolsList(t, s)
	assert.Equal(t, int64(300000), result.TTLMs)
	assert.Equal(t, "public", result.CacheScope)
}
