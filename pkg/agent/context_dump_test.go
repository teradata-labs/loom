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
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// These tests drive the context-dump instrument through the single real seam
// every provider call enters — chatWithRetry — and assert the debug switch's
// acceptance criteria against the on-disk per-run sink:
//
//	AC1: switch ON emits exactly one byte-for-byte (messages, tools) record per
//	     provider call, tagged with session id and a per-session turn number,
//	     for both the streaming and the non-streaming path.
//	AC2: switch OFF emits no record and leaves the provider path unchanged.
//	AC3: when ON, the record reaches only the local per-run sink — never the
//	     tracer (Hawk) or the shared zap log.

// --- test doubles -----------------------------------------------------------

// ctxDumpStubLLM is a non-streaming provider that records what it was dispatched
// and returns a fixed response, so a test can prove the provider path ran.
type ctxDumpStubLLM struct {
	mu       sync.Mutex
	calls    int
	gotMsgs  []Message
	gotTools []shuttle.Tool
	resp     *LLMResponse
}

func (s *ctxDumpStubLLM) Chat(_ context.Context, messages []Message, tools []shuttle.Tool) (*LLMResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.gotMsgs = messages
	s.gotTools = tools
	if s.resp != nil {
		return s.resp, nil
	}
	return &LLMResponse{Content: "ok", StopReason: "end_turn"}, nil
}

func (s *ctxDumpStubLLM) Name() string  { return "ctxdump-stub" }
func (s *ctxDumpStubLLM) Model() string { return "ctxdump-model" }

// ctxDumpStreamingStubLLM additionally implements StreamingLLMProvider, so
// chatWithRetry takes its streaming branch when a progress callback is present.
type ctxDumpStreamingStubLLM struct {
	ctxDumpStubLLM
	streamCalls int
}

func (s *ctxDumpStreamingStubLLM) ChatStream(_ context.Context, messages []Message, tools []shuttle.Tool, cb llmtypes.TokenCallback) (*LLMResponse, error) {
	s.mu.Lock()
	s.streamCalls++
	s.gotMsgs = messages
	s.gotTools = tools
	s.mu.Unlock()
	if cb != nil {
		cb("hel")
		cb("lo")
	}
	if s.resp != nil {
		return s.resp, nil
	}
	return &LLMResponse{Content: "hello", StopReason: "end_turn"}, nil
}

// ctxDumpStubTool is a shuttle.Tool whose name/description/schema/backend are
// the fields the dump record projects.
type ctxDumpStubTool struct {
	name    string
	desc    string
	backend string
	schema  *shuttle.JSONSchema
}

func (t *ctxDumpStubTool) Name() string                     { return t.name }
func (t *ctxDumpStubTool) Description() string              { return t.desc }
func (t *ctxDumpStubTool) InputSchema() *shuttle.JSONSchema { return t.schema }
func (t *ctxDumpStubTool) Backend() string                  { return t.backend }
func (t *ctxDumpStubTool) Execute(_ context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true}, nil
}

// ctxDumpRecordingTracer records every string handed to it so a test can assert
// that dump content never reaches the tracer (the Hawk export boundary).
type ctxDumpRecordingTracer struct {
	mu   sync.Mutex
	seen []string
}

func (tr *ctxDumpRecordingTracer) record(s string) {
	tr.mu.Lock()
	tr.seen = append(tr.seen, s)
	tr.mu.Unlock()
}

func (tr *ctxDumpRecordingTracer) StartSpan(ctx context.Context, name string, opts ...observability.SpanOption) (context.Context, *observability.Span) {
	span := &observability.Span{Name: name, Attributes: map[string]interface{}{}}
	for _, o := range opts {
		o(span)
	}
	tr.record(name)
	for k, v := range span.Attributes {
		tr.record(fmt.Sprintf("%s=%v", k, v))
	}
	return ctx, span
}

func (tr *ctxDumpRecordingTracer) EndSpan(_ *observability.Span) {}

func (tr *ctxDumpRecordingTracer) RecordMetric(name string, _ float64, labels map[string]string) {
	tr.record(name)
	for k, v := range labels {
		tr.record(k + "=" + v)
	}
}

func (tr *ctxDumpRecordingTracer) RecordEvent(_ context.Context, name string, attributes map[string]interface{}) {
	tr.record(name)
	for k, v := range attributes {
		tr.record(fmt.Sprintf("%s=%v", k, v))
	}
}

func (tr *ctxDumpRecordingTracer) Flush(_ context.Context) error { return nil }

