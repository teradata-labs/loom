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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// scriptedHTTPServer is a minimal wire-level MCP server for probing: statelessOK
// selects the 2026-07-28 era (discover answered) vs a legacy 2025 era
// (discover rejected, initialize handshake served). elicitFirst makes the
// first tools/call answer input_required so the probe's MRTR driver runs.
type scriptedHTTPServer struct {
	t           *testing.T
	statelessOK bool
	elicitFirst bool

	mu         sync.Mutex
	callParams []json.RawMessage // raw params of each tools/call attempt
}

func (s *scriptedHTTPServer) handler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	require.NoError(s.t, err)
	var req protocol.Request
	require.NoError(s.t, json.Unmarshal(body, &req))

	if req.ID == nil { // notification (e.g. notifications/initialized)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	writeResult := func(result interface{}) {
		raw, err := json.Marshal(result)
		require.NoError(s.t, err)
		resp, err := json.Marshal(protocol.Response{JSONRPC: protocol.JSONRPCVersion, ID: req.ID, Result: raw})
		require.NoError(s.t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}
	writeError := func(code int, msg string) {
		resp, err := json.Marshal(protocol.Response{
			JSONRPC: protocol.JSONRPCVersion, ID: req.ID,
			Error: protocol.NewError(code, msg, nil),
		})
		require.NoError(s.t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}

	switch req.Method {
	case "server/discover":
		if !s.statelessOK {
			writeError(protocol.MethodNotFound, "method not found")
			return
		}
		res := protocol.DiscoverResult{
			ResultType:        protocol.ResultTypeComplete,
			SupportedVersions: []string{protocol.Version20260728},
			Capabilities:      protocol.ServerCapabilities{Tools: &protocol.ToolsCapability{}},
			TTLMs:             0,
			CacheScope:        "private",
		}
		require.NoError(s.t, res.SetServerInfo(protocol.Implementation{Name: "scripted-http", Version: "1.0"}))
		writeResult(res)
	case "initialize":
		writeResult(protocol.InitializeResult{
			ProtocolVersion: protocol.Version20251125,
			ServerInfo:      protocol.Implementation{Name: "scripted-legacy", Version: "1.0"},
		})
	case "tools/list":
		writeResult(map[string]interface{}{
			"resultType": protocol.ResultTypeComplete,
			"tools": []protocol.Tool{{
				Name:        "greet",
				Description: "greets",
				InputSchema: map[string]interface{}{"type": "object"},
			}},
		})
	case "tools/call":
		s.mu.Lock()
		s.callParams = append(s.callParams, append(json.RawMessage(nil), req.Params...))
		attempt := len(s.callParams)
		s.mu.Unlock()
		if s.elicitFirst && attempt == 1 {
			writeResult(map[string]interface{}{
				"resultType": protocol.ResultTypeInputRequired,
				"inputRequests": map[string]interface{}{
					"who": map[string]interface{}{
						"method": "elicitation/create",
						"params": map[string]interface{}{
							"mode":    "form",
							"message": "who should be greeted?",
						},
					},
				},
				"requestState": "sealed-round-1",
			})
			return
		}
		writeResult(map[string]interface{}{
			"resultType": protocol.ResultTypeComplete,
			"content":    []map[string]interface{}{{"type": "text", "text": "hello from scripted server"}},
		})
	default:
		writeError(protocol.MethodNotFound, "method not found")
	}
}

func startScripted(t *testing.T, s *scriptedHTTPServer) *httptest.Server {
	t.Helper()
	s.t = t
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeStatelessServer(t *testing.T) {
	srv := startScripted(t, &scriptedHTTPServer{statelessOK: true})

	var out strings.Builder
	rep, err := run(context.Background(), options{
		URL: srv.URL, Call: "greet", Args: "{}", Timeout: 5 * time.Second,
	}, &out)
	require.NoError(t, err)

	assert.Equal(t, protocol.Version20260728, rep.Negotiated)
	assert.True(t, rep.Stateless)
	assert.Equal(t, "scripted-http", rep.ServerInfo.Name)
	assert.Equal(t, 1, rep.ToolCount)
	assert.Equal(t, "hello from scripted server", rep.CallOutput)
	assert.Contains(t, out.String(), "stateless (2026-07-28 core)")
}

func TestProbeLegacyFallback(t *testing.T) {
	srv := startScripted(t, &scriptedHTTPServer{statelessOK: false})

	var out strings.Builder
	rep, err := run(context.Background(), options{
		URL: srv.URL, Timeout: 5 * time.Second,
	}, &out)
	require.NoError(t, err)

	assert.Equal(t, protocol.Version20251125, rep.Negotiated)
	assert.False(t, rep.Stateless)
	assert.Equal(t, "scripted-legacy", rep.ServerInfo.Name)
	assert.Contains(t, out.String(), "legacy (initialize handshake)")
}

// TestProbeMRTRElicitation drives the full loop over the wire: the first
// tools/call answers input_required, the probe's handler accepts the
// elicitation with -answer, and the retry must carry both the response and
// the exact requestState.
func TestProbeMRTRElicitation(t *testing.T) {
	s := &scriptedHTTPServer{statelessOK: true, elicitFirst: true}
	srv := startScripted(t, s)

	var out strings.Builder
	rep, err := run(context.Background(), options{
		URL: srv.URL, Call: "greet", Args: "{}",
		Answer: `{"name":"loom"}`, Timeout: 5 * time.Second,
	}, &out)
	require.NoError(t, err)

	require.Equal(t, []string{"who should be greeted?"}, rep.Elicited)
	assert.Equal(t, "hello from scripted server", rep.CallOutput)

	s.mu.Lock()
	defer s.mu.Unlock()
	require.Len(t, s.callParams, 2, "input_required must drive exactly one retry")
	var retry struct {
		InputResponses map[string]struct {
			Action  string                 `json:"action"`
			Content map[string]interface{} `json:"content"`
		} `json:"inputResponses"`
		RequestState *string `json:"requestState"`
	}
	require.NoError(t, json.Unmarshal(s.callParams[1], &retry))
	require.Contains(t, retry.InputResponses, "who")
	assert.Equal(t, "accept", retry.InputResponses["who"].Action)
	assert.Equal(t, "loom", retry.InputResponses["who"].Content["name"])
	require.NotNil(t, retry.RequestState, "requestState must be echoed on the retry")
	assert.Equal(t, "sealed-round-1", *retry.RequestState)
}

func TestProbeRejectsNonElicitationInputRequests(t *testing.T) {
	_, err := buildMRTRHandler(`{"x":1}`, &report{}, printer{w: io.Discard})
	require.NoError(t, err)

	h, err := buildMRTRHandler(`{"x":1}`, &report{}, printer{w: io.Discard})
	require.NoError(t, err)
	_, err = h(context.Background(), protocol.InputRequests{
		"llm": protocol.InputRequest{Method: "sampling/createMessage"},
	})
	require.Error(t, err, "sampling must not be fabricated by the probe")
}

func TestProbeBadAnswerJSON(t *testing.T) {
	_, err := buildMRTRHandler(`not-json`, &report{}, printer{w: io.Discard})
	require.Error(t, err)
}

func TestHelpers(t *testing.T) {
	assert.Equal(t, "stateless (2026-07-28 core)", era(true))
	assert.Equal(t, "legacy (initialize handshake)", era(false))
	assert.Equal(t, ", …", more(10, 6))
	assert.Equal(t, "", more(3, 6))
	assert.Equal(t, "graceful (server closed the subscription)", endState(nil))
}
