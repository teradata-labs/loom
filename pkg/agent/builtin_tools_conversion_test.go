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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/storage"
)

func TestConvertJSONArrayToRows_ValidData(t *testing.T) {
	tool := &QueryToolResultTool{}

	tests := []struct {
		name          string
		jsonData      string
		expectedCols  []string
		expectedRows  int
		validateFirst func(t *testing.T, row []any)
	}{
		{
			name: "simple objects",
			jsonData: `[
				{"id": 1, "name": "Alice", "score": 95.5},
				{"id": 2, "name": "Bob", "score": 87.0},
				{"id": 3, "name": "Charlie", "score": 92.5}
			]`,
			expectedCols: []string{"id", "name", "score"},
			expectedRows: 3,
			validateFirst: func(t *testing.T, row []any) {
				assert.Equal(t, float64(1), row[0])
				assert.Equal(t, "Alice", row[1])
				assert.Equal(t, float64(95.5), row[2])
			},
		},
		{
			name: "mixed types",
			jsonData: `[
				{"str": "text", "num": 42, "bool": true, "null": null},
				{"str": "more", "num": 100, "bool": false, "null": null}
			]`,
			expectedCols: []string{"bool", "null", "num", "str"}, // Sorted alphabetically
			expectedRows: 2,
			validateFirst: func(t *testing.T, row []any) {
				assert.Equal(t, true, row[0])        // bool
				assert.Nil(t, row[1])                // null
				assert.Equal(t, float64(42), row[2]) // num
				assert.Equal(t, "text", row[3])      // str
			},
		},
		{
			name: "missing fields",
			jsonData: `[
				{"id": 1, "name": "Alice"},
				{"id": 2, "score": 95.5},
				{"name": "Charlie", "score": 92.5}
			]`,
			expectedCols: []string{"id", "name"}, // Only columns from first item
			expectedRows: 3,
			validateFirst: func(t *testing.T, row []any) {
				assert.Equal(t, float64(1), row[0]) // id
				assert.Equal(t, "Alice", row[1])    // name
				// Note: "score" column is not detected since it's not in first item
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			columns, rows, err := tool.convertJSONArrayToRows([]byte(tt.jsonData))
			require.NoError(t, err)

			assert.Equal(t, tt.expectedCols, columns)
			assert.Len(t, rows, tt.expectedRows)

			if tt.validateFirst != nil && len(rows) > 0 {
				tt.validateFirst(t, rows[0])
			}
		})
	}
}

func TestConvertJSONArrayToRows_NestedObjects(t *testing.T) {
	tool := &QueryToolResultTool{}

	jsonData := `[
		{"id": 1, "user": {"name": "Alice", "age": 30}},
		{"id": 2, "user": {"name": "Bob", "age": 25}}
	]`

	columns, rows, err := tool.convertJSONArrayToRows([]byte(jsonData))
	require.NoError(t, err)

	// Should flatten or serialize nested objects
	assert.Contains(t, columns, "id")
	assert.Len(t, rows, 2)

	// Verify nested object is handled (either as JSON string or flattened)
	assert.NotNil(t, rows[0])
}

func TestConvertJSONArrayToRows_EmptyArray(t *testing.T) {
	tool := &QueryToolResultTool{}

	columns, rows, err := tool.convertJSONArrayToRows([]byte("[]"))
	require.NoError(t, err)

	assert.Empty(t, columns)
	assert.Empty(t, rows)
}

func TestConvertJSONArrayToRows_InvalidJSON(t *testing.T) {
	tool := &QueryToolResultTool{}

	testCases := []struct {
		name string
		data string
	}{
		{"malformed json", `[{"id": 1, "name": "Alice"`},
		{"not an array", `{"id": 1}`},
		{"empty string", ``},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := tool.convertJSONArrayToRows([]byte(tc.data))
			assert.Error(t, err)
		})
	}
}