func (tr *ctxDumpRecordingTracer) contains(sub string) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, s := range tr.seen {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// --- helpers ----------------------------------------------------------------

// newCtxDumpContext builds a Context carrying the session id (the seam the tap
// reads) and the given tracer and progress callback.
func newCtxDumpContext(sessionID string, tracer observability.Tracer, cb ProgressCallback) Context {
	return &agentContext{
		Context:          session.WithSessionID(context.Background(), sessionID),
		tracer:           tracer,
		progressCallback: cb,
	}
}

// redirectCtxDumpSink points the per-run sink at a fresh temp dir and returns
// the directory the sink files land in.
func redirectCtxDumpSink(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(envDebugDirVar, root)
	return filepath.Join(root, "context-dumps")
}

// resetCtxDumpEnvOnce re-arms the once-guarded environment switch so a test that
// depends on LOOM_DEBUG_CONTEXT_DUMP being unset is not defeated by an earlier
// evaluation cached in this process.
func resetCtxDumpEnvOnce() {
	envDumpOnce = sync.Once{}
	envDumpEnabled = false
}

// readDumpRecords requires exactly one per-run sink file and returns its records.
func readDumpRecords(t *testing.T, sinkDir string) []contextDumpRecord {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(sinkDir, "context-dump-*.jsonl"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "exactly one per-run sink file expected")

	data, err := os.ReadFile(matches[0])
	require.NoError(t, err)

	var recs []contextDumpRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r contextDumpRecord
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		recs = append(recs, r)
	}
	return recs
}

// --- AC1: ON emits one byte-for-byte record per provider call ---------------

// TestContextDump_On_NonStreaming_OneRecordPerCall drives the non-streaming
// branch twice on one session and asserts one dump per call, each capturing the
// dispatched (messages, tools) byte-for-byte and tagged with the session id and
// a monotonic per-session turn number.
func TestContextDump_On_NonStreaming_OneRecordPerCall(t *testing.T) {
	sinkDir := redirectCtxDumpSink(t)

	llm := &ctxDumpStubLLM{}
	a := &Agent{
		id:     "ac1-nonstream",
		llm:    llm,
		config: &Config{Debug: DebugConfig{ContextDump: true}}, // Retry zero-value => direct, non-streaming call
	}

	messages := []Message{
		{Role: "user", Content: "AC1-NONSTREAM-USER-PAYLOAD"},
		{Role: "assistant", Content: "prior assistant turn"},
	}
	tools := []shuttle.Tool{
		&ctxDumpStubTool{name: "run_sql", desc: "execute sql", backend: "teradata", schema: &shuttle.JSONSchema{Type: "object"}},
	}
	ctx := newCtxDumpContext("sess-ac1-ns", observability.NewNoOpTracer(), nil)

	_, err := a.chatWithRetry(ctx, messages, tools)
	require.NoError(t, err)
	_, err = a.chatWithRetry(ctx, messages, tools)
	require.NoError(t, err)

	require.Equal(t, 2, llm.calls, "both calls must reach the non-streaming provider")

	recs := readDumpRecords(t, sinkDir)
	require.Len(t, recs, 2, "exactly one dump record per provider call")

	expMsgs, err := json.Marshal(messages)
	require.NoError(t, err)

	for i, rec := range recs {
		assert.Equal(t, "sess-ac1-ns", rec.SessionID, "record tagged with session id")
		assert.Equal(t, i+1, rec.Turn, "per-session turn number is monotonic from 1")

		actMsgs, err := json.Marshal(rec.Messages)
		require.NoError(t, err)
		assert.JSONEq(t, string(expMsgs), string(actMsgs), "messages captured byte-for-byte")

		require.Len(t, rec.Tools, 1)
		assert.Equal(t, "run_sql", rec.Tools[0].Name)
		assert.Equal(t, "execute sql", rec.Tools[0].Description)
		assert.Equal(t, "teradata", rec.Tools[0].Backend)
		require.NotNil(t, rec.Tools[0].InputSchema)
		assert.Equal(t, "object", rec.Tools[0].InputSchema.Type)
	}
}

// TestContextDump_On_Streaming_OneRecordPerCall drives the streaming branch and
// asserts the same one-record-per-call capture holds when the provider streams.
func TestContextDump_On_Streaming_OneRecordPerCall(t *testing.T) {
	sinkDir := redirectCtxDumpSink(t)

	llm := &ctxDumpStreamingStubLLM{}
	require.True(t, llmtypes.SupportsStreaming(llm), "stub must be a streaming provider")

	a := &Agent{
		id:     "ac1-stream",
		llm:    llm,
		config: &Config{Debug: DebugConfig{ContextDump: true}},
	}

	messages := []Message{{Role: "user", Content: "AC1-STREAM-USER-PAYLOAD"}}
	tools := []shuttle.Tool{
		&ctxDumpStubTool{name: "fetch_rows", desc: "fetch rows", schema: &shuttle.JSONSchema{Type: "object"}},
	}
	// A non-nil progress callback selects the streaming branch in chatWithRetry.
	ctx := newCtxDumpContext("sess-ac1-str", observability.NewNoOpTracer(), func(ProgressEvent) {})

	_, err := a.chatWithRetry(ctx, messages, tools)
	require.NoError(t, err)

	require.Equal(t, 1, llm.streamCalls, "the streaming branch must reach ChatStream")
	require.Equal(t, 0, llm.calls, "the non-streaming Chat path must not be taken")

	recs := readDumpRecords(t, sinkDir)
	require.Len(t, recs, 1, "exactly one dump record for the streaming provider call")

	assert.Equal(t, "sess-ac1-str", recs[0].SessionID)
	assert.Equal(t, 1, recs[0].Turn)

	expMsgs, err := json.Marshal(messages)
	require.NoError(t, err)
	actMsgs, err := json.Marshal(recs[0].Messages)
	require.NoError(t, err)
	assert.JSONEq(t, string(expMsgs), string(actMsgs), "messages captured byte-for-byte on the streaming path")

	require.Len(t, recs[0].Tools, 1)
	assert.Equal(t, "fetch_rows", recs[0].Tools[0].Name)
}

