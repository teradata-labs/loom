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
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/types"
)

func TestKeywordSearchQuery(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "content words survive, stopwords and punctuation drop",
			in:   "This task requires database session continuity. First call teradata_connect to obtain a session_handle!",
			want: "task requires database session continuity first call teradata_connect to obtain session_handle",
		},
		{
			name: "dedupe and lowercase",
			in:   "Volatile VOLATILE volatile tables",
			want: "volatile tables",
		},
		{
			name: "stopword-only input yields empty",
			in:   "the and for with",
			want: "",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			// Aligned with fts5Barewords: the store keeps two-rune terms, so
			// dropping them here would discard discriminators the store would
			// have matched.
			name: "two-character terms survive",
			in:   "did the v2 AI run on S3?",
			want: "did v2 ai run on s3",
		},
		{
			// Restricting to ASCII would empty this query outright and disable
			// recall for the whole conversation.
			name: "non-latin message still yields a query",
			in:   "Что случилось с машиной в марте?",
			want: "что случилось с машиной в марте",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, keywordSearchQuery(tt.in))
		})
	}
}

func TestKeywordSearchQueryCap(t *testing.T) {
	long := strings.Repeat("alpha beta gamma delta epsilon zeta eta theta iota kappa lambda omicron sigma tau ", 3)
	got := keywordSearchQuery(long)
	assert.LessOrEqual(t, len(strings.Fields(got)), 12, "query must cap at twelve words")
	assert.NotEmpty(t, got)
}

// Terms that discriminate between remembered events must not be filtered out
// as filler. Temporal and interrogative words are the whole point of queries
// like "what was the FIRST issue after the service".
func TestSearchQueryStopwordsKeepDiscriminators(t *testing.T) {
	for _, w := range []string{"first", "when", "what", "which", "who", "why", "call", "only"} {
		assert.False(t, searchQueryStopwords[w], "%q discriminates; it must not be a stopword", w)
	}
	// Sanity: genuine filler is still dropped.
	for _, w := range []string{"the", "and", "with", "please", "should"} {
		assert.True(t, searchQueryStopwords[w], "%q is filler; it should stay a stopword", w)
	}
}

// searchQueryLLM is a stub provider for the recall query side-call.
type searchQueryLLM struct {
	content string
	err     error
	calls   int
}

func (s *searchQueryLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &llmtypes.LLMResponse{Content: s.content, StopReason: "end_turn"}, nil
}

func (s *searchQueryLLM) Name() string  { return "search-query-stub" }
func (s *searchQueryLLM) Model() string { return "stub" }

// The recall query side-call is an extra LLM round trip with a 10s timeout.
// Under fleet load it starves (az512h: all 512 agents), and when it does,
// recall must keep working off a keyword query rather than going dark — and
// the span must say which of the two produced the query.
func TestExtractSearchQueryFallbackAndProvenance(t *testing.T) {
	const userMessage = "What was the first issue I had with my new car after its first service?"

	tests := []struct {
		name       string
		llm        *searchQueryLLM
		wantQuery  string
		wantSource string
	}{
		{
			name:       "side-call answers",
			llm:        &searchQueryLLM{content: "first issue with new car after first service"},
			wantQuery:  "first issue with new car after first service",
			wantSource: "llm",
		},
		{
			name:       "side-call errors, keyword fallback",
			llm:        &searchQueryLLM{err: errors.New("rate limited")},
			wantQuery:  keywordSearchQuery(userMessage),
			wantSource: "keyword",
		},
		{
			name:       "side-call times out, keyword fallback",
			llm:        &searchQueryLLM{err: context.DeadlineExceeded},
			wantQuery:  keywordSearchQuery(userMessage),
			wantSource: "keyword",
		},
		{
			name:       "side-call returns empty, keyword fallback",
			llm:        &searchQueryLLM{content: "   \n  "},
			wantQuery:  keywordSearchQuery(userMessage),
			wantSource: "keyword",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Agent{llm: tt.llm}
			query, source := a.extractSearchQuery(context.Background(), userMessage)

			assert.Equal(t, tt.wantSource, source, "span attribute recall.query_source")
			assert.Equal(t, tt.wantQuery, query)
			assert.Equal(t, 1, tt.llm.calls)

			// Usable means: non-empty, and made of terms the store's FTS5
			// sanitizer will keep — no punctuation to strip away to nothing.
			require.NotEmpty(t, query, "recall must not go dark when the side-call fails")
			for _, f := range strings.Fields(query) {
				assert.NotContains(t, f, "?", "fallback terms must be bare words")
				assert.NotContains(t, f, ",", "fallback terms must be bare words")
			}
			if source == "keyword" {
				assert.Contains(t, strings.Fields(query), "first",
					"the discriminating term must survive the fallback")
				assert.Contains(t, strings.Fields(query), "car")
			}
		})
	}
}