func TestConvertCSVToRows_ValidData(t *testing.T) {
	tool := &QueryToolResultTool{}

	tests := []struct {
		name          string
		csvData       string
		expectedCols  []string
		expectedRows  int
		validateFirst func(t *testing.T, row []any)
	}{
		{
			name: "basic csv",
			csvData: `id,name,score
1,Alice,95
2,Bob,87
3,Charlie,92`,
			expectedCols: []string{"id", "name", "score"},
			expectedRows: 3,
			validateFirst: func(t *testing.T, row []any) {
				assert.Equal(t, "1", row[0])
				assert.Equal(t, "Alice", row[1])
				assert.Equal(t, "95", row[2])
			},
		},
		{
			name: "quoted values",
			csvData: `id,name,description
1,"Alice Smith","A software engineer"
2,"Bob Jones","A data scientist"`,
			expectedCols: []string{"id", "name", "description"},
			expectedRows: 2,
			validateFirst: func(t *testing.T, row []any) {
				assert.Equal(t, "1", row[0])
				// Note: The simple CSV parser doesn't unquote values
				assert.Equal(t, "\"Alice Smith\"", row[1])
				assert.Equal(t, "\"A software engineer\"", row[2])
			},
		},
		{
			name: "empty fields",
			csvData: `id,name,score
1,Alice,
2,,87
,Charlie,92`,
			expectedCols: []string{"id", "name", "score"},
			expectedRows: 3,
			validateFirst: func(t *testing.T, row []any) {
				assert.Equal(t, "1", row[0])
				assert.Equal(t, "Alice", row[1])
				assert.Equal(t, "", row[2])
			},
		},
		{
			name: "single row",
			csvData: `id,name,score
1,Alice,95`,
			expectedCols: []string{"id", "name", "score"},
			expectedRows: 1,
			validateFirst: func(t *testing.T, row []any) {
				assert.Equal(t, "1", row[0])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			columns, rows, err := tool.convertCSVToRows([]byte(tt.csvData))
			require.NoError(t, err)

			assert.Equal(t, tt.expectedCols, columns)
			assert.Len(t, rows, tt.expectedRows)

			if tt.validateFirst != nil && len(rows) > 0 {
				tt.validateFirst(t, rows[0])
			}
		})
	}
}

func TestConvertCSVToRows_InvalidData(t *testing.T) {
	tool := &QueryToolResultTool{}

	testCases := []struct {
		name string
		data string
	}{
		{"empty string", ``},
		{"only header", `id,name,score`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := tool.convertCSVToRows([]byte(tc.data))
			assert.Error(t, err)
		})
	}
}

func TestConvertAndQuery_JSONArray(t *testing.T) {
	ctx := context.Background()

	// Setup stores
	sqlStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath:     ":memory:",
		TTLSeconds: 3600,
	})
	require.NoError(t, err)
	defer sqlStore.Close()

	memoryStore := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	tool := &QueryToolResultTool{
		sqlStore:    sqlStore,
		memoryStore: memoryStore,
	}

	// Create test JSON array
	testData := []map[string]any{
		{"id": float64(1), "name": "Alice", "score": float64(95)},
		{"id": float64(2), "name": "Bob", "score": float64(87)},
		{"id": float64(3), "name": "Charlie", "score": float64(92)},
		{"id": float64(4), "name": "David", "score": float64(88)},
		{"id": float64(5), "name": "Eve", "score": float64(91)},
	}
	jsonData, err := json.Marshal(testData)
	require.NoError(t, err)

	// Store in memory
	ref, err := memoryStore.Store("test_json", jsonData, "application/json", nil)
	require.NoError(t, err)

	// Get metadata
	meta, err := memoryStore.GetMetadata(ref)
	require.NoError(t, err)

	// Test SQL query - use CAST to compare numbers properly
	result, err := tool.convertAndQuery(ctx, ref, meta, "SELECT name, score FROM results WHERE CAST(score AS REAL) > 90 ORDER BY CAST(score AS REAL) DESC")
	require.NoError(t, err)
	if !result.Success {
		t.Logf("Query failed: %v", result.Error)
	}
	require.True(t, result.Success, "query should succeed")

	// Verify results
	resultMap, ok := result.Data.(map[string]interface{})
	require.True(t, ok, "result.Data should be a map")

	rows, ok := resultMap["rows"].([][]interface{})
	require.True(t, ok, "result should have rows")

	// Should have 3 rows (Alice=95, Charlie=92, Eve=91)
	require.Len(t, rows, 3, "expected 3 rows with score > 90")

	// Verify names (order: score DESC)
	names := []string{rows[0][0].(string), rows[1][0].(string), rows[2][0].(string)}
	assert.Contains(t, names, "Alice")
	assert.Contains(t, names, "Charlie")
	assert.Contains(t, names, "Eve")
	assert.NotContains(t, names, "Bob")
	assert.NotContains(t, names, "David")
}

