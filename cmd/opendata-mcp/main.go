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

// Command opendata-mcp is a small stdio MCP server that exposes the OpenData
// (tryopendata.ai) REST API as MCP tools.
//
// WHY THIS EXISTS: OpenData's hosted MCP server (mcp.tryopendata.ai) requires an
// interactive OAuth flow (Clerk; authorization_code only, no client_credentials),
// which a headless `looms` server cannot complete. Its REST API, however, accepts
// a static `od_live_` API key. This shim wraps that REST API and speaks MCP over
// stdio, so Loom connects to it locally (no OAuth) and agents gain OpenData's
// search / SQL / dataset-graph capabilities via the normal MCP path.
//
// Auth: reads OPENDATA_API_KEY from the environment and sends it as
// `Authorization: Bearer <key>` on every REST call. Logs go to STDERR (stdout is
// the MCP transport channel).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/server"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

const (
	defaultBaseURL  = "https://api.tryopendata.ai"
	maxResultBytes  = 12 * 1024 // cap tool output so a huge result set can't blow the LLM context
	requestTimeout  = 60 * time.Second
	serverName      = "opendata-mcp"
	serverVersionID = "0.1.0"
)

func main() {
	// IMPORTANT: log to stderr — stdout carries the MCP JSON-RPC stream.
	logger := newStderrLogger()
	defer func() { _ = logger.Sync() }()

	apiKey := os.Getenv("OPENDATA_API_KEY")
	if apiKey == "" {
		logger.Fatal("OPENDATA_API_KEY is not set; cannot authenticate to OpenData REST API")
	}
	base := os.Getenv("OPENDATA_API_BASE_URL")
	if base == "" {
		base = defaultBaseURL
	}

	p := &provider{
		apiKey: apiKey,
		base:   base,
		hc:     &http.Client{Timeout: requestTimeout},
		logger: logger,
	}

	mcpServer := server.NewMCPServer(serverName, serverVersionID, logger,
		server.WithToolProvider(p),
	)

	ctx := context.Background()
	logger.Info("opendata-mcp stdio server starting", zap.String("base_url", base))
	if err := mcpServer.Serve(ctx, transport.NewStdioServerTransport(os.Stdin, os.Stdout)); err != nil {
		logger.Fatal("serve failed", zap.Error(err))
	}
}

func newStderrLogger() *zap.Logger {
	cfg := zap.NewProductionEncoderConfig()
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	core := zapcore.NewCore(zapcore.NewJSONEncoder(cfg), zapcore.Lock(os.Stderr), zap.InfoLevel)
	return zap.New(core)
}

// provider implements server.ToolProvider over the OpenData REST API.
type provider struct {
	apiKey string
	base   string
	hc     *http.Client
	logger *zap.Logger
}

func (p *provider) ListTools(_ context.Context) ([]protocol.Tool, error) {
	str := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}
	integer := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "integer", "description": desc}
	}
	obj := func(props map[string]interface{}, required ...string) map[string]interface{} {
		m := map[string]interface{}{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}

	return []protocol.Tool{
		{
			Name:        "search",
			Description: "Search the OpenData catalog of public datasets by keyword. Returns matching datasets with their provider/dataset path and description. Use this first to find data (e.g. 'unemployment', 'CO2 emissions', 'housing prices').",
			InputSchema: obj(map[string]interface{}{
				"q":     str("Search keywords, e.g. 'US unemployment'"),
				"limit": integer("Max results (default 10)"),
			}, "q"),
		},
		{
			Name:        "list_providers",
			Description: "List OpenData data providers (e.g. FRED, BLS, Census, World Bank). Useful to discover authoritative sources.",
			InputSchema: obj(map[string]interface{}{}),
		},
		{
			Name:        "get_dataset",
			Description: "Get a dataset's metadata (title, description, schema summary) by provider/dataset path. Call before querying so you know what the dataset contains.",
			InputSchema: obj(map[string]interface{}{
				"provider": str("Provider slug, e.g. 'fred'"),
				"dataset":  str("Dataset slug, e.g. 'unemployment'"),
			}, "provider", "dataset"),
		},
		{
			Name:        "list_columns",
			Description: "List a dataset's columns (name + type). Call this before writing SQL so you reference real column names.",
			InputSchema: obj(map[string]interface{}{
				"provider": str("Provider slug"),
				"dataset":  str("Dataset slug"),
			}, "provider", "dataset"),
		},
		{
			Name:        "query",
			Description: "Run a DuckDB SQL query across one or more OpenData datasets and return rows. Reference a dataset in FROM as \"provider/dataset\" (quoted). This is the main analysis tool. Example: SELECT date, value FROM \"fred/unemployment\" ORDER BY date DESC LIMIT 12.",
			InputSchema: obj(map[string]interface{}{
				"sql":       str("DuckDB SQL. Tables are \"provider/dataset\" paths (quoted)."),
				"row_limit": integer("Max rows to return (default 200)"),
			}, "sql"),
		},
		{
			Name:        "query_dataset",
			Description: "Run a DuckDB SQL query scoped to a single dataset (the dataset is the implicit table named 'data'). Use 'query' for cross-dataset joins.",
			InputSchema: obj(map[string]interface{}{
				"provider":  str("Provider slug"),
				"dataset":   str("Dataset slug"),
				"sql":       str("DuckDB SQL; the dataset is available as the table 'data'."),
				"row_limit": integer("Max rows to return (default 200)"),
			}, "provider", "dataset", "sql"),
		},
		{
			Name:        "related_datasets",
			Description: "Find datasets related to a given one via OpenData's dataset graph (shared entities / join paths). Use to discover datasets you can join or compare against.",
			InputSchema: obj(map[string]interface{}{
				"provider": str("Provider slug"),
				"dataset":  str("Dataset slug"),
				"limit":    integer("Max related datasets (default 10)"),
			}, "provider", "dataset"),
		},
	}, nil
}

