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

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// sessionHandleE2ETransport is a scripted stateless (2026-07-28) MCP server
// with one tool ("connect") that declares the session-handle release
// convention and serves scripted tools/call results in order.
type sessionHandleE2ETransport struct {
	mu          sync.Mutex
	callResults []json.RawMessage
	callCount   int
	callArgs    []map[string]interface{}
	responses   chan []byte
}

func newSessionHandleE2ETransport(callResults ...json.RawMessage) *sessionHandleE2ETransport {
	return &sessionHandleE2ETransport{callResults: callResults, responses: make(chan []byte, 16)}
}

func (f *sessionHandleE2ETransport) Send(_ context.Context, message []byte) error {
	var req protocol.Request
	if err := json.Unmarshal(message, &req); err != nil {
		return err
	}
	if req.ID == nil || req.Method == "" {
		return nil // notifications: unanswered
	}

	resp := protocol.Response{JSONRPC: protocol.JSONRPCVersion, ID: req.ID}
	switch req.Method {
	case "server/discover":
		res := &protocol.DiscoverResult{
			ResultType:        protocol.ResultTypeComplete,
			SupportedVersions: []string{protocol.Version20260728},
			CacheScope:        "public",
		}
		if err := res.SetServerInfo(protocol.Implementation{Name: "handlefake", Version: "1"}); err != nil {
			return err
		}
		resp.Result, _ = json.Marshal(res)
	case "tools/list":
		resp.Result, _ = json.Marshal(protocol.ToolListResult{Tools: []protocol.Tool{{
			Name:        "connect",
			Description: "opens a session-scoped connection and mints a session handle",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target":         map[string]interface{}{"type": "string"},
					"release_handle": map[string]interface{}{"type": "string"},
				},
			},
		}}})
	case "tools/call":
		var p struct {
			Arguments map[string]interface{} `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		f.mu.Lock()
		i := f.callCount
		f.callCount++
		f.callArgs = append(f.callArgs, p.Arguments)
		f.mu.Unlock()
		if i >= len(f.callResults) {
			return fmt.Errorf("unscripted tools/call attempt %d", i)
		}
		resp.Result = f.callResults[i]
	default:
		resp.Error = protocol.NewError(protocol.MethodNotFound, "method not found", nil)
	}
	out, _ := json.Marshal(resp)
	f.responses <- out
	return nil
}

func (f *sessionHandleE2ETransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case data := <-f.responses:
		return data, nil
	}
}

func (f *sessionHandleE2ETransport) Close() error { return nil }

func (f *sessionHandleE2ETransport) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

func (f *sessionHandleE2ETransport) args(i int) map[string]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.callArgs) {
		return nil
	}
	return f.callArgs[i]
}

// TestAgentChatAutoReleasesSessionHandle: end-to-end through the agent's own
// wiring (issue #345) — Chat plants the handle collector, the registered MCP
// tool mints a session handle mid-conversation, and by the time Chat returns
// the runtime has released the handle through the minting tool with the
// schema-declared release property. No adapter-level test plumbing: this is
// the production path.
func TestAgentChatAutoReleasesSessionHandle(t *testing.T) {
	mint, _ := json.Marshal(protocol.CallToolResult{Content: []protocol.Content{
		{Type: "text", Text: `{"session_handle":"tdsh_e2e","target":"default"}`},
	}})
	released, _ := json.Marshal(protocol.CallToolResult{Content: []protocol.Content{
		{Type: "text", Text: `{"released":true}`},
	}})
	ft := newSessionHandleE2ETransport(mint, released)

	mcpClient := client.NewClient(client.Config{Transport: ft})
	t.Cleanup(func() { _ = mcpClient.Close() })
	ctx := context.Background()
	require.NoError(t, mcpClient.Connect(ctx, protocol.Implementation{Name: "loom-test", Version: "test"}))
	require.True(t, mcpClient.IsStateless())

	mockLLM := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content:   "",
				toolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "handles:connect", Input: map[string]interface{}{"target": "default"}}},
			},
			{
				content: "Connected.",
			},
		},
	}

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, mockLLM, WithConfig(cfg))

	require.NoError(t, ag.RegisterMCPTools(ctx, MCPServerConfig{Name: "handles", Client: mcpClient}))

	resp, err := ag.Chat(ctx, "session-handle-e2e", "Connect to the default target")
	require.NoError(t, err)
	require.Len(t, resp.ToolExecutions, 1)
	require.True(t, resp.ToolExecutions[0].Result.Success)

	// Chat's deferred ReleaseAll runs synchronously before Chat returns:
	// the release must already be on the wire.
	require.Equal(t, 2, ft.calls(), "mint + auto-release must have reached the server")
	assert.Equal(t, "tdsh_e2e", ft.args(1)["release_handle"],
		"the auto-release must carry the minted handle under the schema-declared property")
}
