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
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

const testEmbedModel = "fake-embed-v1"

// fakeEmbedder returns a fixed vector per known text and other for the rest.
type fakeEmbedder struct {
	vectors map[string][]float32
	other   []float32
	err     error
}

func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	if v, ok := f.vectors[text]; ok {
		return v, nil
	}
	return f.other, nil
}

func (f *fakeEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := f.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}
func (f *fakeEmbedder) Dimensions() int { return 3 }
func (f *fakeEmbedder) Model() string   { return testEmbedModel }

func recallTestAgent(store memory.GraphMemoryStore, llmScript *scriptedMemoryLLM, embedder memory.Embedder, budget int32) (*Agent, *observability.MockTracer) {
	tracer := observability.NewMockTracer()
	return &Agent{
		llm:               llm.NewInstrumentedProvider(llmScript, tracer, llm.WithRole(llm.RoleAgent)),
		graphMemoryStore:  store,
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, MaxContextTokens: budget},
		config:            &Config{Name: "recall-agent"},
		tracer:            tracer,
		embedder:          embedder,
	}, tracer
}

func sessionWith(ctx context.Context, msgs ...types.Message) *types.Session {
	s := &types.Session{}
	for _, m := range msgs {
		s.AddMessage(ctx, m)
	}
	return s
}

func laterTurn(ctx context.Context, user string) *types.Session {
	return sessionWith(ctx,
		types.Message{Role: "user", Content: "Find AI conferences in December."},
		types.Message{Role: "assistant", Content: "Started the discovery workflow."},
		types.Message{Role: "user", Content: user})
}

func recallSpan(t *testing.T, tracer *observability.MockTracer) map[string]interface{} {
	t.Helper()
	spans := tracer.GetSpansByName("graph_memory.recall")
	require.Len(t, spans, 1)
	return spans[0].Attributes
}

func remember(t *testing.T, store memory.GraphMemoryStore, m *memory.Memory) *memory.Memory {
	t.Helper()
	if m.AgentID == "" {
		m.AgentID = "recall-agent"
	}
	if m.MemoryType == "" {
		m.MemoryType = memory.MemoryTypeFact
	}
	stored, err := store.Remember(context.Background(), m)
	require.NoError(t, err)
	return stored
}

// TestInjectGraphMemoryContext_VectorRecallFindsParaphrase is the BUG-8
// case: a message that shares no word with a stored memory finds it through
// the embedding; keyword recall alone finds nothing.
func TestInjectGraphMemoryContext_VectorRecallFindsParaphrase(t *testing.T) {
	ctx := context.Background()
	store := newTestGraphMemoryStore(t)
	near := []float32{1, 0, 0}
	target := remember(t, store, &memory.Memory{
		Content: "The discovery workflow is running its second stage", Salience: 0.6,
		Embedding: near, EmbeddingModel: testEmbedModel,
	})
	remember(t, store, &memory.Memory{ // decayed below the salience floor
		Content: "An old note about the discovery stage", Salience: 0.05,
		Embedding: near, EmbeddingModel: testEmbedModel,
	})
	remember(t, store, &memory.Memory{ // a vector from another model is not comparable
		Content: "The discovery workflow finished stage one", Salience: 0.6,
		Embedding: near, EmbeddingModel: "other-model",
	})
	script := &scriptedMemoryLLM{query: "pipeline working status", rerank: "1"}

	t.Run("keyword recall alone misses it", func(t *testing.T) {
		a, tracer := recallTestAgent(store, script, nil, 2000)
		a.injectGraphMemoryContext(ctx, laterTurn(ctx, "is the pipeline working?"))
		attrs := recallSpan(t, tracer)
		assert.Equal(t, "no_candidates", attrs["recall.outcome"])
		assert.NotContains(t, attrs, "recall.vector_candidates")
	})

	t.Run("semantic recall finds it", func(t *testing.T) {
		embedder := &fakeEmbedder{vectors: map[string][]float32{"pipeline working status": {0.9, 0.1, 0}}, other: []float32{0, 0, 1}}
		a, tracer := recallTestAgent(store, script, embedder, 2000)
		session := laterTurn(ctx, "is the pipeline working?")
		a.injectGraphMemoryContext(ctx, session)
		attrs := recallSpan(t, tracer)
		require.Equal(t, "injected", attrs["recall.outcome"])
		assert.Equal(t, 1, attrs["recall.vector_candidates"])
		assert.Equal(t, false, attrs["recall.vector_error"])
		assert.Equal(t, testEmbedModel, attrs["recall.embedding_model"])
		assert.Equal(t, []string{target.ID}, attrs["recall.selected_ids"])
		msgs := session.GetMessages()
		assert.Contains(t, msgs[len(msgs)-1].Content, relevantMemoriesHeader)
		assert.Contains(t, msgs[len(msgs)-1].Content, "second stage")
	})

	t.Run("a vector below the similarity threshold is not a candidate", func(t *testing.T) {
		embedder := &fakeEmbedder{other: []float32{0, 0, 1}}
		a, tracer := recallTestAgent(store, script, embedder, 2000)
		a.injectGraphMemoryContext(ctx, laterTurn(ctx, "is the pipeline working?"))
		attrs := recallSpan(t, tracer)
		assert.Equal(t, "no_candidates", attrs["recall.outcome"])
		assert.Equal(t, 0, attrs["recall.vector_candidates"])
	})
}

