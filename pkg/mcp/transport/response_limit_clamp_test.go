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

// The synthesized code must not collide with a code that means something else
// to a standard client. The published TypeScript SDK uses -32001 for
// RequestTimeout, and a caller reading a size refusal as a timeout would
// retry it, which can only fail the same way again.
func TestCodeResponseTooLarge_DoesNotCollideWithStandardCodes(t *testing.T) {
	// @modelcontextprotocol/sdk v1.32.2, ErrorCode.
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
	if name, clash := tsSDK[CodeResponseTooLarge]; clash {
		t.Fatalf("CodeResponseTooLarge (%d) collides with the TypeScript SDK's %s", CodeResponseTooLarge, name)
	}
	// And it must stay inside the implementation-defined range the doc claims.
	if CodeResponseTooLarge > -32000 || CodeResponseTooLarge < -32019 {
		t.Fatalf("CodeResponseTooLarge (%d) is outside -32000..-32019", CodeResponseTooLarge)
	}
	// CodeStreamLost shares -32000 with ConnectionClosed on purpose: it means
	// the same thing to a caller. Pinned so the overlap stays deliberate.
	if CodeStreamLost != -32000 {
		t.Fatalf("CodeStreamLost moved to %d; the ConnectionClosed overlap was deliberate", CodeStreamLost)
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
