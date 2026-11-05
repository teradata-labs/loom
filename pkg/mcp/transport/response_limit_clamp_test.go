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

package transport

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// A caller asking for "no limit" with math.MaxInt64 must get a working
// transport. Unclamped, the +1 on the JSON read overflows to negative,
// io.LimitReader returns EOF at once, the empty body is taken as a
// notification acknowledgment, and Send succeeds while Receive waits for a
// reply that never arrives.
func TestStreamableHTTP_MaxInt64LimitRoundTripsJSON(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"ok": true}})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{
		Endpoint:         srv.URL,
		MaxResponseBytes: math.MaxInt64,
		Logger:           zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); err != nil {
		t.Fatalf("send: %v", err)
	}

	rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, rerr := tr.Receive(rctx)
	if rerr != nil {
		t.Fatalf("a MaxInt64 limit must deliver the response, not hang: %v", rerr)
	}
	if len(got) != len(payload) {
		t.Fatalf("response corrupted: got %d bytes, want %d", len(got), len(payload))
	}
}

// The SSE path overflows the same way, and its symptom is an immediate io.EOF
// from ParseEvent rather than a delivered message.
func TestStreamableHTTP_MaxInt64LimitRoundTripsSSE(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: " + body + "\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{
		Endpoint:         srv.URL,
		MaxResponseBytes: math.MaxInt64,
		Logger:           zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); err != nil {
		t.Fatalf("send: %v", err)
	}

	rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, rerr := tr.Receive(rctx)
	if rerr != nil {
		t.Fatalf("a MaxInt64 limit must deliver the SSE message, not hang: %v", rerr)
	}
	if string(got) != body {
		t.Fatalf("message corrupted: got %q", string(got))
	}
}

// An unbounded parser must stay unbounded: NewSSEParser is used to re-parse an
// already-bounded buffer, so the clamp must not introduce a bound there.
func TestSSEParser_UnboundedStaysUnbounded(t *testing.T) {
	big := make([]byte, 0, 1<<20)
	for i := 0; i < 1<<20; i++ {
		big = append(big, 'x')
	}
	p := NewSSEParser(strings.NewReader("data: " + string(big) + "\n\n"))
	ev, err := p.ParseEvent()
	if err != nil {
		t.Fatalf("an unbounded parser must accept any size, got %v", err)
	}
	if len(ev.Data) != 1<<20 {
		t.Fatalf("want %d bytes, got %d", 1<<20, len(ev.Data))
	}
}

