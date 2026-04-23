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
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/types"
)

// newTestGraphMemoryStore creates a real SQLite-backed graph memory store for tests.
func newTestGraphMemoryStore(t *testing.T) memory.GraphMemoryStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL&_busy_timeout=5000")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	migrator, err := sqlite.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(context.Background()))

	return sqlite.NewGraphMemoryStore(db, &mockTC{}, observability.NewNoOpTracer())
}

// extractionMockLLM returns a fixed JSON response for extraction tests.
type extractionMockLLM struct {
	mu       sync.Mutex
	response string
	calls    int
}

func (m *extractionMockLLM) Chat(_ context.Context, _ []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return &types.LLMResponse{
		Content:   m.response,
		ToolCalls: []types.ToolCall{},
		Usage:     types.Usage{InputTokens: 100, OutputTokens: 50, CostUSD: 0.001},
	}, nil
}

func (m *extractionMockLLM) Name() string  { return "mock-extraction" }
func (m *extractionMockLLM) Model() string { return "mock-v1" }

func (m *extractionMockLLM) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// --- Tests ---

func TestBuildGraphMemoryExtractionPrompt(t *testing.T) {
	messages := []types.Message{
		{Role: "user", Content: "Tell me about the users table"},
		{Role: "assistant", Content: "I'll query the schema for the users table."},
		{Role: "tool", Content: `{"columns": ["id", "name", "email"]}`, ToolCalls: []types.ToolCall{{Name: "sql_query"}}},
	}

	prompt := buildGraphMemoryExtractionPrompt(messages, 10, nil, nil)

	assert.Contains(t, prompt, "[user]")
	assert.Contains(t, prompt, "[assistant]")
	assert.Contains(t, prompt, "users table")
	assert.Contains(t, prompt, "up to 10 entities")
	assert.Contains(t, prompt, `"entities"`)
	assert.Contains(t, prompt, `"relationships"`)
	assert.Contains(t, prompt, `"memories"`)
	assert.Contains(t, prompt, `"is_user"`)
	assert.Contains(t, prompt, `"role": "about|mentions"`)
}

func TestBuildGraphMemoryExtractionPrompt_WithContext(t *testing.T) {
	messages := []types.Message{
		{Role: "user", Content: "I cleaned my white Adidas sneakers yesterday"},
	}

	l2Summaries := []string{
		"User discussed their car purchase in February and bike repairs",
		"User talked about attending workshops and webinars in March",
	}
	existingEntities := []string{"user", "honda_civic", "hybrid_bike", "adidas_sneakers"}

	prompt := buildGraphMemoryExtractionPrompt(messages, 10, l2Summaries, existingEntities)

	// L2 summaries should appear in prompt
	assert.Contains(t, prompt, "Previous conversation context")
	assert.Contains(t, prompt, "car purchase in February")
	assert.Contains(t, prompt, "workshops and webinars")

	// Existing entities should appear in prompt
	assert.Contains(t, prompt, "Existing entities in the knowledge graph")
	assert.Contains(t, prompt, "honda_civic")
	assert.Contains(t, prompt, "adidas_sneakers")

	// Conversation content still present
	assert.Contains(t, prompt, "white Adidas sneakers")
}

func TestBuildGraphMemoryExtractionPrompt_NoContext(t *testing.T) {
	messages := []types.Message{
		{Role: "user", Content: "hello"},
	}

	prompt := buildGraphMemoryExtractionPrompt(messages, 10, nil, nil)

	// Should not contain context sections when nil
	assert.NotContains(t, prompt, "Previous conversation context")
	assert.NotContains(t, prompt, "Existing entities in the knowledge graph")
}

func TestBuildGraphMemoryExtractionPrompt_EmptyContext(t *testing.T) {
	messages := []types.Message{
		{Role: "user", Content: "hello"},
	}

	prompt := buildGraphMemoryExtractionPrompt(messages, 10, []string{}, []string{})

	// Should not contain context sections when empty
	assert.NotContains(t, prompt, "Previous conversation context")
	assert.NotContains(t, prompt, "Existing entities in the knowledge graph")
}