// --- AC2: OFF emits no record and leaves the provider path unchanged --------

// TestContextDump_Off_NoRecord_ProviderPathUnchanged asserts that with the
// switch off (config false, env unset) no sink is written, no dumper is
// constructed, and the provider call returns its response exactly as today.
func TestContextDump_Off_NoRecord_ProviderPathUnchanged(t *testing.T) {
	sinkDir := redirectCtxDumpSink(t)

	// Force the environment switch off and re-evaluate it for this test.
	t.Setenv(envContextDumpVar, "")
	resetCtxDumpEnvOnce()

	llm := &ctxDumpStubLLM{resp: &LLMResponse{Content: "prod-response", StopReason: "end_turn"}}
	a := &Agent{
		id:     "ac2-off",
		llm:    llm,
		config: &Config{Debug: DebugConfig{ContextDump: false}},
	}

	messages := []Message{{Role: "user", Content: "AC2-PAYLOAD"}}
	tools := []shuttle.Tool{&ctxDumpStubTool{name: "noop"}}
	ctx := newCtxDumpContext("sess-ac2", observability.NewNoOpTracer(), nil)

	resp, err := a.chatWithRetry(ctx, messages, tools)
	require.NoError(t, err)

	// Provider path behaves exactly as today: the LLM is called and its response
	// is returned unchanged.
	assert.Equal(t, 1, llm.calls, "provider is still called when the switch is off")
	require.NotNil(t, resp)
	assert.Equal(t, "prod-response", resp.Content)

	// No dump record is emitted.
	matches, err := filepath.Glob(filepath.Join(sinkDir, "context-dump-*.jsonl"))
	require.NoError(t, err)
	assert.Empty(t, matches, "switch OFF must emit no dump record")

	// The tap early-returns before constructing the sink — a true no-op.
	assert.Nil(t, a.contextDump, "switch OFF must not construct the sink")
}

// --- AC3: ON writes only to the local sink, never tracer or shared log ------

// TestContextDump_On_LocalSinkOnly_NeverTracerOrZap asserts that an un-redacted
// dump lands in the local per-run sink while its content never reaches the
// tracer (Hawk) and the success path emits nothing to the shared zap log.
func TestContextDump_On_LocalSinkOnly_NeverTracerOrZap(t *testing.T) {
	sinkDir := redirectCtxDumpSink(t)

	// Capture the global zap logger for the duration of the call.
	core, logs := observer.New(zapcore.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()

	tracer := &ctxDumpRecordingTracer{}

	llm := &ctxDumpStubLLM{}
	a := &Agent{
		id:     "ac3-routing",
		llm:    llm,
		config: &Config{Debug: DebugConfig{ContextDump: true}},
	}

	const secretMsg = "AC3-UNREDACTED-SECRET-PAYLOAD"
	const secretTool = "AC3-SECRET-TOOL-NAME"
	messages := []Message{{Role: "user", Content: secretMsg}}
	tools := []shuttle.Tool{
		&ctxDumpStubTool{name: secretTool, desc: "secret schema", backend: "teradata", schema: &shuttle.JSONSchema{Type: "object"}},
	}
	ctx := newCtxDumpContext("sess-ac3", tracer, nil)

	_, err := a.chatWithRetry(ctx, messages, tools)
	require.NoError(t, err)

	// The un-redacted record IS written to the local per-run sink.
	recs := readDumpRecords(t, sinkDir)
	require.Len(t, recs, 1)
	require.Len(t, recs[0].Messages, 1)
	assert.Equal(t, secretMsg, recs[0].Messages[0].Content, "un-redacted record lands in the local sink")
	require.Len(t, recs[0].Tools, 1)
	assert.Equal(t, secretTool, recs[0].Tools[0].Name)

	// ...and its content never reaches the tracer (the Hawk export boundary).
	assert.False(t, tracer.contains(secretMsg), "dump message content must never reach the tracer/Hawk")
	assert.False(t, tracer.contains(secretTool), "dump tool content must never reach the tracer/Hawk")

	// ...nor the shared zap log: the success path logs nothing at all.
	assert.Equal(t, 0, logs.Len(), "context dump must not emit any log on the success path")
	for _, e := range logs.All() {
		assert.NotContains(t, e.Message, secretMsg)
		for _, f := range e.Context {
			assert.NotContains(t, f.String, secretMsg)
			assert.NotContains(t, fmt.Sprintf("%v", f.Interface), secretMsg)
		}
	}
}
