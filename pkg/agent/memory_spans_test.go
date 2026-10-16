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

//go:build fts5

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// scriptedMemoryLLM answers the recall side calls: the search-query distill
// prompt and the rerank prompt.
type scriptedMemoryLLM struct {
	query, rerank string
}

func (s *scriptedMemoryLLM) Chat(_ context.Context, msgs []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	content := msgs[len(msgs)-1].Content
	if strings.HasPrefix(content, "User message:") {
		return &types.LLMResponse{Content: s.rerank}, nil
	}
	return &types.LLMResponse{Content: s.query}, nil
}
func (s *scriptedMemoryLLM) Name() string  { return "scripted" }
func (s *scriptedMemoryLLM) Model() string { return "scripted-v1" }

// failingLLM fails every call.
type failingLLM struct{}

func (failingLLM) Chat(context.Context, []types.Message, []shuttle.Tool) (*types.LLMResponse, error) {
	return nil, errors.New("provider unavailable")
}
func (failingLLM) Name() string  { return "failing" }
func (failingLLM) Model() string { return "failing-v1" }

func extractionAgent(t *testing.T, provider LLMProvider, store memory.GraphMemoryStore, tracer observability.Tracer) *Agent {
	t.Helper()
	ctx := context.Background()
	mem := NewMemory()
	session := mem.GetOrCreateSession(ctx, "extract-session")
	segMem := NewSegmentedMemory("", 200000, 20000)
	segMem.AddMessage(ctx, types.Message{Role: "user", Content: "The users table holds emails."})
	segMem.AddMessage(ctx, types.Message{Role: "assistant", Content: "Noted."})
	session.SegmentedMem = segMem
	return &Agent{
		llm:                         llm.NewInstrumentedProvider(provider, tracer, llm.WithRole(llm.RoleAgent)),
		graphMemoryStore:            store,
		enableGraphMemoryExtraction: true,
		graphExtractionCadence:      5,
		graphMemoryConfig:           &loomv1.GraphMemoryConfig{Enabled: true, EnableExtraction: true},
		memory:                      mem,
		config:                      &Config{Name: "test-agent"},
		tracer:                      tracer,
	}
}

func TestExtractGraphMemoryAsync_Span(t *testing.T) {
	ctx := context.Background()
	store := newTestGraphMemoryStore(t)
	_, err := store.CreateEntity(ctx, &memory.Entity{AgentID: "test-agent", Name: "users_table", EntityType: "dataset"})
	require.NoError(t, err)

	data, err := json.Marshal(ExtractedGraphData{
		Entities: []ExtractedEntity{
			{Name: "users_table", EntityType: "dataset"}, // already stored: not created
			{Name: "email_column", EntityType: "concept"},
		},
		Relationships: []ExtractedRelationship{{Source: "users_table", Target: "email_column", Relation: "CONTAINS"}},
		Memories:      []ExtractedMemory{{Content: "The users table has an email column", MemoryType: "fact", Salience: 0.6}},
	})
	require.NoError(t, err)

	tracer := observability.NewMockTracer()
	a := extractionAgent(t, &extractionMockLLM{response: string(data)}, store, tracer)
	a.extractGraphMemoryAsync(ctx, "extract-session")

	extract := tracer.GetSpansByName("graph_memory.extract")
	require.Len(t, extract, 1)
	attrs := extract[0].Attributes
	assert.Equal(t, "completed", attrs["extract.outcome"])
	assert.Equal(t, 1, attrs["extract.entities_created"], "an entity that was already stored is not counted")
	assert.Equal(t, 1, attrs["extract.relationships_created"])
	assert.Equal(t, 1, attrs["extract.memories_created"])
	assert.Equal(t, 0, attrs["extract.store_errors"])
	assert.Equal(t, "test-agent", attrs["agent.name"])
	_, hasSession := attrs[observability.AttrSessionID]
	assert.False(t, hasSession, "eval_runs analysis expects session.id only on server.Weave and agent.chat")

	llmSpans := tracer.GetSpansByName(observability.SpanLLMCompletion)
	require.Len(t, llmSpans, 1)
	assert.Equal(t, extract[0].SpanID, llmSpans[0].ParentID, "the extraction LLM call is a child of the extract span")
	assert.Equal(t, llm.RoleMemoryExtract, llmSpans[0].Attributes["llm.role"])
}