func TestBuildGraphMemoryExtractionPrompt_FullContent(t *testing.T) {
	longContent := strings.Repeat("x", 1000)
	messages := []types.Message{
		{Role: "user", Content: longContent},
	}

	prompt := buildGraphMemoryExtractionPrompt(messages, 5, nil, nil)

	// Full content should be preserved (no truncation)
	assert.Contains(t, prompt, longContent)
}

func TestExtractGraphMemoryAsync_Disabled(t *testing.T) {
	mockLLM := &extractionMockLLM{response: "{}"}
	store := newTestGraphMemoryStore(t)

	a := &Agent{
		llm:                         mockLLM,
		graphMemoryStore:            store,
		enableGraphMemoryExtraction: false, // disabled
		config:                      &Config{Name: "test-agent"},
	}

	a.extractGraphMemoryAsync(context.Background(), "test-session")

	// LLM should not have been called
	assert.Equal(t, 0, mockLLM.getCalls())
}

func TestExtractGraphMemoryAsync_NoStore(t *testing.T) {
	mockLLM := &extractionMockLLM{response: "{}"}

	a := &Agent{
		llm:                         mockLLM,
		graphMemoryStore:            nil, // no store
		enableGraphMemoryExtraction: true,
		config:                      &Config{Name: "test-agent"},
	}

	a.extractGraphMemoryAsync(context.Background(), "test-session")

	assert.Equal(t, 0, mockLLM.getCalls())
}

func TestExtractGraphMemoryAsync_ParseJSON(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	extractedData := ExtractedGraphData{
		Entities: []ExtractedEntity{
			{Name: "users_table", EntityType: "dataset"},
			{Name: "email_column", EntityType: "concept"},
		},
		Relationships: []ExtractedRelationship{
			{Source: "users_table", Target: "email_column", Relation: "CONTAINS"},
		},
		Memories: []ExtractedMemory{
			{
				Content:    "The users table has columns id, name, and email",
				Summary:    "Users table schema",
				MemoryType: "fact",
				Tags:       []string{"schema"},
				Salience:   0.7,
				Entities:   []ExtractedEntityRole{{Name: "users_table", Role: "about"}},
			},
		},
	}
	responseJSON, err := json.Marshal(extractedData)
	require.NoError(t, err)

	mockLLM := &extractionMockLLM{response: string(responseJSON)}

	// Set up agent with memory manager that has the session.
	mem := NewMemory()
	session := mem.GetOrCreateSession(ctx, "test-session")

	// Configure segmented memory on the session.
	segMem := NewSegmentedMemory("", 200000, 20000)
	segMem.AddMessage(ctx, types.Message{Role: "user", Content: "Tell me about the users table"})
	segMem.AddMessage(ctx, types.Message{Role: "assistant", Content: "I'll query the schema."})
	segMem.AddMessage(ctx, types.Message{Role: "tool", Content: `{"columns": ["id", "name", "email"]}`})
	session.SegmentedMem = segMem

	a := &Agent{
		llm:                         mockLLM,
		graphMemoryStore:            store,
		enableGraphMemoryExtraction: true,
		graphExtractionCadence:      5,
		graphMemoryConfig: &loomv1.GraphMemoryConfig{
			Enabled:                  true,
			EnableExtraction:         true,
			MaxEntitiesPerExtraction: 10,
		},
		memory: mem,
		config: &Config{Name: "test-agent"},
	}

	a.extractGraphMemoryAsync(ctx, "test-session")

	// Verify LLM was called (single-pass extraction).
	assert.Equal(t, 1, mockLLM.getCalls())

	// Verify entities were created.
	entity, err := store.GetEntity(ctx, "test-agent", "users_table")
	require.NoError(t, err)
	assert.Equal(t, "dataset", entity.EntityType)

	entity2, err := store.GetEntity(ctx, "test-agent", "email_column")
	require.NoError(t, err)
	assert.Equal(t, "concept", entity2.EntityType)

	// Verify relationship was created.
	edges, err := store.ListEdgesFrom(ctx, entity.ID)
	require.NoError(t, err)
	assert.Len(t, edges, 1)
	assert.Equal(t, "CONTAINS", edges[0].Relation)

	// Verify memory was stored.
	memories, err := store.Recall(ctx, memory.RecallOpts{
		AgentID: "test-agent",
		Query:   "users table",
		Limit:   10,
	})
	require.NoError(t, err)
	// Two passes with same mock response produce 2 copies of the same memory.
	require.GreaterOrEqual(t, len(memories), 1)
	assert.Equal(t, "The users table has columns id, name, and email", memories[0].Content)
	assert.Equal(t, "auto_extracted", memories[0].Source)
	assert.Equal(t, "fact", memories[0].MemoryType)
}

