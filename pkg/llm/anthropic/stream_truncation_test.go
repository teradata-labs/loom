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

package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// A stream that closes after message_start and a delta, without message_stop,
// is an error (llm.ErrStreamTruncated, HTTP 502), not a partial answer.
func TestChatStream_TruncatedStreamIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\n"+
			`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3,"output_tokens":1}}}`+"\n\n"+
			"event: content_block_start\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
			"event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`+"\n\n")
	}))
	defer srv.Close()
	c := NewClient(Config{APIKey: "test-key-not-real", Endpoint: srv.URL, Model: "claude-sonnet-4-5"})
	_, err := c.ChatStream(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrStreamTruncated)
	assert.Equal(t, llm.ProviderFailureUnavailable, llm.ClassifyProviderFailure(err))
}

// roundTripperFunc is a RoundTripper that is not an *http.Transport, as an
// OpenTelemetry-wrapped http.DefaultTransport is.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Stream timeouts with a replaced http.DefaultTransport must not panic.
func TestNewClient_StreamTimeoutsWithAWrappedDefaultTransport(t *testing.T) {
	orig := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(orig.RoundTrip)
	defer func() { http.DefaultTransport = orig }()
	assert.NotPanics(t, func() {
		NewClient(Config{APIKey: "test-key-not-real", StreamFirstByteTimeout: time.Second, StreamIdleTimeout: time.Second})
	})
}
