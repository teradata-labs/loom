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
package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// weaveEntryPoint runs one request through a server entry point and returns
// the response text the client received.
type weaveEntryPoint struct {
	name     string
	spanName string
	run      func(t *testing.T, query string) (*observability.MockTracer, string)
}

func weaveEntryPoints() []weaveEntryPoint {
	newAgent := func(tr observability.Tracer) *agent.Agent {
		return agent.NewAgent(&mockBackend{}, &mockLLMForMultiAgent{}, agent.WithTracer(tr))
	}
	streamText := func(t *testing.T, stream *mockStreamWeaveStream) string {
		t.Helper()
		require.NotEmpty(t, stream.progress)
		last := stream.progress[len(stream.progress)-1]
		require.Equal(t, loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED, last.Stage)
		return last.PartialContent
	}
	return []weaveEntryPoint{
		{"MultiAgentServer.Weave", "server.Weave", func(t *testing.T, q string) (*observability.MockTracer, string) {
			tr := observability.NewMockTracer()
			srv := NewMultiAgentServer(map[string]*agent.Agent{"default": newAgent(tr)}, nil)
			srv.SetTracer(tr)
			resp, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{Query: q, SessionId: "preview-ma-weave"})
			require.NoError(t, err)
			return tr, resp.Text
		}},
		{"MultiAgentServer.StreamWeave", "server.StreamWeave", func(t *testing.T, q string) (*observability.MockTracer, string) {
			tr := observability.NewMockTracer()
			srv := NewMultiAgentServer(map[string]*agent.Agent{"default": newAgent(tr)}, nil)
			srv.SetTracer(tr)
			stream := &mockStreamWeaveStream{ctx: context.Background()}
			require.NoError(t, srv.StreamWeave(&loomv1.WeaveRequest{Query: q, SessionId: "preview-ma-stream"}, stream))
			return tr, streamText(t, stream)
		}},
		{"Server.Weave", "server.Weave", func(t *testing.T, q string) (*observability.MockTracer, string) {
			tr := observability.NewMockTracer()
			srv := NewServer(newAgent(tr), nil)
			srv.SetTracer(tr)
			resp, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{Query: q, SessionId: "preview-s-weave"})
			require.NoError(t, err)
			return tr, resp.Text
		}},
		{"Server.StreamWeave", "server.StreamWeave", func(t *testing.T, q string) (*observability.MockTracer, string) {
			tr := observability.NewMockTracer()
			srv := NewServer(newAgent(tr), nil)
			srv.SetTracer(tr)
			stream := &mockStreamWeaveStream{ctx: context.Background()}
			require.NoError(t, srv.StreamWeave(&loomv1.WeaveRequest{Query: q, SessionId: "preview-s-stream"}, stream))
			return tr, streamText(t, stream)
		}},
	}
}

// TestWeaveServerSpans_CarryMessageAndResponse checks that the root span of
// every request entry point records the user's message and the response
// (BUG-4): Phoenix fills its trace and session list columns from root spans
// only, so without them every chat and experiment turn showed as empty.
func TestWeaveServerSpans_CarryMessageAndResponse(t *testing.T) {
	for _, ep := range weaveEntryPoints() {
		t.Run(ep.name, func(t *testing.T) {
			query := "Which AI conferences run in December 2026?"
			tr, text := ep.run(t, query)
			require.NotEmpty(t, text)

			span := tr.GetSpanByName(ep.spanName)
			require.NotNil(t, span, "%s span must be recorded", ep.spanName)
			assert.Empty(t, span.ParentID, "%s is the root of the request's trace", ep.spanName)
			assert.Equal(t, query, span.Attributes["message.preview"])
			assert.Equal(t, text, span.Attributes["response.preview"])
			assert.Equal(t, len(query), span.Attributes["query.length"], "existing attributes are kept")
		})
	}
}

// TestWeaveServerSpans_PreviewsFollowContentBound checks that the previews use
// the content bound (observability.DefaultMaxContentLength) and that the
// export redaction applies to them.
func TestWeaveServerSpans_PreviewsFollowContentBound(t *testing.T) {
	const limit = observability.DefaultMaxContentLength
	const bearer = "planted123bearer456token789"
	query := "Authorization: Bearer " + bearer + " " + strings.Repeat("long question ", 20)
	for _, ep := range weaveEntryPoints() {
		t.Run(ep.name, func(t *testing.T) {
			tr, _ := ep.run(t, query)
			span := tr.GetSpanByName(ep.spanName)
			require.NotNil(t, span)

			msg, _ := span.Attributes["message.preview"].(string)
			resp, _ := span.Attributes["response.preview"].(string)
			assert.Equal(t, observability.TruncateContent(observability.RedactText(query)), msg)
			assert.NotContains(t, msg, bearer, "previews are redacted when set")
			assert.Less(t, len([]rune(msg)), len([]rune(query)), "message truncated")
			assert.LessOrEqual(t, len([]rune(msg)), limit+1, "message bounded (plus the one-rune truncation marker)")
			assert.LessOrEqual(t, len([]rune(resp)), limit+1, "response bounded (plus the one-rune truncation marker)")

			exported := observability.RedactSpan(span, observability.PrivacyConfig{RedactCredentials: true, RedactPII: true})
			assert.NotContains(t, fmt.Sprint(exported.Attributes), bearer, "previews are redacted on export")
		})
	}
}

