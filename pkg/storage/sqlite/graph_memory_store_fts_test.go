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

package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/memory"
)

// Free text with FTS5-hostile punctuation must become a syntax-safe bareword
// OR-query — a raw apostrophe/comma/'?' in MATCH is a parse error the recall
// callers swallow as zero results (the az512h fleet measured recall silently
// dead because of exactly this class of failure).
func TestToFTS5OrQuerySanitizes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain words", "volatile table overflow", "volatile OR table OR overflow"},
		{"punctuation stripped", "card_id: overflow? (DECIMAL, 14,2)", "card_id OR overflow OR decimal OR 14"},
		{"apostrophe split", "user's session won't persist", "user OR session OR won OR persist"},
		{"single word", "overflow", "overflow"},
		{"empty", "", ""},
		{"punctuation only", "?!,.;", ""},
		// No pass-through: operator words in prose are tokenized like any
		// other word, because the input is always free text.
		{"operator words are tokenized, never passed through", `"volatile table" OR overflow`, "volatile OR table OR or OR overflow"},
		{"unbalanced quote is sanitized, not passed", `overflow" OR table`, "overflow OR or OR table"},
		{"prose 'and' does not trigger a pass-through", "Coach's AND player's stats", "coach OR and OR player OR stats"},
		// Codepoints above 127 are FTS5 barewords and this index is
		// `porter unicode61`; dropping them empties the query entirely.
		{"cyrillic survives", "Привет мир Москва", "привет OR мир OR москва"},
		{"accented latin is not truncated", "café naïve", "café OR naïve"},
		{"mixed script", "北京 tables", "北京 OR tables"},
		{"lone cjk ideograph is a whole word", "京", "京"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toFTS5OrQuery(tt.in))
		})
	}
}

// matchCount runs an FTS5 MATCH and surfaces the parse error. Errors on a
// virtual-table MATCH arrive from rows.Err(), not from QueryContext, which is
// precisely why a malformed query looks like "zero results" to a careless
// caller.
func matchCount(t *testing.T, db *sql.DB, table, expr string) (int, error) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT COUNT(*) FROM "+table+" WHERE "+table+" MATCH ?", expr)
	if err != nil {
		return 0, err
	}
	defer rows.Close() //nolint:errcheck
	n := 0
	for rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

// The test that matters: string equality never caught this. Every query
// toFTS5OrQuery produces is EXECUTED against a real migrated
// `porter unicode61` FTS5 table and must not be a syntax error.
func TestToFTS5OrQueryExecutesWithoutSyntaxError(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	_, err := store.Remember(ctx, &memory.Memory{
		AgentID:    "agent-fts",
		Content:    "Coach's player stats, the first issue was a GPS malfunction in March; volatile table overflow (DECIMAL). Привет мир Москва café 北京",
		MemoryType: "fact",
		Salience:   0.9,
	})
	require.NoError(t, err)

	// Every one of these is a natural-language string an LLM or a user can
	// produce. All four ASCII cases were verified to error before the
	// pass-through was removed.
	inputs := []string{
		"Coach's AND player's stats",
		"first issue NOT resolved yet?",
		"car service AND GPS malfunction, March",
		"volatile table OR overflow (DECIMAL)",
		`he said "it broke" NEAR the door`,
		"what's the ETA -- 3:45pm?",
		"Привет мир Москва",
		"Πού είναι το αυτοκίνητο;",
		"北京 tables 北京",
		"café naïve résumé",
		"Coach’s stats — 北京, café?",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			q := toFTS5OrQuery(in)
			require.NotEmpty(t, q, "these inputs all contain usable terms")
			for _, table := range []string{"graph_memories_fts", "graph_entities_fts"} {
				_, err := matchCount(t, store.db, table, q)
				assert.NoError(t, err, "MATCH %q on %s must not be a syntax error", q, table)
			}
		})
	}
}

// An empty MATCH expression is itself a hard FTS5 syntax error, so an empty
// sanitizer result must never reach a MATCH.
func TestEmptyMatchExpressionIsAnFTS5Error(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	_, err := matchCount(t, store.db, "graph_memories_fts", "")
	require.Error(t, err, "an empty MATCH expression must error — this is why callers have to skip the FTS join")
}

// A/B on identical data: a non-Latin query returned its row before the
// sanitizer was introduced and must still return it.
func TestRecallNonLatinQueryReturnsRow(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	created, err := store.Remember(ctx, &memory.Memory{
		AgentID:    "agent-ru",
		Content:    "Привет мир Москва — встреча в марте",
		MemoryType: "fact",
		Salience:   0.9,
	})
	require.NoError(t, err)

	for _, q := range []string{"Привет мир Москва", "Москва", "москва?"} {
		t.Run(q, func(t *testing.T) {
			got, err := store.Recall(ctx, memory.RecallOpts{AgentID: "agent-ru", Query: q, Limit: 10})
			require.NoError(t, err)
			require.Len(t, got, 1, "non-Latin query must still recall its row")
			assert.Equal(t, created.ID, got[0].ID)
		})
	}
}

func TestSearchEntitiesNonLatinQuery(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	created, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "agent-ru", Name: "Москва", EntityType: "place",
	})
	require.NoError(t, err)

	got, err := store.SearchEntities(ctx, "agent-ru", "Москва", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, created.ID, got[0].ID)
}

// A query with no usable terms must fall back to the non-FTS branch rather
// than issuing an empty MATCH expression.
func TestRecallUnusableQueryFallsBackToNonFTS(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	created, err := store.Remember(ctx, &memory.Memory{
		AgentID: "agent-x", Content: "volatile table overflow", MemoryType: "fact", Salience: 0.9,
	})
	require.NoError(t, err)

	for _, q := range []string{"?!,.;", "!", "  ,  "} {
		t.Run(q, func(t *testing.T) {
			got, err := store.Recall(ctx, memory.RecallOpts{AgentID: "agent-x", Query: q, Limit: 10})
			require.NoError(t, err, "an unusable query must not become an empty MATCH expression")
			require.Len(t, got, 1, "recall degrades to the unfiltered salience-ordered path")
			assert.Equal(t, created.ID, got[0].ID)
		})
	}
}

// SearchEntities has no non-FTS search path (ListEntities is an unfiltered
// listing, not a search), so an unusable query honestly returns no matches —
// but it must not error.
func TestSearchEntitiesUnusableQueryReturnsNoMatches(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	_, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "agent-x", Name: "volatile-table", EntityType: "concept",
	})
	require.NoError(t, err)

	got, err := store.SearchEntities(ctx, "agent-x", "?!,.;", 10)
	require.NoError(t, err, "an unusable query must not become an empty MATCH expression")
	assert.Empty(t, got)
}
