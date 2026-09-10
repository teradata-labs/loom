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
	"fmt"
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
	statelessOK bool
	elicitFirst bool
	watchMode   string // "": listen → MethodNotFound; "no-ack": SSE opens, no ack; "ack-then-close": ack then immediate close
	callIsError bool   // tools/call answers isError:true

	mu         sync.Mutex
	callParams []json.RawMessage // raw params of each tools/call attempt
}

// handler runs on the HTTP server goroutine, where FailNow is illegal — any
// internal problem becomes an HTTP 500 the client-side assertions surface.
func (s *scriptedHTTPServer) handler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var req protocol.Request
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if req.ID == nil { // notification (e.g. notifications/initialized)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	writeResult := func(result interface{}) {
		raw, err := json.Marshal(result)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp, err := json.Marshal(protocol.Response{JSONRPC: protocol.JSONRPCVersion, ID: req.ID, Result: raw})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}
	writeError := func(code int, msg string) {
		resp, err := json.Marshal(protocol.Response{
			JSONRPC: protocol.JSONRPCVersion, ID: req.ID,
			Error: protocol.NewError(code, msg, nil),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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
		if err := res.SetServerInfo(protocol.Implementation{Name: "scripted-http", Version: "1.0"}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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
		if s.callIsError {
			writeResult(map[string]interface{}{
				"resultType": protocol.ResultTypeComplete,
				"isError":    true,
				"content":    []map[string]interface{}{{"type": "text", "text": "tool exploded"}},
			})
			return
		}
		writeResult(map[string]interface{}{
			"resultType": protocol.ResultTypeComplete,
			"content":    []map[string]interface{}{{"type": "text", "text": "hello from scripted server"}},
		})
	case protocol.MethodSubscriptionsListen:
		switch s.watchMode {
		case "stall-headers":
			// Stall before sending ANY response headers: the transport's
			// http.Client has no deadline of its own, so only the probe's
			// pre-ack watchdog can catch this.
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
		case "no-ack":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			// Hold the stream open without ever acknowledging.
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
		case "ack-then-close":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			idJSON, _ := json.Marshal(req.ID)
			ack, _ := json.Marshal(map[string]interface{}{
				"jsonrpc": protocol.JSONRPCVersion,
				"method":  protocol.NotificationSubscriptionAcknowledged,
				"params": map[string]interface{}{
					"_meta": map[string]json.RawMessage{protocol.MetaSubscriptionID: idJSON},
				},
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", ack)
			w.(http.Flusher).Flush()
			// Returning here closes the stream immediately after the ack.
		default:
			writeError(protocol.MethodNotFound, "subscriptions unsupported")
		}
	default:
		writeError(protocol.MethodNotFound, "method not found")
	}
}

func startScripted(t *testing.T, s *scriptedHTTPServer) *httptest.Server {
	t.Helper()
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

// Round-1 blocker: -watch must never exit 0 without a healthy, acknowledged
// subscription held for the full window. Four escape paths, four regressions.
func TestProbeWatchOnLegacyIsError(t *testing.T) {
	srv := startScripted(t, &scriptedHTTPServer{statelessOK: false})
	_, err := run(context.Background(), options{
		URL: srv.URL, WatchSec: 1, Timeout: 5 * time.Second,
	}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy")
}

func TestProbeWatchMethodNotFoundIsError(t *testing.T) {
	srv := startScripted(t, &scriptedHTTPServer{statelessOK: true}) // watchMode "": listen → MethodNotFound
	_, err := run(context.Background(), options{
		URL: srv.URL, WatchSec: 1, Timeout: 5 * time.Second,
	}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subscription")
}

func TestProbeWatchNoAckIsError(t *testing.T) {
	srv := startScripted(t, &scriptedHTTPServer{statelessOK: true, watchMode: "no-ack"})
	_, err := run(context.Background(), options{
		URL: srv.URL, WatchSec: 5, Timeout: 500 * time.Millisecond,
	}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acknowledgment")
}

func TestProbeWatchHeaderStallIsError(t *testing.T) {
	srv := startScripted(t, &scriptedHTTPServer{statelessOK: true, watchMode: "stall-headers"})
	start := time.Now()
	_, err := run(context.Background(), options{
		URL: srv.URL, WatchSec: 5, Timeout: 500 * time.Millisecond,
	}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acknowledgment")
	assert.Less(t, time.Since(start), 3*time.Second,
		"the pre-ack watchdog must fire on -timeout, not wait out the stalled server")
}

func TestProbeWatchEarlyCloseIsError(t *testing.T) {
	srv := startScripted(t, &scriptedHTTPServer{statelessOK: true, watchMode: "ack-then-close"})
	_, err := run(context.Background(), options{
		URL: srv.URL, WatchSec: 3, Timeout: 5 * time.Second,
	}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before the watch window")
}

func TestProbeWhitespaceCmdIsError(t *testing.T) {
	_, err := run(context.Background(), options{Cmd: "   ", Timeout: time.Second}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-cmd")
}

func TestHeadersFromEnv(t *testing.T) {
	t.Setenv("PROBE_H_OK", `{"Authorization":"Bearer fake-test-token"}`)
	h, err := headersFromEnv("PROBE_H_OK")
	require.NoError(t, err)
	assert.Equal(t, "Bearer fake-test-token", h["Authorization"])

	_, err = headersFromEnv("PROBE_H_MISSING")
	require.Error(t, err)

	t.Setenv("PROBE_H_BAD", `not-json`)
	_, err = headersFromEnv("PROBE_H_BAD")
	require.Error(t, err)

	h, err = headersFromEnv("")
	require.NoError(t, err)
	assert.Nil(t, h)
}

// The gate-script pitch: every path that verifies nothing must exit non-zero.
func TestProbeIsErrorResultFailsProbe(t *testing.T) {
	srv := startScripted(t, &scriptedHTTPServer{statelessOK: true, callIsError: true})
	_, err := run(context.Background(), options{
		URL: srv.URL, Call: "greet", Args: "{}", Timeout: 5 * time.Second,
	}, io.Discard)
	require.Error(t, err, "an isError:true tool result must fail the probe")
	assert.Contains(t, err.Error(), "greet")
}

func TestProbeBadArgsJSONFailsBeforeTransport(t *testing.T) {
	_, err := run(context.Background(), options{
		URL: "http://127.0.0.1:1", Call: "greet", Args: "not-json", Timeout: time.Second,
	}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-args")
}

func TestProbeInputRequiredWithoutAnswerFailsFast(t *testing.T) {
	srv := startScripted(t, &scriptedHTTPServer{statelessOK: true, elicitFirst: true})
	_, err := run(context.Background(), options{
		URL: srv.URL, Call: "greet", Args: "{}", Timeout: 5 * time.Second,
	}, io.Discard)
	require.Error(t, err, "input_required with no -answer must fail, not silently pass")
}

func TestProbeAnswerWithoutElicitationIsError(t *testing.T) {
	// Stateless server whose tool completes immediately: the MRTR driver is
	// armed but never runs, so -answer verified nothing and must fail loudly.
	srv := startScripted(t, &scriptedHTTPServer{statelessOK: true})
	_, err := run(context.Background(), options{
		URL: srv.URL, Call: "greet", Args: "{}", Answer: `{"x":1}`, Timeout: 5 * time.Second,
	}, io.Discard)
	require.Error(t, err, "-answer with a call that never elicits must fail, not exit 0")
	assert.Contains(t, err.Error(), "never exercised")
}

func TestProbeAnswerOnLegacyConnectionIsError(t *testing.T) {
	srv := startScripted(t, &scriptedHTTPServer{statelessOK: false})
	_, err := run(context.Background(), options{
		URL: srv.URL, Call: "greet", Args: "{}", Answer: `{"x":1}`, Timeout: 5 * time.Second,
	}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy")
	assert.Contains(t, err.Error(), "MRTR")
}

func TestHelpers(t *testing.T) {
	assert.Equal(t, "stateless (2026-07-28 core)", era(true))
	assert.Equal(t, "legacy (initialize handshake)", era(false))
	assert.Equal(t, ", …", more(10, 6))
	assert.Equal(t, "", more(3, 6))
	assert.Equal(t, "graceful (server closed the subscription)", endState(nil))
}
