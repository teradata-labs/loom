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
package openai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/types"
)

func TestClient_ChatStream_StreamIdleTimeoutResetsOnRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()

		chunks := []string{
			`{"choices":[{"delta":{"content":"A"}}]}`,
			`{"choices":[{"delta":{"content":"B"}}]}`,
			`{"choices":[{"delta":{"content":"C"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		}
		for index, chunk := range chunks {
			if index > 0 {
				time.Sleep(75 * time.Millisecond)
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(Config{
		APIKey:                 "test-key",
		Endpoint:               server.URL,
		Timeout:                100 * time.Millisecond,
		StreamFirstByteTimeout: 250 * time.Millisecond,
		StreamIdleTimeout:      250 * time.Millisecond,
	})
	response, err := client.ChatStream(
		context.Background(),
		[]types.Message{{Role: "user", Content: "hello"}},
		nil,
		nil,
	)

	require.NoError(t, err)
	assert.Equal(t, "ABC", response.Content)
	assert.Equal(t, "end_turn", response.StopReason)
}

func TestClient_ChatStream_StreamFirstByteTimeoutStopsSilentStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		APIKey:                 "test-key",
		Endpoint:               server.URL,
		Timeout:                2 * time.Second,
		StreamFirstByteTimeout: 50 * time.Millisecond,
		StreamIdleTimeout:      time.Second,
	})
	started := time.Now()
	_, err := client.ChatStream(
		context.Background(),
		[]types.Message{{Role: "user", Content: "hello"}},
		nil,
		nil,
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	var timeoutErr net.Error
	require.True(t, errors.As(err, &timeoutErr))
	assert.True(t, timeoutErr.Timeout())
	assert.Contains(t, err.Error(), "first-byte")
	assert.Less(t, time.Since(started), time.Second)
}

func TestClient_ChatStream_StreamIdleTimeoutStartsAfterFirstRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()

		time.Sleep(100 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"A\"}}]}\n\n")
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	client := NewClient(Config{
		APIKey:                 "test-key",
		Endpoint:               server.URL,
		Timeout:                2 * time.Second,
		StreamFirstByteTimeout: 250 * time.Millisecond,
		StreamIdleTimeout:      50 * time.Millisecond,
	})
	_, err := client.ChatStream(
		context.Background(),
		[]types.Message{{Role: "user", Content: "hello"}},
		nil,
		nil,
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "idle")
}

func TestClient_ChatStream_DefaultsToSplitTimeouts(t *testing.T) {
	client := NewClient(Config{Timeout: 2 * time.Second})

	assert.Equal(t, DefaultOpenAIStreamFirstByteTimeout, client.streamFirstByteTimeout)
	assert.Equal(t, DefaultOpenAIStreamIdleTimeout, client.streamIdleTimeout)
	streamClient := client.streamingHTTPClient()
	assert.NotSame(t, client.httpClient, streamClient)
	assert.Zero(t, streamClient.Timeout)
	assert.Equal(t, 2*time.Second, client.httpClient.Timeout)
	streamTransport, ok := streamClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, DefaultOpenAIStreamFirstByteTimeout, streamTransport.ResponseHeaderTimeout)
	assert.NotSame(t, client.httpClient.Transport, streamTransport)
}

func TestClient_ChatStream_DefaultsDoNotApplyWholeRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()

		for index := 0; index < 5; index++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(40 * time.Millisecond):
				_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"A\"}}]}\n\n")
				flusher.Flush()
			}
		}
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(Config{
		APIKey:   "test-key",
		Endpoint: server.URL,
		Timeout:  150 * time.Millisecond,
	})
	started := time.Now()
	response, err := client.ChatStream(
		context.Background(),
		[]types.Message{{Role: "user", Content: "hello"}},
		nil,
		nil,
	)

	require.NoError(t, err)
	assert.Equal(t, "AAAAA", response.Content)
	assert.Greater(t, time.Since(started), 150*time.Millisecond)
}

func TestClient_Chat_StreamIdleTimeoutDoesNotDisableRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		APIKey:                 "test-key",
		Endpoint:               server.URL,
		Timeout:                100 * time.Millisecond,
		StreamFirstByteTimeout: time.Second,
		StreamIdleTimeout:      time.Second,
	})
	_, err := client.Chat(
		context.Background(),
		[]types.Message{{Role: "user", Content: "hello"}},
		nil,
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
