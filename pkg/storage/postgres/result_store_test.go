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
package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractRowsAndColumns_MapData_DeterministicColumnOrder(t *testing.T) {
	// Map iteration order is non-deterministic in Go, so we verify that
	// extractRowsAndColumns returns sorted columns every time.
	data := []map[string]interface{}{
		{"zebra": "z", "apple": "a", "mango": "m"},
		{"zebra": "z2", "apple": "a2", "mango": "m2"},
	}

	// Run multiple times to verify deterministic ordering
	for i := 0; i < 10; i++ {
		rows, columns, err := extractRowsAndColumns(data)
		require.NoError(t, err)
		assert.Equal(t, []string{"apple", "mango", "zebra"}, columns,
			"columns should be sorted alphabetically on iteration %d", i)
		assert.Len(t, rows, 2)
		// Verify row values align with sorted columns
		assert.Equal(t, "a", rows[0][0])
		assert.Equal(t, "m", rows[0][1])
		assert.Equal(t, "z", rows[0][2])
	}
}

func TestExtractRowsAndColumns_MapData_EmptySlice(t *testing.T) {
	data := []map[string]interface{}{}
	rows, columns, err := extractRowsAndColumns(data)
	require.NoError(t, err)
	assert.Nil(t, rows)
	assert.Nil(t, columns)
}

func TestExtractRowsAndColumns_SliceData(t *testing.T) {
	data := [][]interface{}{
		{"a", "b", "c"},
		{"d", "e", "f"},
	}

	rows, columns, err := extractRowsAndColumns(data)
	require.NoError(t, err)
	assert.Equal(t, []string{"col1", "col2", "col3"}, columns)
	assert.Len(t, rows, 2)
	assert.Equal(t, data, rows)
}

func TestExtractRowsAndColumns_SliceData_EmptySlice(t *testing.T) {
	data := [][]interface{}{}
	rows, columns, err := extractRowsAndColumns(data)
	require.NoError(t, err)
	assert.Nil(t, rows)
	assert.Nil(t, columns)
}

func TestExtractRowsAndColumns_UnsupportedType(t *testing.T) {
	_, _, err := extractRowsAndColumns("not a valid type")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported data type")
}

func TestValidTableNameRe(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		isValid bool
	}{
		{"simple alphanumeric", "tool_result_abc123", true},
		{"starts with letter", "abc", true},
		{"starts with underscore", "_abc", true},
		{"contains SQL injection semicolon", "tool_result_abc;DROP TABLE", false},
		{"contains space", "tool_result abc", false},
		{"contains dash", "tool-result-abc", false},
		{"contains quote", "tool_result_abc'", false},
		{"starts with number", "123abc", false},
		{"empty string", "", false},
		{"contains parentheses", "tool_result_()", false},
		{"contains double dash comment", "abc--comment", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validTableNameRe.MatchString(tt.input)
			assert.Equal(t, tt.isValid, result,
				"validTableNameRe.MatchString(%q) = %v, want %v", tt.input, result, tt.isValid)
		})
	}
}

func TestSanitizeTableName(t *testing.T) {
	// pgx.Identifier{name}.Sanitize() quotes the identifier
	result := sanitizeTableName("tool_result_abc")
	assert.Equal(t, `"tool_result_abc"`, result)

	// Special characters get quoted (not stripped), making them safe
	result = sanitizeTableName("my table")
	assert.Equal(t, `"my table"`, result)
}

func TestBuildCreateTableSQL(t *testing.T) {
	sql := buildCreateTableSQL(`"my_table"`, []string{"col_a", "col_b"})
	assert.Contains(t, sql, `CREATE TABLE IF NOT EXISTS "my_table"`)
	assert.Contains(t, sql, `"col_a" TEXT`)
	assert.Contains(t, sql, `"col_b" TEXT`)
}

func TestQuotedColumns(t *testing.T) {
	result := quotedColumns([]string{"name", "age", "email"})
	assert.Equal(t, []string{`"name"`, `"age"`, `"email"`}, result)
}

func TestEstimateSize(t *testing.T) {
	columns := []string{"a", "b"}
	rows := [][]interface{}{
		{"hello", "world"},
	}
	size := estimateSize(rows, columns)
	// 2 columns * 20 = 40, plus "hello"(5) + "world"(5) = 50
	assert.Equal(t, int64(50), size)
}

func TestEstimateSize_NilValues(t *testing.T) {
	columns := []string{"a"}
	rows := [][]interface{}{
		{nil},
	}
	size := estimateSize(rows, columns)
	// 1 column * 20 = 20, plus "<nil>"(5) = 25
	assert.Equal(t, int64(25), size)
}

func TestExtractRowsAndColumns_MapData_NilValues(t *testing.T) {
	// Verify that nil values in maps are preserved (not converted to strings)
	data := []map[string]interface{}{
		{"alpha": nil, "beta": "value"},
	}

	rows, columns, err := extractRowsAndColumns(data)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta"}, columns)
	assert.Nil(t, rows[0][0], "nil values should be preserved as nil")
	assert.Equal(t, "value", rows[0][1])
}
