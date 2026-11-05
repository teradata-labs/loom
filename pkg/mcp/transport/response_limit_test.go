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
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// --- the bound, on both response shapes ---

// TestStreamableHTTP_JSONResponseOverLimitIsRejected: a plain-JSON response
// larger than the bound is rejected, not buffered. io.ReadAll on a body a
// remote server controls is how one oversized response becomes the process's
// memory.
func TestStreamableHTTP_JSONResponseOverLimitIsRejected(t *testing.T) {
	const limit = 4096
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A valid JSON-RPC response that is simply too big.
		payload, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"blob": strings.Repeat("x", limit*4)},
		})
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{
		Endpoint:         srv.URL,
		MaxResponseBytes: limit,
		Logger:           zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	sendErr := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"resources/read"}`))
	if sendErr == nil {
		t.Fatal("an oversized JSON response must fail, not be delivered")
	}
	if !errors.Is(sendErr, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge, got %v", sendErr)
	}
	var tooLarge *ResponseTooLargeError
	if !errors.As(sendErr, &tooLarge) || tooLarge.Limit != limit {
		t.Fatalf("the error must report the limit it exceeded, got %v", sendErr)
	}
}

// TestStreamableHTTP_JSONResponseAtLimitIsAccepted: the limit is a limit, not
// a margin. A response that exactly fits must still be delivered, or the bound
// would be an outage.
func TestStreamableHTTP_JSONResponseAtLimitIsAccepted(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"ok": true}})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{
		Endpoint: srv.URL,
		// Exactly the payload's size.
		MaxResponseBytes: int64(len(payload)),
		Logger:           zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); err != nil {
		t.Fatalf("a response exactly at the limit must be accepted: %v", err)
	}
	rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, rerr := tr.Receive(rctx)
	if rerr != nil {
		t.Fatalf("the message must arrive: %v", rerr)
	}
	if string(got) != string(payload) {
		t.Fatalf("the message must arrive whole and unmodified")
	}
}

// TestStreamableHTTP_SSEMessageOverLimitIsRejected is the path that matters
// most: a streamable-http server answers every method as text/event-stream and
// delivers a whole JSON-RPC message as ONE "data:" line, which the parser
// previously read with an unbounded ReadString.
//
// The caller must be told it was a SIZE refusal, with its own code — a
// stream-lost error would advise re-issuing a request that fails identically
// every time.
func TestStreamableHTTP_SSEMessageOverLimitIsRejected(t *testing.T) {
	const limit = 4096
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		payload, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"blob": strings.Repeat("x", limit*4)},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{
		Endpoint:         srv.URL,
		MaxResponseBytes: limit,
		Logger:           zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"resources/read"}`)); err != nil {
		t.Fatalf("send: %v", err)
	}

	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, rerr := tr.Receive(rctx)
	if rerr != nil {
		t.Fatalf("the caller was left hanging instead of being told the response was too large: %v", rerr)
	}
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(got, &resp); err != nil {
		t.Fatalf("synthesized response must be valid JSON-RPC: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("an oversized SSE message must surface as an error, got %s", got)
	}
	if resp.Error.Code != CodeResponseTooLarge {
		t.Fatalf("want CodeResponseTooLarge (%d) so the caller does not blindly retry, got %d: %s",
			CodeResponseTooLarge, resp.Error.Code, resp.Error.Message)
	}
	if int64(len(got)) > limit*2 {
		t.Fatalf("the oversized payload must not ride along in the error")
	}
}