// TestWeaveServerSpans_NoPreviewForEmptyQuery keeps the attribute off a span
// whose request had no message.
func TestWeaveServerSpans_NoPreviewForEmptyQuery(t *testing.T) {
	tr := observability.NewMockTracer()
	_, span := startWeaveSpan(context.Background(), tr, "server.Weave", &loomv1.WeaveRequest{})
	require.NotNil(t, span)
	_, ok := span.Attributes["message.preview"]
	assert.False(t, ok)
	recordWeaveSpanUsage(span, &agent.Response{})
	_, ok = span.Attributes["response.preview"]
	assert.False(t, ok)
}

type staticReplyLLM struct {
	reply string
}

func (l *staticReplyLLM) Chat(_ context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return &llmtypes.LLMResponse{Content: l.reply}, nil
}
func (l *staticReplyLLM) Name() string  { return "static" }
func (l *staticReplyLLM) Model() string { return "static-v1" }

// TestWeaveGetTrace_RedactsCredentialsAndPIIInPreviews verifies that credentials
// and PII in the user's query and the agent's reply are redacted before
// message.preview / response.preview reach GetTrace (F2), including when a
// secret crosses the 200-rune preview boundary.
func TestWeaveGetTrace_RedactsCredentialsAndPIIInPreviews(t *testing.T) {
	const (
		bearer     = "planted123bearer456token789"
		email      = "alice.secret@example.com"
		boundarySk = "sk-boundary1234567890abcdefghijklmnop"
		replyKey   = "tvly-replysecret1234567890abcdef"
		replySSN   = "123-45-6789"
	)
	// Place boundarySk so it starts before rune 200 and extends past rune 200.
	prefix := "Contact " + email + " with Authorization: Bearer " + bearer + " "
	padLen := observability.DefaultMaxContentLength - len([]rune(prefix)) - 10
	require.Greater(t, padLen, 0)
	query := prefix + strings.Repeat("x", padLen) + " " + boundarySk
	reply := "Used key " + replyKey + " for SSN " + replySSN

	for _, streaming := range []bool{false, true} {
		name := "Weave"
		if streaming {
			name = "StreamWeave"
		}
		t.Run(name, func(t *testing.T) {
			tr := observability.NewMockTracer()
			ag := agent.NewAgent(&mockBackend{}, &staticReplyLLM{reply: reply}, agent.WithTracer(tr))
			srv := NewMultiAgentServer(map[string]*agent.Agent{"default": ag}, nil)
			srv.SetTracer(tr)

			ctx := context.Background()
			var traceID string
			if !streaming {
				resp, err := srv.Weave(ctx, &loomv1.WeaveRequest{Query: query, SessionId: "f2-weave"})
				require.NoError(t, err)
				traceID = resp.TraceId
			} else {
				stream := &mockStreamWeaveStream{ctx: ctx}
				require.NoError(t, srv.StreamWeave(&loomv1.WeaveRequest{Query: query, SessionId: "f2-stream"}, stream))
				require.NotEmpty(t, stream.progress)
				traceID = stream.progress[len(stream.progress)-1].TraceId
			}
			require.NotEmpty(t, traceID)

			traceResp, err := srv.GetTrace(ctx, &loomv1.GetTraceRequest{TraceId: traceID})
			require.NoError(t, err)
			require.NotEmpty(t, traceResp.Spans)

			dumped := fmt.Sprintf("%+v", traceResp.Spans)
			for _, secret := range []string{bearer, email, boundarySk, boundarySk[:10], replyKey, replySSN} {
				assert.NotContains(t, dumped, secret, "GetTrace leaked %q", secret)
			}
		})
	}
}

// TestServer_ConcurrentSetTracerAndWeave verifies that Server synchronizes
// tracer reads and writes across SetTracer, Weave, and StreamWeave (F4).
func TestServer_ConcurrentSetTracerAndWeave(t *testing.T) {
	tr1 := observability.NewMockTracer()
	tr2 := observability.NewMockTracer()
	ag := agent.NewAgent(&mockBackend{}, &mockLLMForMultiAgent{}, agent.WithTracer(tr1))
	srv := NewServer(ag, nil)
	srv.SetTracer(tr1)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func(iter int) {
			defer wg.Done()
			if iter%2 == 0 {
				srv.SetTracer(tr2)
			} else {
				srv.SetTracer(tr1)
			}
		}(i)
		go func(iter int) {
			defer wg.Done()
			_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
				Query:     "hello",
				SessionId: fmt.Sprintf("race-weave-%d", iter),
			})
			assert.NoError(t, err)
		}(i)
		go func(iter int) {
			defer wg.Done()
			stream := &mockStreamWeaveStream{ctx: context.Background()}
			err := srv.StreamWeave(&loomv1.WeaveRequest{
				Query:     "hello",
				SessionId: fmt.Sprintf("race-stream-%d", iter),
			}, stream)
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()
}