// A refusal must leave a record at default log levels. The caller logs it only
// at Debug, so without this Warn there is nothing tying a refusal to the
// server and the limit it exceeded.
func TestStreamableHTTP_JSONRefusalLogsAtWarn(t *testing.T) {
	const limit = 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		payload, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"blob": strings.Repeat("x", limit*4)},
		})
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	core, logs := observer.New(zapcore.WarnLevel)

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{
		Endpoint:         srv.URL,
		MaxResponseBytes: limit,
		Logger:           zap.New(core),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if sendErr := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"resources/read"}`)); sendErr == nil {
		t.Fatal("an oversized JSON response must fail")
	}

	entries := logs.FilterMessageSnippet("over the configured size limit").All()
	if len(entries) == 0 {
		t.Fatalf("a JSON-path refusal must log at Warn; got %d Warn+ entries overall", logs.Len())
	}
	var sawLimit bool
	for _, f := range entries[0].Context {
		if f.Key == "limit_bytes" && f.Integer == limit {
			sawLimit = true
		}
	}
	if !sawLimit {
		t.Fatalf("the Warn must carry limit_bytes=%d, got fields %v", limit, entries[0].Context)
	}
}

// A code loom synthesizes itself must not be mistakable for one the peer
// sent. MCP 2026-07-28 ("Error Codes") closes both halves of the JSON-RPC
// server-error range to a code like this and says local errors SHOULD be
// allocated outside -32768..-32000 entirely.
//
// The table below is the whole of what the specification and the SDK allocate.
// An earlier version of this test checked the SDK enum ONLY, which is why it
// passed while the code sat on -32002 — the specification's "Resource not
// found". Checking one source and calling it settled is the defect this test
// now exists to prevent, so the spec rows come first.
func TestCodeResponseTooLarge_DoesNotCollideWithStandardCodes(t *testing.T) {
	// Allocated by the MCP SPECIFICATION. Verified against
	// modelcontextprotocol/modelcontextprotocol: schema/*/schema.ts and
	// docs/specification/*/server/resources.mdx ("Error Handling").
	spec := map[int]string{
		-32002: "Resource not found (2025-11-25 and earlier; reserved, never reused)",
		-32003: "MissingRequiredClientCapability (pre-2026-07-28 numbering)",
		-32004: "UnsupportedProtocolVersion (pre-2026-07-28 numbering)",
		-32020: "HeaderMismatch",
		-32021: "MissingRequiredClientCapability",
		-32022: "UnsupportedProtocolVersion",
		-32042: "URL elicitation required (2025-11-25 only; reserved)",
		-32600: "InvalidRequest",
		-32601: "MethodNotFound",
		-32602: "InvalidParams / resource not found from 2026-07-28",
		-32603: "InternalError",
		-32700: "ParseError",
	}
	// Allocated by the published TypeScript SDK (@modelcontextprotocol/sdk
	// v1.32.2), ErrorCode.
	tsSDK := map[int]string{
		-32000: "ConnectionClosed",
		-32001: "RequestTimeout",
		-32042: "UrlElicitationRequired",
		-32600: "InvalidRequest",
		-32601: "MethodNotFound",
		-32602: "InvalidParams",
		-32603: "InternalError",
		-32700: "ParseError",
	}

	if name, clash := spec[CodeResponseTooLarge]; clash {
		t.Fatalf("CodeResponseTooLarge (%d) collides with the MCP specification's %s", CodeResponseTooLarge, name)
	}
	if name, clash := tsSDK[CodeResponseTooLarge]; clash {
		t.Fatalf("CodeResponseTooLarge (%d) collides with the TypeScript SDK's %s", CodeResponseTooLarge, name)
	}

	// The structural rule, which is what makes this robust against codes the
	// specification has not allocated YET: a locally-synthesized error must
	// sit outside the JSON-RPC reserved range altogether.
	if CodeResponseTooLarge <= -32000 && CodeResponseTooLarge >= -32768 {
		t.Fatalf("CodeResponseTooLarge (%d) is inside the JSON-RPC reserved range -32768..-32000; "+
			"MCP 2026-07-28 says a locally-synthesized error SHOULD be allocated outside it", CodeResponseTooLarge)
	}
	// And specifically not in the specification's own sub-range, which an
	// implementation MUST NOT emit undefined codes from.
	if CodeResponseTooLarge <= -32020 && CodeResponseTooLarge >= -32099 {
		t.Fatalf("CodeResponseTooLarge (%d) is in -32020..-32099, reserved for the MCP specification", CodeResponseTooLarge)
	}

	// CodeStreamLost stays at -32000: the value predates the allocation
	// policy, is grandfathered, and callers match on it. Pinned so that
	// staying put remains a decision rather than an oversight.
	if CodeStreamLost != -32000 {
		t.Fatalf("CodeStreamLost moved to %d; its -32000 value is deliberate and grandfathered", CodeStreamLost)
	}
}

// On a stream with no request id, a synthesized error carries "id":null, which
// matches no pending request: the client cannot route it and logs "received
// unrecognized message". So the refusal must be logged, not synthesized.
func TestStreamableHTTP_RefusalOnIdlessStreamSynthesizesNothing(t *testing.T) {
	const limit = 512

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// An oversized message, on a stream answering a NOTIFICATION, so the
		// transport has no request id to attribute a refusal to.
		_, _ = w.Write([]byte("data: " + strings.Repeat("x", limit*4) + "\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	core, logs := observer.New(zapcore.WarnLevel)
	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{
		Endpoint:         srv.URL,
		MaxResponseBytes: limit,
		Logger:           zap.New(core),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	// A notification has no id, so requestID yields nil.
	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Nothing must be delivered on the message channel.
	rctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	got, rerr := tr.Receive(rctx)
	if rerr == nil {
		t.Fatalf("a refusal with no request id must not be delivered as a message, got %q", string(got))
	}
	if !errors.Is(rerr, context.DeadlineExceeded) {
		t.Fatalf("want the channel to stay empty (deadline), got %v", rerr)
	}

	// But it must be on the record.
	if logs.FilterMessageSnippet("no request id").Len() == 0 {
		t.Fatalf("the refusal must be logged at Warn; Warn+ entries: %d", logs.Len())
	}
}

// Q3: once the final response has been delivered, no request is waiting for an
// answer any more. A later oversized message on the same POST stream must not
// synthesize a second error for that id — it would match no pending request and
// only produce "received unrecognized message" noise, which is exactly the rule
// the id-less case above already follows.
func TestStreamableHTTP_RefusalAfterTheFinalResponseSynthesizesNothing(t *testing.T) {
	const limit = 512

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush := func() {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		// The final response for the request, within the bound.
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n"))
		flush()
		// Then an oversized message on the SAME stream, after the answer.
		_, _ = w.Write([]byte("data: " + strings.Repeat("x", limit*4) + "\n\n"))
		flush()
	}))
	defer srv.Close()

	core, logs := observer.New(zapcore.WarnLevel)
	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{
		Endpoint:         srv.URL,
		MaxResponseBytes: limit,
		Logger:           zap.New(core),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)); err != nil {
		t.Fatalf("send: %v", err)
	}

	// The real answer must arrive.
	rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	first, rerr := tr.Receive(rctx)
	if rerr != nil {
		t.Fatalf("the final response must be delivered: %v", rerr)
	}
	if !strings.Contains(string(first), `"result"`) {
		t.Fatalf("want the server's result first, got %q", string(first))
	}

	// Nothing more: no second error for an id that is already answered.
	rctx2, cancel2 := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel2()
	extra, rerr2 := tr.Receive(rctx2)
	if rerr2 == nil {
		t.Fatalf("a refusal after the final response must not be synthesized, got %q", string(extra))
	}
	if !errors.Is(rerr2, context.DeadlineExceeded) {
		t.Fatalf("want the channel to stay empty (deadline), got %v", rerr2)
	}

	// It still has to be on the record.
	if logs.FilterMessageSnippet("over the configured size limit").Len() == 0 {
		t.Fatalf("the refusal must be logged at Warn; Warn+ entries: %d", logs.Len())
	}
}