// A store failure must be distinguishable from an honest miss. Both used to
// present as recall.outcome=no_candidates with no error recorded, which is why
// az512h could not be diagnosed from its traces at all.
func TestInjectGraphMemoryContextRecordsStoreErrors(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "recall.db")+"?_fk=1&_journal_mode=WAL&_busy_timeout=5000")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	migrator, err := sqlite.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(context.Background()))

	tracer := observability.NewMockTracer()
	a := &Agent{
		llm:               &searchQueryLLM{content: "volatile table overflow"},
		graphMemoryStore:  sqlite.NewGraphMemoryStore(db, &mockTC{}, observability.NewNoOpTracer()),
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, MaxContextTokens: 2000},
		config:            &Config{Name: "recall-agent"},
		tracer:            tracer,
	}

	session := &types.Session{}
	session.AddMessage(context.Background(), types.Message{Role: "user", Content: "what broke in the volatile table?"})

	// Closing the database makes every store call fail. Zero candidates now
	// means "the store is broken", not "nothing matched".
	require.NoError(t, db.Close())

	a.injectGraphMemoryContext(context.Background(), session)

	spans := tracer.GetSpansByName("graph_memory.recall")
	require.Len(t, spans, 1)
	span := spans[0]

	assert.Equal(t, "store_error", span.Attributes["recall.outcome"],
		"a store failure must not be reported as no_candidates")
	assert.Equal(t, true, span.Attributes["recall.store_error"])
	assert.Equal(t, observability.StatusError, span.Status.Code, "the error must be recorded on the span")
	assert.NotEmpty(t, span.Attributes[observability.AttrErrorMessage])
	assert.Equal(t, "llm", span.Attributes["recall.query_source"])

	// Nothing was injected, so the session is untouched apart from the user turn.
	assert.Len(t, session.GetMessages(), 1)
}

// The query preview on the span is truncated like every other preview
// attribute in this file, so a long recall query cannot bloat trace payloads.
func TestInjectGraphMemoryContextTruncatesQueryAttribute(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	longQuery := strings.Repeat("volatile ", 400)

	tracer := observability.NewMockTracer()
	a := &Agent{
		llm:               &searchQueryLLM{content: longQuery},
		graphMemoryStore:  store,
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, MaxContextTokens: 2000},
		config:            &Config{Name: "recall-agent"},
		tracer:            tracer,
	}

	session := &types.Session{}
	session.AddMessage(context.Background(), types.Message{Role: "user", Content: "what broke?"})

	a.injectGraphMemoryContext(context.Background(), session)

	spans := tracer.GetSpansByName("graph_memory.recall")
	require.Len(t, spans, 1)
	recorded, ok := spans[0].Attributes["recall.query"].(string)
	require.True(t, ok, "recall.query must be recorded")
	assert.LessOrEqual(t, len([]rune(recorded)), maxPreviewLen+1, "recall.query must be truncated")
	assert.Less(t, len(recorded), len(longQuery))
}