func (p *provider) CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	switch name {
	case "search":
		q := url.Values{}
		q.Set("q", argStr(args, "q"))
		q.Set("limit", strconv.Itoa(argInt(args, "limit", 10)))
		return p.getJSON(ctx, "/v1/search", q)
	case "list_providers":
		return p.getJSON(ctx, "/v1/providers", nil)
	case "get_dataset":
		return p.getJSON(ctx, fmt.Sprintf("/v1/datasets/%s/%s/meta", esc(argStr(args, "provider")), esc(argStr(args, "dataset"))), nil)
	case "list_columns":
		return p.getJSON(ctx, fmt.Sprintf("/v1/datasets/%s/%s/columns", esc(argStr(args, "provider")), esc(argStr(args, "dataset"))), nil)
	case "query":
		body := map[string]interface{}{"sql": argStr(args, "sql"), "row_limit": argInt(args, "row_limit", 200)}
		return p.postJSON(ctx, "/v1/query", body)
	case "query_dataset":
		path := fmt.Sprintf("/v1/datasets/%s/%s/query", esc(argStr(args, "provider")), esc(argStr(args, "dataset")))
		body := map[string]interface{}{"sql": argStr(args, "sql"), "row_limit": argInt(args, "row_limit", 200)}
		return p.postJSON(ctx, path, body)
	case "related_datasets":
		q := url.Values{}
		q.Set("limit", strconv.Itoa(argInt(args, "limit", 10)))
		return p.getJSON(ctx, fmt.Sprintf("/v1/graph/datasets/%s/%s/related", esc(argStr(args, "provider")), esc(argStr(args, "dataset"))), q)
	default:
		return errResult(fmt.Sprintf("unknown tool: %s", name)), nil
	}
}

func (p *provider) getJSON(ctx context.Context, path string, query url.Values) (*protocol.CallToolResult, error) {
	u := p.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return errResult("build request: " + err.Error()), nil
	}
	return p.do(req)
}

func (p *provider) postJSON(ctx context.Context, path string, body map[string]interface{}) (*protocol.CallToolResult, error) {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+path, bytes.NewReader(data))
	if err != nil {
		return errResult("build request: " + err.Error()), nil
	}
	req.Header.Set("Content-Type", "application/json")
	return p.do(req)
}

func (p *provider) do(req *http.Request) (*protocol.CallToolResult, error) {
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := p.hc.Do(req)
	if err != nil {
		return errResult("request failed: " + err.Error()), nil
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResultBytes+1))
	out := string(raw)
	truncated := false
	if len(out) > maxResultBytes {
		out = out[:maxResultBytes]
		truncated = true
	}
	if resp.StatusCode >= 400 {
		return errResult(fmt.Sprintf("OpenData returned HTTP %d: %s", resp.StatusCode, out)), nil
	}
	if truncated {
		out += "\n\n[truncated: result exceeded " + strconv.Itoa(maxResultBytes) + " bytes; add a tighter LIMIT or narrower SELECT]"
	}
	return &protocol.CallToolResult{Content: []protocol.Content{{Type: "text", Text: out}}}, nil
}

func errResult(msg string) *protocol.CallToolResult {
	return &protocol.CallToolResult{IsError: true, Content: []protocol.Content{{Type: "text", Text: msg}}}
}

func esc(s string) string { return url.PathEscape(s) }

func argStr(args map[string]interface{}, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func argInt(args map[string]interface{}, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