// TestStreamableHTTP_SSEBoundIsPerMessageNotPerStream is the invariant that
// keeps subscriptions working. A subscriptions/listen stream stays open for its
// lifetime and delivers many messages; a cumulative bound would kill it once
// the total crossed the limit, however small each message was.
func TestStreamableHTTP_SSEBoundIsPerMessageNotPerStream(t *testing.T) {
	const limit = 512
	const messages = 20 // 20 * ~100 bytes is well over the limit in total

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < messages; i++ {
			payload, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/message",
				"params":  map[string]any{"seq": i, "pad": strings.Repeat("p", 60)},
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{
		Endpoint:         srv.URL,
		MaxResponseBytes: limit,
		Logger:           zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen"}`)); err != nil {
		t.Fatalf("send: %v", err)
	}

	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for received := 0; received < messages; received++ {
		got, rerr := tr.Receive(rctx)
		if rerr != nil {
			t.Fatalf("only %d of %d messages arrived; the bound is being applied cumulatively: %v",
				received, messages, rerr)
		}
		var probe struct {
			Error *json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(got, &probe)
		if probe.Error != nil {
			t.Fatalf("message %d of %d was refused: a per-stream bound would break every subscription: %s",
				received+1, messages, got)
		}
	}
}

// --- the injected client ---

// TestStreamableHTTP_InjectedClientIsUsedAsGiven: a caller supplies a client
// when the CONNECTION carries policy the transport cannot know about — a
// dialler pinned to pre-verified addresses, a guard refusing private
// addresses. Rebuilding or rewrapping it here would drop that policy silently,
// which is worse than refusing to accept one at all.
func TestStreamableHTTP_InjectedClientIsUsedAsGiven(t *testing.T) {
	var used atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	// A RoundTripper standing in for a pinned dialler: the only way the
	// request can succeed is if the transport actually used THIS client.
	marker := &markingRoundTripper{used: &used, base: http.DefaultTransport}
	injected := &http.Client{Transport: marker}

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{
		Endpoint:   srv.URL,
		HTTPClient: injected,
		Logger:     zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if tr.client != injected {
		t.Fatal("the injected client must be used as given, not copied or rewrapped")
	}
	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !used.Load() {
		t.Fatal("the request did not go through the injected client's transport")
	}
}

// TestStreamableHTTP_NoInjectedClientKeepsTheExistingBehaviour: the field is
// additive. A config that does not set it must behave exactly as before,
// including inheriting http.DefaultTransport's proxy and TLS settings.
func TestStreamableHTTP_NoInjectedClientKeepsTheExistingBehaviour(t *testing.T) {
	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{
		Endpoint: "http://mcp.example.invalid/mcp",
		Logger:   zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if tr.client == nil {
		t.Fatal("a client must still be built when none is injected")
	}
	if tr.transport == nil {
		t.Fatal("the *http.Transport must still be retained for CloseIdleConnections")
	}
}

// TestStreamableHTTP_DefaultLimitApplies: an unset MaxResponseBytes must mean
// the default bound, never "unbounded" — the whole point is that a caller who
// did not think about it is still protected.
func TestStreamableHTTP_DefaultLimitApplies(t *testing.T) {
	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{
		Endpoint: "http://mcp.example.invalid/mcp",
		Logger:   zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if tr.maxResponseBytes != DefaultMaxResponseBytes {
		t.Fatalf("want the default bound %d, got %d", DefaultMaxResponseBytes, tr.maxResponseBytes)
	}
}

type markingRoundTripper struct {
	used *atomic.Bool
	base http.RoundTripper
}

func (m *markingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	m.used.Store(true)
	return m.base.RoundTrip(r)
}

// --- the parser in isolation ---

func TestSSEParser_LimitRejectsAnOversizedEvent(t *testing.T) {
	const limit = 256
	stream := "data: " + strings.Repeat("x", limit*3) + "\n\n"

	p := NewSSEParserWithLimit(strings.NewReader(stream), limit)
	_, err := p.ParseEvent()
	if err == nil {
		t.Fatal("an event over the limit must fail")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge, got %v", err)
	}
}

func TestSSEParser_LimitAdmitsEventsThatFit(t *testing.T) {
	const limit = 256
	want := `{"jsonrpc":"2.0","id":1,"result":{}}`
	stream := "id: 7\ndata: " + want + "\n\n"

	p := NewSSEParserWithLimit(strings.NewReader(stream), limit)
	ev, err := p.ParseEvent()
	if err != nil {
		t.Fatalf("an event within the limit must parse: %v", err)
	}
	if string(ev.Data) != want {
		t.Fatalf("data must survive intact\n got: %s\nwant: %s", ev.Data, want)
	}
	if ev.ID != "7" {
		t.Fatalf("event id must survive, got %q", ev.ID)
	}
}

// TestSSEParser_LimitIsPerEvent: the counter resets, so a stream of many small
// events is never refused for its total.
func TestSSEParser_LimitIsPerEvent(t *testing.T) {
	const limit = 64
	var b strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, "data: {\"seq\":%d}\n\n", i)
	}

	p := NewSSEParserWithLimit(strings.NewReader(b.String()), limit)
	for i := 0; i < 10; i++ {
		if _, err := p.ParseEvent(); err != nil {
			t.Fatalf("event %d must parse; the bound is per event, not cumulative: %v", i, err)
		}
	}
}

// TestSSEParser_MultiLineDataCountsTowardOneEvent: the spec allows an event's
// data to arrive over several "data:" lines, so the bound has to be on their
// total and not on the longest one.
func TestSSEParser_MultiLineDataCountsTowardOneEvent(t *testing.T) {
	const limit = 100
	chunk := strings.Repeat("y", 40)
	stream := "data: " + chunk + "\ndata: " + chunk + "\ndata: " + chunk + "\n\n"

	p := NewSSEParserWithLimit(strings.NewReader(stream), limit)
	_, err := p.ParseEvent()
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("three 40-byte data lines exceed a 100-byte event bound; got %v", err)
	}
}

// TestSSEParser_UnboundedConstructorStillWorks keeps the existing API honest:
// NewSSEParser is used to re-parse an already-bounded buffer internally and by
// callers that bound the reader themselves.
func TestSSEParser_UnboundedConstructorStillWorks(t *testing.T) {
	big := strings.Repeat("z", 1<<20)
	p := NewSSEParser(strings.NewReader("data: " + big + "\n\n"))
	ev, err := p.ParseEvent()
	if err != nil {
		t.Fatalf("the unbounded parser must not impose a limit: %v", err)
	}
	if len(ev.Data) != len(big) {
		t.Fatalf("want %d bytes, got %d", len(big), len(ev.Data))
	}
}

func TestEffectiveResponseLimit(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want int64
	}{
		{0, DefaultMaxResponseBytes},
		{-1, DefaultMaxResponseBytes},
		{1024, 1024},
		// Clamped: the read paths add to the limit before using it, and at
		// math.MaxInt64 that arithmetic overflows to negative, which hangs
		// the call rather than refusing it.
		{math.MaxInt64, MaxResponseLimit},
		{MaxResponseLimit, MaxResponseLimit},
		{MaxResponseLimit + 1, MaxResponseLimit},
	} {
		if got := effectiveResponseLimit(tc.in); got != tc.want {
			t.Fatalf("effectiveResponseLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestResponseTooLargeError_IsSentinel(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &ResponseTooLargeError{Limit: 99})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatal("a wrapped ResponseTooLargeError must match the sentinel")
	}
	var typed *ResponseTooLargeError
	if !errors.As(err, &typed) || typed.Limit != 99 {
		t.Fatal("the limit must survive wrapping")
	}
}

// TestSSEParser_MultiLineEventIsBoundedInTotal pins that an event split across
// several small data lines cannot exceed the limit in total, though no single
// line is near it. The accumulated data is charged and checked in ParseEvent;
// readLine's shrinking per-line budget is what stops ONE line being buffered
// past the bound.
func TestSSEParser_MultiLineEventIsBoundedInTotal(t *testing.T) {
	const limit = 40
	// Three 15-byte data lines: 45 bytes of data in total, over the limit,
	// though no single line is anywhere near it.
	body := "data:" + strings.Repeat("x", 15) + "\n" +
		"data:" + strings.Repeat("x", 15) + "\n" +
		"data:" + strings.Repeat("x", 15) + "\n\n"

	parser := NewSSEParserWithLimit(strings.NewReader(body), limit)
	_, err := parser.ParseEvent()
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("a multi-line event over the limit in total must be refused, got %v", err)
	}
}

// TestSSEParser_MultiLineEventUnderLimitIsAccepted is the other half: the
// shrinking budget must not refuse an event that genuinely fits.
func TestSSEParser_MultiLineEventUnderLimitIsAccepted(t *testing.T) {
	const limit = 200
	body := "data:" + strings.Repeat("x", 15) + "\n" +
		"data:" + strings.Repeat("y", 15) + "\n\n"

	parser := NewSSEParserWithLimit(strings.NewReader(body), limit)
	event, err := parser.ParseEvent()
	if err != nil {
		t.Fatalf("a multi-line event under the limit must parse: %v", err)
	}
	if got := string(event.Data); got != strings.Repeat("x", 15)+"\n"+strings.Repeat("y", 15) {
		t.Fatalf("data lines must be joined intact, got %q", got)
	}
}