func TestExtractGraphMemoryAsync_MalformedJSON(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	mockLLM := &extractionMockLLM{response: "this is not json"}

	mem := NewMemory()
	session := mem.GetOrCreateSession(ctx, "test-session")

	segMem := NewSegmentedMemory("", 200000, 20000)
	segMem.AddMessage(ctx, types.Message{Role: "user", Content: "hello"})
	session.SegmentedMem = segMem

	a := &Agent{
		llm:                         mockLLM,
		graphMemoryStore:            store,
		enableGraphMemoryExtraction: true,
		graphExtractionCadence:      5,
		graphMemoryConfig: &loomv1.GraphMemoryConfig{
			Enabled:          true,
			EnableExtraction: true,
		},
		memory: mem,
		config: &Config{Name: "test-agent"},
	}

	// Should not panic, just silently fail.
	a.extractGraphMemoryAsync(ctx, "test-session")

	assert.Equal(t, 1, mockLLM.getCalls())

	// Verify nothing was stored (both passes fail to parse).
	stats, err := store.GetStats(ctx, "test-agent")
	require.NoError(t, err)
	assert.Equal(t, 0, stats.EntityCount)
	assert.Equal(t, 0, stats.MemoryCount)
}

func TestExtractGraphMemoryAsync_Deduplication(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// Pre-create an entity.
	_, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID:    "test-agent",
		Name:       "existing_entity",
		EntityType: "project",
	})
	require.NoError(t, err)

	// Extraction includes the same entity.
	extractedData := ExtractedGraphData{
		Entities: []ExtractedEntity{
			{Name: "existing_entity", EntityType: "concept"}, // different type, same name
			{Name: "new_entity", EntityType: "tool"},
		},
	}
	responseJSON, err := json.Marshal(extractedData)
	require.NoError(t, err)

	mockLLM := &extractionMockLLM{response: string(responseJSON)}

	mem := NewMemory()
	session := mem.GetOrCreateSession(ctx, "test-session")

	segMem := NewSegmentedMemory("", 200000, 20000)
	segMem.AddMessage(ctx, types.Message{Role: "user", Content: "working with existing_entity"})
	session.SegmentedMem = segMem

	a := &Agent{
		llm:                         mockLLM,
		graphMemoryStore:            store,
		enableGraphMemoryExtraction: true,
		graphExtractionCadence:      5,
		graphMemoryConfig: &loomv1.GraphMemoryConfig{
			Enabled:          true,
			EnableExtraction: true,
		},
		memory: mem,
		config: &Config{Name: "test-agent"},
	}

	a.extractGraphMemoryAsync(ctx, "test-session")

	// Existing entity should still have original type (get-or-create does not update).
	entity, err := store.GetEntity(ctx, "test-agent", "existing_entity")
	require.NoError(t, err)
	assert.Equal(t, "project", entity.EntityType)

	// New entity should be created.
	entity2, err := store.GetEntity(ctx, "test-agent", "new_entity")
	require.NoError(t, err)
	assert.Equal(t, "tool", entity2.EntityType)
}

func TestGetRecentConversationTurns(t *testing.T) {
	ctx := context.Background()
	segMem := NewSegmentedMemory("", 200000, 20000)

	// Add 5 messages.
	segMem.AddMessage(ctx, types.Message{Role: "user", Content: "msg1"})
	segMem.AddMessage(ctx, types.Message{Role: "assistant", Content: "msg2"})
	segMem.AddMessage(ctx, types.Message{Role: "tool", Content: "msg3"})
	segMem.AddMessage(ctx, types.Message{Role: "user", Content: "msg4"})
	segMem.AddMessage(ctx, types.Message{Role: "assistant", Content: "msg5"})

	t.Run("get last 3", func(t *testing.T) {
		msgs := segMem.GetRecentConversationTurns(3)
		require.Len(t, msgs, 3)
		assert.Equal(t, "msg3", msgs[0].Content)
		assert.Equal(t, "msg4", msgs[1].Content)
		assert.Equal(t, "msg5", msgs[2].Content)
	})

	t.Run("get more than available", func(t *testing.T) {
		msgs := segMem.GetRecentConversationTurns(100)
		assert.Len(t, msgs, 5)
	})

	t.Run("get zero", func(t *testing.T) {
		msgs := segMem.GetRecentConversationTurns(0)
		assert.Nil(t, msgs)
	})

	t.Run("get negative", func(t *testing.T) {
		msgs := segMem.GetRecentConversationTurns(-1)
		assert.Nil(t, msgs)
	})
}

