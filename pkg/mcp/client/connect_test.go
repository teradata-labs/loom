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
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// fakeRevisionTransport answers requests according to the protocol revision
// it simulates: a 2026-07-28 server implements server/discover and must never
// see initialize; a legacy server answers MethodNotFound to the discover
// probe and handles the handshake.
type fakeRevisionTransport struct {
	statelessServer  bool
	responses        chan []byte
	lastToolsCall    json.RawMessage
	lastDiscoverCall json.RawMessage
}

func newFakeRevisionTransport(stateless bool) *fakeRevisionTransport {
	return &fakeRevisionTransport{statelessServer: stateless, responses: make(chan []byte, 10)}
}

func (f *fakeRevisionTransport) Send(ctx context.Context, message []byte) error {
	var req protocol.Request
	if err := json.Unmarshal(message, &req); err != nil {
		return err
	}
	if req.ID == nil {
		return nil // notifications/initialized
	}
	var resp protocol.Response
	resp.JSONRPC = protocol.JSONRPCVersion
	resp.ID = req.ID

	switch req.Method {
	case "server/discover":
		f.lastDiscoverCall = req.Params
		if !f.statelessServer {
			resp.Error = protocol.NewError(protocol.MethodNotFound, "method not found", nil)
			break
		}
		result := protocol.DiscoverResult{
			ResultType:        protocol.ResultTypeComplete,
			SupportedVersions: []string{protocol.Version20260728, protocol.Version20251125},
			CacheScope:        "public",
			Instructions:      "fake stateless usage guidance",
		}
		if err := result.SetServerInfo(protocol.Implementation{Name: "fake", Version: "1.0"}); err != nil {
			return err
		}
		resp.Result, _ = json.Marshal(result)
	case "initialize":
		if f.statelessServer {
			return fmt.Errorf("stateless server must not receive initialize")
		}
		result := protocol.InitializeResult{
			ProtocolVersion: protocol.Version20241105,
			ServerInfo:      protocol.Implementation{Name: "fake-legacy", Version: "1.0"},
			Instructions:    "fake-legacy usage guidance",
		}
		resp.Result, _ = json.Marshal(result)
	case "tools/list":
		f.lastToolsCall = req.Params
		resp.Result, _ = json.Marshal(protocol.ToolListResult{})
	default:
		resp.Error = protocol.NewError(protocol.MethodNotFound, "method not found", nil)
	}

	data, _ := json.Marshal(resp)
	f.responses <- data
	return nil
}

func (f *fakeRevisionTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case data := <-f.responses:
		return data, nil
	}
}

func (f *fakeRevisionTransport) Close() error { return nil }

func TestConnectStatelessNegotiation(t *testing.T) {
	ft := newFakeRevisionTransport(true)
	c := NewClient(Config{Transport: ft})
	defer func() { _ = c.Close() }()

	if err := c.Connect(context.Background(), protocol.Implementation{Name: "loom", Version: "1.4.0"}); err != nil {
		t.Fatal(err)
	}
	if !c.IsStateless() {
		t.Fatal("expected stateless mode")
	}
	if c.ServerInfo().Name != "fake" {
		t.Fatalf("serverInfo not recorded: %+v", c.ServerInfo())
	}
	if c.Instructions() != "fake stateless usage guidance" {
		t.Fatalf("instructions not recorded from discover: %q (issue #336)", c.Instructions())
	}

	// The discover probe itself must carry the standard _meta identity keys —
	// the request "carries no body parameters beyond the standard _meta", and
	// a conforming server rejects a probe whose body lacks the protocolVersion
	// that the MCP-Protocol-Version header names (HeaderMismatch).
	var probeParams struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(ft.lastDiscoverCall, &probeParams); err != nil || probeParams.Meta == nil {
		t.Fatalf("discover probe params missing _meta: %s", ft.lastDiscoverCall)
	}
	var probeVersion string
	if err := json.Unmarshal(probeParams.Meta[protocol.MetaProtocolVersion], &probeVersion); err != nil || probeVersion != protocol.Version20260728 {
		t.Fatalf("discover probe not stamped with preferred version: %s", ft.lastDiscoverCall)
	}
	if _, ok := probeParams.Meta[protocol.MetaClientInfo]; !ok {
		t.Fatalf("discover probe missing clientInfo in _meta: %s", ft.lastDiscoverCall)
	}
	if _, ok := probeParams.Meta[protocol.MetaClientCapabilities]; !ok {
		t.Fatalf("discover probe missing clientCapabilities in _meta: %s", ft.lastDiscoverCall)
	}

	// A follow-up request must carry the stamped _meta identity keys.
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(ft.lastToolsCall, &params); err != nil {
		t.Fatalf("tools/list params not an object: %s", ft.lastToolsCall)
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(params["_meta"], &meta); err != nil {
		t.Fatalf("_meta missing on tools/list: %s", ft.lastToolsCall)
	}
	var version string
	if err := json.Unmarshal(meta[protocol.MetaProtocolVersion], &version); err != nil || version != protocol.Version20260728 {
		t.Fatalf("wrong stamped version: %s", meta[protocol.MetaProtocolVersion])
	}
	var info protocol.Implementation
	if err := json.Unmarshal(meta[protocol.MetaClientInfo], &info); err != nil || info.Name != "loom" {
		t.Fatalf("wrong stamped clientInfo: %s", meta[protocol.MetaClientInfo])
	}
}

func TestConnectFallsBackToInitialize(t *testing.T) {
	ft := newFakeRevisionTransport(false)
	c := NewClient(Config{Transport: ft})
	defer func() { _ = c.Close() }()

	if err := c.Connect(context.Background(), protocol.Implementation{Name: "loom", Version: "1.4.0"}); err != nil {
		t.Fatal(err)
	}
	if c.IsStateless() {
		t.Fatal("expected legacy mode")
	}
	if !c.IsInitialized() {
		t.Fatal("expected initialized after fallback")
	}
	if c.ServerInfo().Name != "fake-legacy" {
		t.Fatalf("serverInfo not recorded: %+v", c.ServerInfo())
	}
	if c.Instructions() != "fake-legacy usage guidance" {
		t.Fatalf("instructions not recorded from initialize: %q (issue #336)", c.Instructions())
	}
}