func TestExtractGraphMemoryAsync_SpanOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		provider LLMProvider
		session  string
		want     string
	}{
		{name: "unknown session", provider: &extractionMockLLM{response: "{}"}, session: "missing", want: "no_session"},
		{name: "LLM failure", provider: failingLLM{}, session: "extract-session", want: "llm_error"},
		{name: "unparsable response", provider: &extractionMockLLM{response: "not json"}, session: "extract-session", want: "parse_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := observability.NewMockTracer()
			a := extractionAgent(t, tt.provider, newTestGraphMemoryStore(t), tracer)
			a.extractGraphMemoryAsync(context.Background(), tt.session)
			spans := tracer.GetSpansByName("graph_memory.extract")
			require.Len(t, spans, 1)
			assert.Equal(t, tt.want, spans[0].Attributes["extract.outcome"])
		})
	}
}

func TestInjectGraphMemoryContext_RecallSpan(t *testing.T) {
	ctx := context.Background()
	store := newTestGraphMemoryStore(t)
	stored, err := store.Remember(ctx, &memory.Memory{
		AgentID: "recall-agent", Content: "The volatile table overflowed at two million rows",
		MemoryType: memory.MemoryTypeFact, Salience: 0.8,
	})
	require.NoError(t, err)

	tracer := observability.NewMockTracer()
	a := &Agent{
		llm:               llm.NewInstrumentedProvider(&scriptedMemoryLLM{query: "volatile table overflow", rerank: "1"}, tracer, llm.WithRole(llm.RoleAgent)),
		graphMemoryStore:  store,
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, MaxContextTokens: 2000},
		config:            &Config{Name: "recall-agent"},
		tracer:            tracer,
	}
	session := &types.Session{}
	session.AddMessage(ctx, types.Message{Role: "user", Content: "what broke in the volatile table?"})

	a.injectGraphMemoryContext(ctx, session)

	spans := tracer.GetSpansByName("graph_memory.recall")
	require.Len(t, spans, 1)
	recall := spans[0]
	attrs := recall.Attributes
	require.Equal(t, "injected", attrs["recall.outcome"])
	assert.Equal(t, "recall-agent", attrs["agent.name"])
	assert.Equal(t, "volatile table overflow", attrs["input.value"])
	assert.Contains(t, attrs["recall.candidate_ids"], stored.ID)
	assert.Equal(t, []string{stored.ID}, attrs["recall.selected_ids"])
	assert.Greater(t, attrs["recall.injected_tokens"], 0)
	assert.Contains(t, attrs["output.value"], "two million rows")
	assert.Equal(t, stored.ID, attrs["retrieval.documents.0.document.id"])
	assert.Contains(t, attrs["retrieval.documents.0.document.content"], "volatile table")

	roles := map[interface{}]bool{}
	for _, s := range tracer.GetSpansByName(observability.SpanLLMCompletion) {
		assert.Equal(t, recall.SpanID, s.ParentID, "recall LLM calls are children of the recall span")
		roles[s.Attributes["llm.role"]] = true
	}
	assert.Equal(t, map[interface{}]bool{llm.RoleMemoryQuery: true, llm.RoleMemoryRerank: true}, roles)
}

func TestReleasePressure_Span(t *testing.T) {
	sm := parallelBatchMemory(t, 12000, 1000, 40, "run_query")
	tracer := observability.NewMockTracer()
	sm.SetTracer(tracer)

	shed, estimate, target := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed)

	spans := tracer.GetSpansByName("memory.release_pressure")
	require.Len(t, spans, 1)
	attrs := spans[0].Attributes
	assert.Equal(t, true, attrs["pressure.shed"])
	assert.Equal(t, estimate, attrs["pressure.end_tokens"], "the span records the estimate the pass returned")
	assert.Equal(t, target, attrs["pressure.target_mark"])
	assert.Greater(t, attrs["pressure.start_tokens"], target)
	assert.Greater(t, attrs["pressure.offload_ops"], 0)
	assert.Equal(t, int64(2), attrs["pressure.turn"])

	// A second pass under the start mark sheds nothing and records no span.
	sm.ReleasePressure(context.Background(), 0)
	assert.Len(t, tracer.GetSpansByName("memory.release_pressure"), 1)
}

func TestLLMForRole(t *testing.T) {
	plain := &extractionMockLLM{}
	assert.Same(t, plain, llmForRole(plain, roleMemoryExtract), "an uninstrumented provider is returned as it is")

	instrumented := llm.NewInstrumentedProvider(plain, observability.NewMockTracer(), llm.WithRole(llm.RoleAgent))
	tagged, ok := llmForRole(instrumented, roleMemoryExtract).(*llm.InstrumentedProvider)
	require.True(t, ok)
	assert.Equal(t, llm.RoleMemoryExtract, tagged.Role())
	assert.Equal(t, llm.RoleAgent, instrumented.Role())
}