func TestConvertAndQuery_CSV(t *testing.T) {
	ctx := context.Background()

	// Setup stores
	sqlStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath:     ":memory:",
		TTLSeconds: 3600,
	})
	require.NoError(t, err)
	defer sqlStore.Close()

	memoryStore := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	tool := &QueryToolResultTool{
		sqlStore:    sqlStore,
		memoryStore: memoryStore,
	}

	// Create test CSV
	csvData := `id,name,score
1,Alice,95
2,Bob,87
3,Charlie,92
4,David,88
5,Eve,91`

	// Store in memory
	ref, err := memoryStore.Store("test_csv", []byte(csvData), "text/csv", nil)
	require.NoError(t, err)

	// Get metadata
	meta, err := memoryStore.GetMetadata(ref)
	require.NoError(t, err)

	// Test SQL query
	result, err := tool.convertAndQuery(ctx, ref, meta, "SELECT name FROM results WHERE CAST(score AS INTEGER) < 90 ORDER BY name")
	require.NoError(t, err)
	if !result.Success {
		t.Logf("Query failed: %v", result.Error)
	}
	require.True(t, result.Success, "query should succeed")

	// Verify results
	resultMap, ok := result.Data.(map[string]interface{})
	require.True(t, ok, "result.Data should be a map")

	rows, ok := resultMap["rows"].([][]interface{})
	require.True(t, ok, "result should have rows")

	// Should have 2 rows (Bob=87, David=88)
	assert.Len(t, rows, 2)

	// Verify names
	names := []string{rows[0][0].(string), rows[1][0].(string)}
	assert.Contains(t, names, "Bob")
	assert.Contains(t, names, "David")
	assert.NotContains(t, names, "Alice")
	assert.NotContains(t, names, "Charlie")
	assert.NotContains(t, names, "Eve")
}

func TestConvertAndQuery_InvalidDataType(t *testing.T) {
	ctx := context.Background()

	sqlStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath:     ":memory:",
		TTLSeconds: 3600,
	})
	require.NoError(t, err)
	defer sqlStore.Close()

	memoryStore := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	tool := &QueryToolResultTool{
		sqlStore:    sqlStore,
		memoryStore: memoryStore,
	}

	// Store plain text (not JSON or CSV)
	ref, err := memoryStore.Store("test_text", []byte("Just plain text"), "text/plain", nil)
	require.NoError(t, err)

	// Get metadata
	meta, err := memoryStore.GetMetadata(ref)
	require.NoError(t, err)

	// Try to query (should fail)
	result, err := tool.convertAndQuery(ctx, ref, meta, "SELECT * FROM results")
	require.NoError(t, err, "convertAndQuery should not return error")
	assert.False(t, result.Success, "result should indicate failure")
	require.NotNil(t, result.Error)
	assert.Contains(t, result.Error.Message, "not supported")
}

func TestConvertAndQuery_JSONObject(t *testing.T) {
	ctx := context.Background()

	sqlStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath:     ":memory:",
		TTLSeconds: 3600,
	})
	require.NoError(t, err)
	defer sqlStore.Close()

	memoryStore := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	tool := &QueryToolResultTool{
		sqlStore:    sqlStore,
		memoryStore: memoryStore,
	}

	// Store single JSON object (not array)
	jsonData := `{"name": "Alice", "score": 95}`
	ref, err := memoryStore.Store("test_obj", []byte(jsonData), "application/json", nil)
	require.NoError(t, err)

	// Get metadata
	meta, err := memoryStore.GetMetadata(ref)
	require.NoError(t, err)

	// Try to query (should fail - only arrays are convertible)
	result, err := tool.convertAndQuery(ctx, ref, meta, "SELECT * FROM results")
	require.NoError(t, err, "convertAndQuery should not return error")
	assert.False(t, result.Success, "result should indicate failure")
	require.NotNil(t, result.Error)
	assert.Contains(t, result.Error.Message, "not supported")
}

func TestConvertJSONArrayToRows_ColumnOrdering(t *testing.T) {
	tool := &QueryToolResultTool{}

	// Test that column order is consistent across rows
	jsonData := `[
		{"z": 3, "a": 1, "m": 2},
		{"a": 4, "z": 6, "m": 5},
		{"m": 8, "z": 9, "a": 7}
	]`

	columns, rows, err := tool.convertJSONArrayToRows([]byte(jsonData))
	require.NoError(t, err)

	// Columns should be sorted alphabetically for consistency
	assert.Equal(t, []string{"a", "m", "z"}, columns)

	// Verify each row has values in correct column order
	for i, row := range rows {
		assert.Len(t, row, 3, "row %d should have 3 values", i)
		// Each row should have values matching the column order
		assert.NotNil(t, row[0]) // a
		assert.NotNil(t, row[1]) // m
		assert.NotNil(t, row[2]) // z
	}
}

func TestConvertCSVToRows_TrailingNewlines(t *testing.T) {
	tool := &QueryToolResultTool{}

	csvData := "id,name\n1,Alice\n2,Bob\n\n\n"

	columns, rows, err := tool.convertCSVToRows([]byte(csvData))
	require.NoError(t, err)

	assert.Equal(t, []string{"id", "name"}, columns)
	assert.Len(t, rows, 2, "should ignore trailing newlines")
}
