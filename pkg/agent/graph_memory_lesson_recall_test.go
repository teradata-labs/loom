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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// F3 + F8: a stored lesson reaches the first request even when no ordinary
// memory matches, and the recall span counts it. Before the fix the recall
// returned at "no_candidates" before the lesson lane was ever queried.
func TestConversationStartLessonWithoutOrdinaryMemories(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	tracer := observability.NewMockTracer()
	a := &Agent{
		llm:               &searchQueryLLM{content: "numeric overflow INTEGER column"},
		graphMemoryStore:  store,
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, MaxContextTokens: 2000},
		config:            &Config{Name: "runner-start"},
		tracer:            tracer,
	}
	_, err := store.Remember(ctx, &memory.Memory{
		AgentID:    a.lessonPartition(),
		Content:    "numeric overflow inserting into an INTEGER column — declare the column BIGINT",
		MemoryType: memory.MemoryTypeLesson,
		Source:     "lesson_mined",
		Salience:   0.9,
	})
	require.NoError(t, err)

	session := &types.Session{ID: "s-start"}
	session.AddMessage(ctx, types.Message{Role: "user", Content: "why does my INTEGER column overflow?"})
	note := a.graphMemoryNote(ctx, session)

	require.NotEmpty(t, note, "the lesson must be injected with no ordinary memory present")
	assert.Len(t, session.GetMessages(), 1, "the recall note is never stored in the session")
	assert.Contains(t, note, "Verified lessons from prior work (each fix was observed to succeed):")
	assert.Contains(t, note, "BIGINT")
	assert.NotContains(t, note, "Relevant memories from past conversations")

	spans := tracer.GetSpansByName("graph_memory.recall")
	require.Len(t, spans, 1)
	assertSpanAttrs(t, spans[0], map[string]interface{}{
		"recall.outcome":           "injected",
		"recall.candidates":        0,
		"recall.injected":          1,
		"recall.injected_memories": 0,
		"recall.injected_lessons":  1,
	})
}

// F8: with both kinds present, recall.injected is the total and the two
// breakdown attributes split it.
func TestRecallSpanCountsLessonsAndMemories(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	tracer := observability.NewMockTracer()
	a := &Agent{
		// One LLM answers both the search-query side-call and the rerank; a
		// rerank reply it cannot parse keeps every candidate.
		llm:               &searchQueryLLM{content: "numeric overflow INTEGER column"},
		graphMemoryStore:  store,
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, MaxContextTokens: 2000},
		config:            &Config{Name: "runner-both"},
		tracer:            tracer,
	}
	_, err := store.Remember(ctx, &memory.Memory{
		AgentID: a.graphMemoryKey(), Content: "the user's INTEGER column holds card numbers that overflow",
		MemoryType: memory.MemoryTypeFact, Salience: 0.9,
	})
	require.NoError(t, err)
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID: a.lessonPartition(), Content: "numeric overflow inserting into an INTEGER column — declare the column BIGINT",
		MemoryType: memory.MemoryTypeLesson, Source: "lesson_mined", Salience: 0.9,
	})
	require.NoError(t, err)

	session := &types.Session{ID: "s-both"}
	session.AddMessage(ctx, types.Message{Role: "user", Content: "why does my INTEGER column overflow?"})
	a.graphMemoryNote(ctx, session)

	spans := tracer.GetSpansByName("graph_memory.recall")
	require.Len(t, spans, 1)
	span := spans[0]
	require.Equal(t, "injected", span.Attributes["recall.outcome"])
	mems, _ := span.Attributes["recall.injected_memories"].(int)
	lessons, _ := span.Attributes["recall.injected_lessons"].(int)
	assert.Equal(t, 1, lessons)
	assert.GreaterOrEqual(t, mems, 1)
	assert.Equal(t, mems+lessons, span.Attributes["recall.injected"])
}
