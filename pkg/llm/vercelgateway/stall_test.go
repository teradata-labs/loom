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

package vercelgateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
)

// stallFirst answers the first request with headers and then silence (until
// the client gives up), and every later request normally.
func stallFirst(calls *atomic.Int32) func(http.ResponseWriter, *http.Request, []byte) {
	return func(w http.ResponseWriter, r *http.Request, raw []byte) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		okHandler(w, r, raw)
	}
}

func shortStreamTimeouts(cfg *Config) {
	cfg.StreamFirstByteTimeout = 150 * time.Millisecond
	cfg.StreamIdleTimeout = 150 * time.Millisecond
}

func TestClient_StalledStreamIsRetriedOnce(t *testing.T) {
	for _, model := range []string{"anthropic/claude-sonnet-4.5", "openai/gpt-5"} {
		t.Run(model, func(t *testing.T) {
			var calls atomic.Int32
			g := &gateway{handler: stallFirst(&calls)}
			c := newTestClient(t, g.start(t).URL, model, shortStreamTimeouts)

			started := time.Now()
			resp, err := c.Chat(context.Background(), hi, nil)
			require.NoError(t, err)
			assert.NotEmpty(t, resp.Content)
			assert.Equal(t, int32(2), calls.Load(), "one stalled attempt, one retry")
			assert.Less(t, time.Since(started), 5*time.Second, "the stall is caught by the first-byte timeout, not the 300 s request timeout")
		})
	}
}

func TestClient_StallAfterDeliveredTokensIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	g := &gateway{handler: func(w http.ResponseWriter, r *http.Request, _ []byte) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `data: {"id":"gen_x","object":"chat.completion.chunk","created":1,"model":"openai/gpt-5","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}}
	c := newTestClient(t, g.start(t).URL, "openai/gpt-5", shortStreamTimeouts)

	var got strings.Builder
	_, err := c.ChatStream(context.Background(), hi, nil, func(tok string) { got.WriteString(tok) })
	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrStreamTimeout)
	assert.Equal(t, "partial", got.String())
	assert.Equal(t, int32(1), calls.Load(), "a retry would repeat text the caller already has")
}

func TestClient_StallTwiceReturnsTimeout(t *testing.T) {
	var calls atomic.Int32
	g := &gateway{handler: func(w http.ResponseWriter, r *http.Request, _ []byte) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}}
	c := newTestClient(t, g.start(t).URL, "anthropic/claude-sonnet-4.5", shortStreamTimeouts)
	_, err := c.Chat(context.Background(), hi, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrStreamTimeout)
	assert.Equal(t, int32(2), calls.Load())
}

func TestClient_TruncatedStreamIsAnError(t *testing.T) {
	tests := []struct {
		model, body string
	}{
		// message_start and a text delta, then the connection closes.
		{"anthropic/claude-sonnet-4.5", messagesSSE[:strings.Index(messagesSSE, "event: content_block_stop")]},
		// one content chunk, no finish_reason, no [DONE].
		{"openai/gpt-5", chatSSE[:strings.Index(chatSSE, "\n\n")+2]},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			g := &gateway{handler: func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tt.body)
			}}
			c := newTestClient(t, g.start(t).URL, tt.model)
			_, err := c.Chat(context.Background(), hi, nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
			assert.Contains(t, err.Error(), "truncated response")
		})
	}
}