func TestNormalizeEntityName(t *testing.T) {
	assert.Equal(t, "john smith", normalizeEntityName("  John Smith  "))
	assert.Equal(t, "users_table", normalizeEntityName("users_table"))
	assert.Equal(t, "", normalizeEntityName("  "))
}

func TestIsValidMemoryType(t *testing.T) {
	assert.True(t, isValidMemoryType("fact"))
	assert.True(t, isValidMemoryType("preference"))
	assert.True(t, isValidMemoryType("decision"))
	assert.True(t, isValidMemoryType("experience"))
	assert.True(t, isValidMemoryType("failure"))
	assert.True(t, isValidMemoryType("observation"))
	assert.True(t, isValidMemoryType("consolidation"))
	assert.False(t, isValidMemoryType("note"))
	assert.False(t, isValidMemoryType(""))
}

func TestExtractGraphMemoryAsync_EntityRolesAndUserMarker(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	extractedData := ExtractedGraphData{
		Entities: []ExtractedEntity{
			{Name: "ilsun", EntityType: "person", IsUser: true},
			{Name: "marcus", EntityType: "person", IsUser: false},
			{Name: "cc_transactions", EntityType: "dataset"},
		},
		Relationships: []ExtractedRelationship{
			{Source: "ilsun", Target: "cc_transactions", Relation: "WORKS_ON"},
		},
		Memories: []ExtractedMemory{
			{
				Content:    "Ilsun is analyzing cc_transactions for fraud patterns",
				Summary:    "Ilsun analyzing fraud",
				MemoryType: "fact",
				Tags:       []string{"fraud"},
				Salience:   0.8,
				Entities: []ExtractedEntityRole{
					{Name: "ilsun", Role: "about"},
					{Name: "cc_transactions", Role: "mentions"},
				},
			},
			{
				Content:    "Marcus focuses on fraud detection for transactions over 500 dollars",
				Summary:    "Marcus fraud focus",
				MemoryType: "fact",
				Salience:   0.7,
				Entities: []ExtractedEntityRole{
					{Name: "marcus", Role: "about"},
				},
			},
		},
	}
	responseJSON, err := json.Marshal(extractedData)
	require.NoError(t, err)

	mockLLM := &extractionMockLLM{response: string(responseJSON)}
	mem := NewMemory()
	session := mem.GetOrCreateSession(ctx, "test-session")
	segMem := NewSegmentedMemory("", 200000, 20000)
	segMem.AddMessage(ctx, types.Message{Role: "user", Content: "I am Ilsun. My colleague Marcus focuses on fraud."})
	segMem.AddMessage(ctx, types.Message{Role: "assistant", Content: "Got it."})
	session.SegmentedMem = segMem

	a := &Agent{
		llm:                         mockLLM,
		graphMemoryStore:            store,
		enableGraphMemoryExtraction: true,
		graphExtractionCadence:      5,
		graphMemoryConfig: &loomv1.GraphMemoryConfig{
			Enabled:                  true,
			EnableExtraction:         true,
			MaxEntitiesPerExtraction: 10,
		},
		memory: mem,
		config: &Config{Name: "test-agent"},
	}

	a.extractGraphMemoryAsync(ctx, "test-session")

	// Verify user marker: ilsun should have is_user property.
	ilsun, err := store.GetEntity(ctx, "test-agent", "ilsun")
	require.NoError(t, err)
	assert.Equal(t, "person", ilsun.EntityType)
	assert.Contains(t, ilsun.PropertiesJSON, `"is_user":true`)

	// Verify marcus does NOT have is_user property.
	marcus, err := store.GetEntity(ctx, "test-agent", "marcus")
	require.NoError(t, err)
	assert.Equal(t, "person", marcus.EntityType)
	assert.NotContains(t, marcus.PropertiesJSON, `"is_user":true`)

	// Verify dataset entity has no is_user.
	ccTx, err := store.GetEntity(ctx, "test-agent", "cc_transactions")
	require.NoError(t, err)
	assert.Equal(t, "dataset", ccTx.EntityType)

	// Verify memories were stored with entity roles.
	memories, err := store.Recall(ctx, memory.RecallOpts{
		AgentID: "test-agent",
		Query:   "fraud",
		Limit:   10,
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(memories), 2)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hello", truncate("hello", 10))
	assert.Equal(t, "hel...", truncate("hello world", 3))
}

func TestSanitizeEventDate(t *testing.T) {
	tests := []struct {
		name           string
		inDate         string
		inConfidence   string
		wantDate       string
		wantConfidence string
	}{
		{
			name:           "empty passes through",
			inDate:         "",
			inConfidence:   "",
			wantDate:       "",
			wantConfidence: "",
		},
		{
			name:           "valid ISO date with exact confidence",
			inDate:         "2023-03-14",
			inConfidence:   "exact",
			wantDate:       "2023-03-14",
			wantConfidence: "exact",
		},
		{
			name:           "valid ISO date with approximate confidence",
			inDate:         "2023-04-20",
			inConfidence:   "approximate",
			wantDate:       "2023-04-20",
			wantConfidence: "approximate",
		},
		{
			name:           "date with missing confidence defaults to approximate",
			inDate:         "2023-04-20",
			inConfidence:   "",
			wantDate:       "2023-04-20",
			wantConfidence: "approximate",
		},
		{
			name:           "ambiguous with empty date is preserved",
			inDate:         "",
			inConfidence:   "ambiguous",
			wantDate:       "",
			wantConfidence: "ambiguous",
		},
		{
			name:           "ambiguous with a date drops the date (protocol violation)",
			inDate:         "2023-04-20",
			inConfidence:   "ambiguous",
			wantDate:       "",
			wantConfidence: "ambiguous",
		},
		{
			name:           "malformed date is dropped",
			inDate:         "April 20, 2023",
			inConfidence:   "approximate",
			wantDate:       "",
			wantConfidence: "approximate",
		},
		{
			name:           "unknown confidence is normalized to default",
			inDate:         "2023-01-01",
			inConfidence:   "very-sure",
			wantDate:       "2023-01-01",
			wantConfidence: "approximate",
		},
		{
			name:           "whitespace-only date is treated as empty",
			inDate:         "   ",
			inConfidence:   "exact",
			wantDate:       "",
			wantConfidence: "exact",
		},
		{
			name:           "mixed-case confidence is normalized",
			inDate:         "2023-05-14",
			inConfidence:   "EXACT",
			wantDate:       "2023-05-14",
			wantConfidence: "exact",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDate, gotConfidence := sanitizeEventDate(tt.inDate, tt.inConfidence)
			assert.Equal(t, tt.wantDate, gotDate, "date")
			assert.Equal(t, tt.wantConfidence, gotConfidence, "confidence")
		})
	}
}

func TestJSONSchema_ContainsEventDateFields(t *testing.T) {
	// Guard against the prompt schema drifting out of sync with the struct:
	// the LLM needs these keys in the schema for the extraction rule to work.
	assert.Contains(t, jsonSchema, "event_date")
	assert.Contains(t, jsonSchema, "event_date_confidence")
}

func TestExtractedMemory_UnmarshalsEventDateFields(t *testing.T) {
	raw := `{
		"content": "started watching The Crown",
		"summary": "watching Crown",
		"memory_type": "experience",
		"tags": [],
		"salience": 0.5,
		"entities": [],
		"event_date": "2023-03-14",
		"event_date_confidence": "approximate"
	}`
	var m ExtractedMemory
	err := json.Unmarshal([]byte(raw), &m)
	require.NoError(t, err)
	assert.Equal(t, "2023-03-14", m.EventDate)
	assert.Equal(t, "approximate", m.EventDateConfidence)
}