// TestInjectGraphMemoryContext_VectorErrorIsNotAMiss: when the embedding call
// fails and nothing else is found, the outcome says so.
func TestInjectGraphMemoryContext_VectorErrorIsNotAMiss(t *testing.T) {
	ctx := context.Background()
	store := newTestGraphMemoryStore(t)
	remember(t, store, &memory.Memory{Content: "The discovery workflow is running", Salience: 0.6})
	a, tracer := recallTestAgent(store, &scriptedMemoryLLM{query: "pipeline status", rerank: "1"},
		&fakeEmbedder{err: errors.New("embedding API unavailable")}, 2000)
	a.injectGraphMemoryContext(ctx, laterTurn(ctx, "status?"))
	attrs := recallSpan(t, tracer)
	assert.Equal(t, "vector_error", attrs["recall.outcome"])
	assert.Equal(t, true, attrs["recall.vector_error"])
}

// TestInjectGraphMemoryContext_SessionStartFallback: the first turn of a
// session that recalls nothing for its message ("Hi") gets the agent's
// highest-salience memories, without task-completion memories; later turns
// do not.
func TestInjectGraphMemoryContext_SessionStartFallback(t *testing.T) {
	ctx := context.Background()
	store := newTestGraphMemoryStore(t)
	top := remember(t, store, &memory.Memory{Content: "User is preparing AI conference talks for December", Salience: 0.9, Source: "conversation"})
	second := remember(t, store, &memory.Memory{Content: "User prefers academic venues", Salience: 0.6, Source: "conversation"})
	third := remember(t, store, &memory.Memory{Content: "User is based in Austin", Salience: 0.4})
	remember(t, store, &memory.Memory{Content: "Completed task: Hi\nResult: Turn completed — 9 tool calls.", Salience: 0.75, Source: memory.SourceTask})
	remember(t, store, &memory.Memory{Content: "A stale observation", Salience: 0.05})
	script := &scriptedMemoryLLM{query: "greeting", rerank: "none"}

	t.Run("first turn", func(t *testing.T) {
		a, tracer := recallTestAgent(store, script, nil, 2000)
		session := sessionWith(ctx, types.Message{Role: "user", Content: "Hi"})
		a.injectGraphMemoryContext(ctx, session)
		attrs := recallSpan(t, tracer)
		require.Equal(t, "fallback_injected", attrs["recall.outcome"])
		assert.Equal(t, "session_start", attrs["recall.fallback"])
		assert.Equal(t, 0, attrs["recall.candidates"])
		assert.Equal(t, []string{top.ID, second.ID, third.ID}, attrs["recall.selected_ids"], "by salience; no task memory, nothing below the floor")
		assert.Equal(t, top.ID, attrs["retrieval.documents.0.document.id"])
		msgs := session.GetMessages()
		block := msgs[len(msgs)-1].Content
		assert.True(t, strings.HasPrefix(block, graphMemoryContextMarker+"\n"+sessionStartMemoriesHeader), block)
		assert.NotContains(t, block, "Completed task")
	})

	t.Run("later turn", func(t *testing.T) {
		a, tracer := recallTestAgent(store, script, nil, 2000)
		session := laterTurn(ctx, "Hi again")
		before := len(session.GetMessages())
		a.injectGraphMemoryContext(ctx, session)
		attrs := recallSpan(t, tracer)
		assert.Equal(t, "no_candidates", attrs["recall.outcome"])
		assert.NotContains(t, attrs, "recall.fallback")
		assert.Len(t, session.GetMessages(), before, "nothing injected")
	})

	t.Run("first turn whose candidates the re-rank rejects", func(t *testing.T) {
		greeting := remember(t, store, &memory.Memory{AgentID: "greeting-agent", Content: "User greets with a short greeting", Salience: 0.3})
		a, tracer := recallTestAgent(store, script, nil, 2000)
		a.config.Name = "greeting-agent"
		a.injectGraphMemoryContext(ctx, sessionWith(ctx, types.Message{Role: "user", Content: "Hi"}))
		attrs := recallSpan(t, tracer)
		assert.Equal(t, "fallback_injected", attrs["recall.outcome"])
		assert.Equal(t, 1, attrs["recall.candidates"])
		assert.Equal(t, []string{greeting.ID}, attrs["recall.selected_ids"])
	})

	t.Run("token budget", func(t *testing.T) {
		mems, err := store.Recall(ctx, memory.RecallOpts{AgentID: "recall-agent", Limit: 2, ExcludeSources: []string{memory.SourceTask}})
		require.NoError(t, err)
		require.Len(t, mems, 2)
		budget := mems[0].TokenCount + mems[1].TokenCount - 1
		require.GreaterOrEqual(t, budget, mems[0].TokenCount)
		a, tracer := recallTestAgent(store, script, nil, int32(budget))
		a.injectGraphMemoryContext(ctx, sessionWith(ctx, types.Message{Role: "user", Content: "Hi"}))
		assert.Equal(t, []string{top.ID}, recallSpan(t, tracer)["recall.selected_ids"])
	})

	t.Run("an agent with no memories", func(t *testing.T) {
		a, tracer := recallTestAgent(store, script, nil, 2000)
		a.config.Name = "empty-agent"
		a.injectGraphMemoryContext(ctx, sessionWith(ctx, types.Message{Role: "user", Content: "Hi"}))
		assert.Equal(t, "no_candidates", recallSpan(t, tracer)["recall.outcome"])
	})
}

