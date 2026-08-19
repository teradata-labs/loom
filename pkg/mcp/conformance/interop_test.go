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
//
// SDK interop (migration Phase 8): the official Go SDK runs as the
// counterpart peer in both directions — the cheapest continuous check that
// pkg/mcp tracks the specification rather than our reading of it. Behind the
// interop build tag so the core suite never depends on SDK behavior:
//
//	go test -tags "fts5 interop" ./pkg/mcp/conformance/ -run Interop

//go:build interop

package conformance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomclient "github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	loomserver "github.com/teradata-labs/loom/pkg/mcp/server"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

// interopToolProvider is a minimal Loom-side tool provider.
type interopToolProvider struct{}

func (p *interopToolProvider) ListTools(_ context.Context) ([]protocol.Tool, error) {
	return []protocol.Tool{{
		Name:        "echo",
		Description: "echoes its input",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}},
		},
	}}, nil
}

func (p *interopToolProvider) CallTool(_ context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	text, _ := args["text"].(string)
	return &protocol.CallToolResult{Content: []protocol.Content{{Type: "text", Text: "echo: " + text}}}, nil
}

// TestInteropSDKClientAgainstLoomServer drives the official SDK client
// against Loom's server over Streamable HTTP: negotiation, tools/list
// (including CacheableResult fields surviving SDK decoding), and tools/call.
func TestInteropSDKClientAgainstLoomServer(t *testing.T) {
	mcpServer := loomserver.NewMCPServer("loom-interop", "1.4.0", nil,
		loomserver.WithToolProvider(&interopToolProvider{}))
	httpSrv, err := transport.NewStreamableHTTPServer(transport.StreamableHTTPServerConfig{
		Handler:       mcpServer.HandleMessage,
		StreamHandler: mcpServer,
	})
	require.NoError(t, err)
	ts := httptest.NewServer(httpSrv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sdkClient := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "sdk-interop-client", Version: "1.0.0"}, nil)
	session, err := sdkClient.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint: ts.URL,
		// Loom's server answers the legacy standalone GET stream with 405 as
		// the 2026-07-28 revision prescribes; request/response only.
		DisableStandaloneSSE: true,
	}, nil)
	require.NoError(t, err, "SDK client must connect to Loom's server")
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
	assert.Equal(t, "echo", tools.Tools[0].Name)

	// The CacheableResult freshness fields (2026-07-28, SEP-2549) must
	// survive the official SDK's decoding — this is the assertion the
	// comment above promises.
	assert.Greater(t, tools.GetTTLMs(), 0, "ttlMs must survive SDK decoding")
	assert.Equal(t, "private", tools.GetCacheScope(), "cacheScope must survive SDK decoding")

	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"text": "interop"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)
	tc, ok := result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "echo: interop", tc.Text)
}

// TestInteropLoomClientAgainstSDKServer drives Loom's client against the
// official SDK server in its stateless (2026-07-28, SEP-2567) mode.
func TestInteropLoomClientAgainstSDKServer(t *testing.T) {
	sdkServer := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "sdk-interop-server", Version: "1.0.0"}, nil)
	type echoArgs struct {
		Text string `json:"text"`
	}
	sdkmcp.AddTool(sdkServer, &sdkmcp.Tool{Name: "echo", Description: "echoes its input"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, args echoArgs) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "echo: " + args.Text}}}, nil, nil
		})

	handler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return sdkServer },
		&sdkmcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	trans, err := transport.NewStreamableHTTPTransport(transport.StreamableHTTPConfig{Endpoint: ts.URL})
	require.NoError(t, err)
	c := loomclient.NewClient(loomclient.Config{Transport: trans})
	defer func() { _ = c.Close() }()

	require.NoError(t, c.Connect(ctx, protocol.Implementation{Name: "loom", Version: "1.4.0"}),
		"Loom's client must connect to the SDK's stateless server")
	// Modern negotiation is the point of this suite: assert it, don't log it.
	assert.Equal(t, protocol.Version20260728, c.NegotiatedVersion(),
		"negotiation against the official stateless server must land on 2026-07-28")
	assert.True(t, c.IsStateless())

	tools, err := c.ListTools(ctx)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "echo", tools[0].Name)

	raw, err := c.CallTool(ctx, "echo", map[string]interface{}{"text": "interop"})
	require.NoError(t, err)
	result, ok := raw.(*protocol.CallToolResult)
	require.True(t, ok)
	require.NotEmpty(t, result.Content)
	assert.Equal(t, "echo: interop", result.Content[0].Text)
}
