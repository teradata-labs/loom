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
// Live SSE stream tests (review finding 2, PR #327): the transport must
// parse an SSE response body incrementally. A subscriptions/listen response
// intentionally never closes, so any read-to-end buffering blocks Send
// forever and delivers nothing.
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sseStreamServer is an httptest server that answers every POST with an SSE
// stream fed from events; the stream stays open until release is closed.
type sseStreamServer struct {
	srv     *httptest.Server
	events  chan string
	release chan struct{}
}

func newSSEStreamServer(t *testing.T, contentType string) *sseStreamServer {
	t.Helper()
	s := &sseStreamServer{
		events:  make(chan string, 16),
		release: make(chan struct{}),
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		require.True(t, ok, "httptest response writer must support flushing")
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		for {
			select {
			case data := <-s.events:
				_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			case <-s.release:
				return
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func receiveWithTimeout(t *testing.T, tr *StreamableHTTPTransport, d time.Duration) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	msg, err := tr.Receive(ctx)
	require.NoError(t, err, "expected a message before the stream closed")
	return msg
}

// TestSSEStreamDeliversEventsWhileOpen is the subscriptions/listen shape: the
// acknowledgment and subsequent notifications must arrive while the response
// stream is still open. With read-to-end buffering this test deadlocks.
func TestSSEStreamDeliversEventsWhileOpen(t *testing.T) {
	s := newSSEStreamServer(t, "text/event-stream")
	// The acknowledgment is queued before the request so the handler writes
	// it immediately; the stream then stays open.
	s.events <- `{"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{"_meta":{"io.modelcontextprotocol/subscriptionId":7}}}`

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{Endpoint: s.srv.URL})
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()
	defer close(s.release)

	sendDone := make(chan error, 1)
	go func() {
		sendDone <- tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":7,"method":"subscriptions/listen","params":{}}`))
	}()

	select {
	case err := <-sendDone:
		require.NoError(t, err, "Send must return once the stream is handed off")
	case <-time.After(5 * time.Second):
		t.Fatal("Send blocked on an open SSE stream: the body is being buffered instead of streamed")
	}

	var ack struct {
		Method string `json:"method"`
	}
	require.NoError(t, json.Unmarshal(receiveWithTimeout(t, tr, 5*time.Second), &ack))
	assert.Equal(t, "notifications/subscriptions/acknowledged", ack.Method)

	// A later event on the same still-open stream must also arrive.
	s.events <- `{"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{"_meta":{"io.modelcontextprotocol/subscriptionId":7}}}`
	var change struct {
		Method string `json:"method"`
	}
	require.NoError(t, json.Unmarshal(receiveWithTimeout(t, tr, 5*time.Second), &change))
	assert.Equal(t, "notifications/tools/list_changed", change.Method)
}

// TestSSEStreamAbruptCloseSynthesizesStreamLost: a stream that ends without
// the final response must produce the CodeStreamLost error so the pending
// request fails promptly.
func TestSSEStreamAbruptCloseSynthesizesStreamLost(t *testing.T) {
	s := newSSEStreamServer(t, "text/event-stream")
	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{Endpoint: s.srv.URL})
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()

	require.NoError(t, tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{}}`)))
	close(s.release) // server drops the stream without answering id 9

	var resp struct {
		ID    int64 `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(receiveWithTimeout(t, tr, 5*time.Second), &resp))
	assert.Equal(t, int64(9), resp.ID)
	assert.Equal(t, CodeStreamLost, resp.Error.Code)
}

// TestCloseUnblocksOpenSSEStream: Close must not hang waiting for a stream
// the server keeps open; closing the body is the only way to interrupt the
// parser's blocking read.
func TestCloseUnblocksOpenSSEStream(t *testing.T) {
	s := newSSEStreamServer(t, "text/event-stream")
	defer close(s.release)

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{Endpoint: s.srv.URL})
	require.NoError(t, err)

	require.NoError(t, tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{}}`)))

	closed := make(chan error, 1)
	go func() { closed <- tr.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung on an open SSE stream")
	}
}

// TestRequestCancellationClosesSSEStream: cancelling the request context is
// the HTTP-transport cancellation signal (closing the SSE stream); the
// stream goroutine must exit without synthesizing a stream-lost error.
func TestRequestCancellationClosesSSEStream(t *testing.T) {
	s := newSSEStreamServer(t, "text/event-stream")
	defer close(s.release)

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{Endpoint: s.srv.URL})
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, tr.Send(ctx, []byte(`{"jsonrpc":"2.0","id":3,"method":"subscriptions/listen","params":{}}`)))
	cancel()

	// No message (in particular no synthesized stream-lost) may arrive.
	recvCtx, recvCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer recvCancel()
	msg, err := tr.Receive(recvCtx)
	require.Error(t, err, "cancellation must not synthesize a message, got: %s", msg)
}

// TestSSEContentTypeWithParameters: servers legitimately send media-type
// parameters; "text/event-stream; charset=utf-8" must be treated as SSE.
func TestSSEContentTypeWithParameters(t *testing.T) {
	s := newSSEStreamServer(t, "text/event-stream; charset=utf-8")
	s.events <- `{"jsonrpc":"2.0","id":4,"result":{"resultType":"complete"}}`
	defer close(s.release)

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{Endpoint: s.srv.URL})
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()

	require.NoError(t, tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`)))
	var resp struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(receiveWithTimeout(t, tr, 5*time.Second), &resp))
	assert.Equal(t, int64(4), resp.ID)
}

// TestNotificationAcknowledgmentWithoutContentType: per the transport spec a
// server answers an accepted notification POST with 202 and no body — and
// therefore no Content-Type. Send must treat that as success rather than
// rejecting an "unexpected Content-Type".
func TestNotificationAcknowledgmentWithoutContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{Endpoint: srv.URL})
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()

	require.NoError(t, tr.Send(context.Background(),
		[]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)),
		"a bodiless 202 acknowledgment must be accepted")
}