// noVectorStore is a store whose backend keeps no embeddings (as PostgreSQL).
type noVectorStore struct{ memory.GraphMemoryStore }

func (noVectorStore) VectorRecall(context.Context, memory.VectorRecallOpts) ([]*memory.Memory, error) {
	return nil, fmt.Errorf("test store: %w", memory.ErrVectorRecallUnsupported)
}

// countingEmbedder counts Embed calls.
type countingEmbedder struct {
	fakeEmbedder
	calls int
}

func (c *countingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	c.calls++
	return c.fakeEmbedder.Embed(ctx, text)
}

// TestInjectGraphMemoryContext_VectorRecallUnsupportedFallsBackToKeywords: a
// backend without embeddings is keyword recall, not a failure, and later
// turns stop embedding the query.
func TestInjectGraphMemoryContext_VectorRecallUnsupportedFallsBackToKeywords(t *testing.T) {
	ctx := context.Background()
	store := noVectorStore{newTestGraphMemoryStore(t)}
	embedder := &countingEmbedder{fakeEmbedder: fakeEmbedder{other: []float32{1, 0, 0}}}
	a, tracer := recallTestAgent(store, &scriptedMemoryLLM{query: "pipeline status", rerank: "1"}, embedder, 2000)

	a.injectGraphMemoryContext(ctx, laterTurn(ctx, "status?"))
	a.injectGraphMemoryContext(ctx, laterTurn(ctx, "status now?"))

	spans := tracer.GetSpansByName("graph_memory.recall")
	require.Len(t, spans, 2)
	for _, s := range spans {
		assert.Equal(t, "no_candidates", s.Attributes["recall.outcome"])
		assert.NotContains(t, s.Attributes, "recall.vector_error")
	}
	assert.Equal(t, 1, embedder.calls, "the query is not embedded again once the store says it cannot use it")
}
