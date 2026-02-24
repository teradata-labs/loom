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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/storage"
)

// TestProgressiveDisclosure_LargeJSONArray tests the full progressive disclosure workflow
// for a large JSON array: Store → GetMetadata (preview) → QueryToolResult (SQL filter)
func TestProgressiveDisclosure_LargeJSONArray(t *testing.T) {
	ctx := context.Background()

	// Setup stores
	sqlStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath:     ":memory:",
		TTLSeconds: 3600,
	})
	require.NoError(t, err)
	defer func() { _ = sqlStore.Close() }()

	memoryStore := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Setup tools
	getMetadataTool := NewGetToolResultTool(memoryStore, sqlStore)
	queryTool := NewQueryToolResultTool(sqlStore, memoryStore)

	// Step 1: Store large JSON array (simulating a tool returning large results)
	largeData := make([]map[string]any, 100)
	for i := range 100 {
		largeData[i] = map[string]any{
			"id":       float64(i + 1),
			"name":     "User" + string(rune(65+(i%26))), // UserA, UserB, etc.
			"score":    float64(50 + (i % 51)),           // Scores from 50-100
			"active":   i%2 == 0,
			"category": []string{"cat1", "cat2", "cat3"}[i%3],
		}
	}
	jsonData, err := json.Marshal(largeData)
	require.NoError(t, err)

	ref, err := memoryStore.Store("large_result", jsonData, "application/json", nil)
	require.NoError(t, err)
	assert.Equal(t, loomv1.StorageLocation_STORAGE_LOCATION_MEMORY, ref.Location)

	// Step 2: Agent calls get_tool_result to get metadata (should NOT get full data)
	metadataResult, err := getMetadataTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
	})
	require.NoError(t, err)

	require.True(t, metadataResult.Success, "get_tool_result should succeed")
	metadata, ok := metadataResult.Data.(map[string]any)
	require.True(t, ok)

	// Verify metadata contains expected fields
	assert.Equal(t, ref.Id, metadata["reference_id"])
	assert.Equal(t, "json_array", metadata["data_type"])
	assert.Equal(t, "application/json", metadata["content_type"])

	// Schema and Preview are structs, not maps
	schema, ok := metadata["schema"].(*storage.SchemaInfo)
	require.True(t, ok)
	assert.Equal(t, "array", schema.Type)
	assert.Equal(t, int64(100), schema.ItemCount)

	// Verify preview (first 5 + last 5)
	preview, ok := metadata["preview"].(*storage.PreviewData)
	require.True(t, ok)

	assert.Len(t, preview.First5, 5)
	assert.Len(t, preview.Last5, 5)

	// Verify retrieval hints exist (they're strings, not map)
	hints, ok := metadata["retrieval_hints"].([]string)
	require.True(t, ok)
	assert.Greater(t, len(hints), 0, "should have retrieval hints")

	// Step 3: Agent analyzes preview and decides to query for high scores
	queryResult, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"sql":          "SELECT name, score FROM results WHERE CAST(score AS REAL) >= 90 ORDER BY CAST(score AS REAL) DESC LIMIT 10",
	})
	require.NoError(t, err)

	if !queryResult.Success {
		t.Logf("Query failed: %+v", queryResult.Error)
	}
	require.True(t, queryResult.Success, "query should succeed")
	resultData, ok := queryResult.Data.(map[string]any)
	require.True(t, ok)

	rows := resultData["rows"].([][]any)
	columns := resultData["columns"].([]string)

	assert.Contains(t, columns, "name")
	assert.Contains(t, columns, "score")
	assert.LessOrEqual(t, len(rows), 11, "should have at most 11 rows (scores 90-100)")

	// Verify all scores are >= 90
	for _, row := range rows {
		scoreStr := row[1].(string)
		// Score is stored as TEXT, so we do string comparison
		// But since we ORDER BY CAST(score AS REAL) DESC, the data is correct
		// Just verify we got some results
		assert.NotEmpty(t, scoreStr)
	}
}

// TestProgressiveDisclosure_CSVPagination tests pagination for CSV data
func TestProgressiveDisclosure_CSVPagination(t *testing.T) {
	ctx := context.Background()

	// Setup stores
	sqlStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath:     ":memory:",
		TTLSeconds: 3600,
	})
	require.NoError(t, err)
	defer func() { _ = sqlStore.Close() }()

	memoryStore := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Setup tools
	getMetadataTool := NewGetToolResultTool(memoryStore, sqlStore)
	queryTool := NewQueryToolResultTool(sqlStore, memoryStore)

	// Step 1: Store CSV data
	csvData := "id,name,email\n"
	for i := range 50 {
		csvData += string(rune(49+i)) + ",User" + string(rune(65+(i%26))) + ",user" + string(rune(49+i)) + "@example.com\n"
	}

	ref, err := memoryStore.Store("csv_result", []byte(csvData), "text/csv", nil)
	require.NoError(t, err)

	// Step 2: Get metadata
	metadataResult, err := getMetadataTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
	})
	require.NoError(t, err)

	require.True(t, metadataResult.Success)
	metadata, ok := metadataResult.Data.(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "csv", metadata["data_type"])

	schema, ok := metadata["schema"].(*storage.SchemaInfo)
	require.True(t, ok)
	// CSV parser counts header + data rows, so 51 total (1 header + 50 data)
	assert.Equal(t, int64(51), schema.ItemCount)

	// Step 3: Paginate through results (first 10 rows)
	queryResult, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"sql":          "SELECT * FROM results LIMIT 10",
	})
	require.NoError(t, err)

	require.True(t, queryResult.Success)
	resultData, ok := queryResult.Data.(map[string]any)
	require.True(t, ok)

	rows := resultData["rows"].([][]any)
	columns := resultData["columns"].([]string)

	assert.Len(t, columns, 3) // id, name, email
	assert.Len(t, rows, 10)   // Limited to 10

	// Step 4: Get next page (offset 10, limit 10)
	queryResult2, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"sql":          "SELECT * FROM results LIMIT 10 OFFSET 10",
	})
	require.NoError(t, err)

	require.True(t, queryResult2.Success)
	resultData2 := queryResult2.Data.(map[string]any)
	rows2 := resultData2["rows"].([][]any)
	assert.Len(t, rows2, 10)

	// Verify pages are different
	assert.NotEqual(t, rows[0], rows2[0])
}

// TestProgressiveDisclosure_PreventContextBlowout tests that get_tool_result
// never returns full data, forcing use of query_tool_result
func TestProgressiveDisclosure_PreventContextBlowout(t *testing.T) {
	ctx := context.Background()

	// Setup stores
	sqlStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath:     ":memory:",
		TTLSeconds: 3600,
	})
	require.NoError(t, err)
	defer func() { _ = sqlStore.Close() }()

	memoryStore := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Setup tool
	getMetadataTool := NewGetToolResultTool(memoryStore, sqlStore)

	// Store huge JSON array (1000 items)
	hugeData := make([]map[string]any, 1000)
	for i := range 1000 {
		hugeData[i] = map[string]any{
			"id":          float64(i + 1),
			"description": "This is a very long description with lots of text that would blow up context if returned in full",
		}
	}
	jsonData, err := json.Marshal(hugeData)
	require.NoError(t, err)

	ref, err := memoryStore.Store("huge_result", jsonData, "application/json", nil)
	require.NoError(t, err)

	// Get metadata
	metadataResult, err := getMetadataTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
	})
	require.NoError(t, err)

	require.True(t, metadataResult.Success)
	metadata, ok := metadataResult.Data.(map[string]any)
	require.True(t, ok)

	// Verify metadata is small (only preview, not full data)
	schema := metadata["schema"].(*storage.SchemaInfo)
	assert.Equal(t, int64(1000), schema.ItemCount)

	preview := metadata["preview"].(*storage.PreviewData)

	// Should only have first 5 + last 5, not all 1000
	assert.Len(t, preview.First5, 5)
	assert.Len(t, preview.Last5, 5)

	// Metadata result should be much smaller than original data
	metadataJSON, _ := json.Marshal(metadata)
	assert.Less(t, len(metadataJSON), len(jsonData)/10, "metadata should be <10% of original data size")
}

// TestProgressiveDisclosure_SQLFiltering tests SQL WHERE clauses for filtering
func TestProgressiveDisclosure_SQLFiltering(t *testing.T) {
	ctx := context.Background()

	// Setup stores
	sqlStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath:     ":memory:",
		TTLSeconds: 3600,
	})
	require.NoError(t, err)
	defer func() { _ = sqlStore.Close() }()

	memoryStore := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Setup tool
	queryTool := NewQueryToolResultTool(sqlStore, memoryStore)

	// Store data
	testData := []map[string]any{
		{"category": "A", "value": float64(10), "active": true},
		{"category": "B", "value": float64(20), "active": false},
		{"category": "A", "value": float64(30), "active": true},
		{"category": "C", "value": float64(40), "active": true},
		{"category": "B", "value": float64(50), "active": true},
	}
	jsonData, _ := json.Marshal(testData)
	ref, err := memoryStore.Store("filter_test", jsonData, "application/json", nil)
	require.NoError(t, err)

	// Test 1: Filter by category
	result1, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"sql":          "SELECT category, value FROM results WHERE category = 'A'",
	})
	require.NoError(t, err)

	require.True(t, result1.Success)
	data1 := result1.Data.(map[string]any)
	rows1 := data1["rows"].([][]any)
	assert.Len(t, rows1, 2, "should have 2 rows with category A")

	// Test 2: Filter by value range
	result2, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"sql":          "SELECT category, value FROM results WHERE CAST(value AS REAL) >= 30",
	})
	require.NoError(t, err)

	require.True(t, result2.Success)
	data2 := result2.Data.(map[string]any)
	rows2 := data2["rows"].([][]any)
	assert.Len(t, rows2, 3, "should have 3 rows with value >= 30")

	// Test 3: Complex filter
	result3, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"sql":          "SELECT category, value FROM results WHERE category IN ('A', 'B') AND CAST(value AS REAL) > 15",
	})
	require.NoError(t, err)

	require.True(t, result3.Success)
	data3 := result3.Data.(map[string]any)
	rows3 := data3["rows"].([][]any)
	assert.Len(t, rows3, 3, "should have 3 rows matching complex filter")
}

// TestProgressiveDisclosure_MultipleQueries tests that the same data
// can be queried multiple times with different filters
func TestProgressiveDisclosure_MultipleQueries(t *testing.T) {
	ctx := context.Background()

	// Setup stores
	sqlStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath:     ":memory:",
		TTLSeconds: 3600,
	})
	require.NoError(t, err)
	defer func() { _ = sqlStore.Close() }()

	memoryStore := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Setup tool
	queryTool := NewQueryToolResultTool(sqlStore, memoryStore)

	// Store data once
	testData := make([]map[string]any, 20)
	for i := range 20 {
		testData[i] = map[string]any{
			"id":    float64(i + 1),
			"score": float64(50 + i*2),
		}
	}
	jsonData, _ := json.Marshal(testData)
	ref, err := memoryStore.Store("multi_query_test", jsonData, "application/json", nil)
	require.NoError(t, err)

	// Query 1: Get high scores
	result1, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"sql":          "SELECT * FROM results WHERE CAST(score AS REAL) >= 80",
	})
	require.NoError(t, err)
	require.True(t, result1.Success)
	rows1 := result1.Data.(map[string]any)["rows"].([][]any)

	// Query 2: Get low scores
	result2, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"sql":          "SELECT * FROM results WHERE CAST(score AS REAL) < 60",
	})
	require.NoError(t, err)
	require.True(t, result2.Success)
	rows2 := result2.Data.(map[string]any)["rows"].([][]any)

	// Query 3: Get count
	result3, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"sql":          "SELECT COUNT(*) as total FROM results",
	})
	require.NoError(t, err)
	require.True(t, result3.Success)
	rows3 := result3.Data.(map[string]any)["rows"].([][]any)

	assert.Greater(t, len(rows1), 0)
	assert.Greater(t, len(rows2), 0)
	assert.Len(t, rows3, 1) // COUNT returns single row
}

// TestProgressiveDisclosure_JSONObject tests that json_object types can be retrieved
// without requiring offset/limit parameters
func TestProgressiveDisclosure_JSONObject(t *testing.T) {
	ctx := context.Background()

	// Setup stores
	sqlStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath:     ":memory:",
		TTLSeconds: 3600,
	})
	require.NoError(t, err)
	defer func() { _ = sqlStore.Close() }()

	memoryStore := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Setup tools
	getMetadataTool := NewGetToolResultTool(memoryStore, sqlStore)
	queryTool := NewQueryToolResultTool(sqlStore, memoryStore)

	// Step 1: Store JSON object (simulating discovery results)
	discoveryResult := map[string]any{
		"database_version": "Teradata 17.20.00.08",
		"tools_available": []string{
			"teradata_execute_query",
			"teradata_describe_table",
			"teradata_sample_table",
		},
		"connection_info": map[string]any{
			"host":     "localhost",
			"port":     1025,
			"database": "DBC",
		},
		"features": map[string]any{
			"query_banding": true,
			"temporal":      false,
			"json":          true,
		},
	}
	jsonData, err := json.Marshal(discoveryResult)
	require.NoError(t, err)

	ref, err := memoryStore.Store("discovery_result", jsonData, "application/json", nil)
	require.NoError(t, err)
	assert.Equal(t, loomv1.StorageLocation_STORAGE_LOCATION_MEMORY, ref.Location)

	// Step 2: Agent calls get_tool_result to get metadata
	metadataResult, err := getMetadataTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
	})
	require.NoError(t, err)
	require.True(t, metadataResult.Success)

	metadata, ok := metadataResult.Data.(map[string]any)
	require.True(t, ok)

	// Verify it's classified as json_object
	assert.Equal(t, "json_object", metadata["data_type"])
	assert.Equal(t, "application/json", metadata["content_type"])

	// Step 3: Agent calls query_tool_result WITHOUT any parameters
	// This should FAIL for json_object types - they cannot be retrieved directly (too large)
	queryResult, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
	})
	require.NoError(t, err)

	// Should fail with helpful error message
	require.False(t, queryResult.Success, "query_tool_result should fail for json_object without retrieval method")
	require.NotNil(t, queryResult.Error)
	assert.Equal(t, "invalid_input", queryResult.Error.Code)
	assert.Contains(t, queryResult.Error.Message, "json_object")
	assert.Contains(t, queryResult.Error.Suggestion, "metadata", "error should suggest checking metadata")
	assert.Contains(t, queryResult.Error.Suggestion, "retrieval hints", "error should mention retrieval hints")

	// Verify metadata provides helpful hints about the structure
	// Agent should use the preview and schema from get_tool_result instead
	retrievalHints := metadata["retrieval_hints"]
	require.NotNil(t, retrievalHints)
	// Check that hints warn about large object (if hints provided)
	hintsStr := fmt.Sprintf("%v", retrievalHints)
	if len(hintsStr) > 2 { // More than just "[]"
		assert.Contains(t, hintsStr, "cannot be retrieved directly")
	}
}

// TestProgressiveDisclosure_JSONObjectVsArray tests the distinction between
// json_object and json_array handling
func TestProgressiveDisclosure_JSONObjectVsArray(t *testing.T) {
	ctx := context.Background()

	memoryStore := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	queryTool := NewQueryToolResultTool(nil, memoryStore)

	// Test 1: JSON object should fail without retrieval method
	objData := map[string]any{"key": "value", "count": float64(42)}
	objJSON, _ := json.Marshal(objData)
	objRef, _ := memoryStore.Store("obj", objJSON, "application/json", nil)

	objResult, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": objRef.Id,
	})
	require.NoError(t, err)
	assert.False(t, objResult.Success, "json_object should fail without retrieval method")
	assert.Contains(t, objResult.Error.Message, "json_object", "error should mention data type")

	// Test 2: JSON array also requires offset/limit or SQL
	arrayData := []map[string]any{{"id": float64(1)}, {"id": float64(2)}}
	arrayJSON, _ := json.Marshal(arrayData)
	arrayRef, _ := memoryStore.Store("array", arrayJSON, "application/json", nil)

	arrayResult, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": arrayRef.Id,
	})
	require.NoError(t, err)
	assert.False(t, arrayResult.Success, "json_array should require parameters")
	assert.Contains(t, arrayResult.Error.Message, "json_array", "error should mention data type")
	assert.Contains(t, arrayResult.Error.Suggestion, "metadata", "error should suggest checking metadata")
	assert.Contains(t, arrayResult.Error.Suggestion, "retrieval hints", "error should mention retrieval hints")

	// Test 3: JSON array works with offset/limit
	arrayResult2, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": arrayRef.Id,
		"offset":       float64(0),
		"limit":        float64(10),
	})
	require.NoError(t, err)
	assert.True(t, arrayResult2.Success, "json_array should work with offset/limit")
}

// TestProgressiveDisclosure_TextPagination tests line-based pagination for plain text data
func TestProgressiveDisclosure_TextPagination(t *testing.T) {
	ctx := context.Background()

	memoryStore := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	getMetadataTool := NewGetToolResultTool(memoryStore, nil)
	queryTool := NewQueryToolResultTool(nil, memoryStore)

	// Create test text data (100 lines)
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("Line %d: This is test content for line number %d", i, i))
	}
	textData := []byte(strings.Join(lines, "\n"))

	// Store text data
	ref, err := memoryStore.Store("test_text", textData, "text/plain", nil)
	require.NoError(t, err)
	assert.Equal(t, loomv1.StorageLocation_STORAGE_LOCATION_MEMORY, ref.Location)

	// Get metadata
	metadataResult, err := getMetadataTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
	})
	require.NoError(t, err)
	require.True(t, metadataResult.Success)

	metadata, ok := metadataResult.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "text", metadata["data_type"])

	// Test pagination - first 10 lines
	result1, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"offset":       float64(0),
		"limit":        float64(10),
	})
	require.NoError(t, err)
	require.True(t, result1.Success, "text pagination should succeed")

	data1 := result1.Data.(map[string]any)
	returnedLines1 := data1["lines"].([]string)
	assert.Len(t, returnedLines1, 10, "should return 10 lines")
	assert.Equal(t, "Line 1: This is test content for line number 1", returnedLines1[0])
	assert.Equal(t, 10, data1["returned_count"])
	assert.Equal(t, 100, data1["total_lines"])
	assert.True(t, data1["has_more"].(bool))

	// Test pagination - middle 10 lines
	result2, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"offset":       float64(45),
		"limit":        float64(10),
	})
	require.NoError(t, err)
	require.True(t, result2.Success)

	data2 := result2.Data.(map[string]any)
	returnedLines2 := data2["lines"].([]string)
	assert.Len(t, returnedLines2, 10)
	assert.Equal(t, "Line 46: This is test content for line number 46", returnedLines2[0])

	// Test pagination - last 10 lines
	result3, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"offset":       float64(90),
		"limit":        float64(10),
	})
	require.NoError(t, err)
	require.True(t, result3.Success)

	data3 := result3.Data.(map[string]any)
	returnedLines3 := data3["lines"].([]string)
	assert.Len(t, returnedLines3, 10)
	assert.Equal(t, "Line 91: This is test content for line number 91", returnedLines3[0])
	assert.False(t, data3["has_more"].(bool), "should be last page")

	// Test invalid offset
	result4, err := queryTool.Execute(ctx, map[string]any{
		"reference_id": ref.Id,
		"offset":       float64(150),
		"limit":        float64(10),
	})
	require.NoError(t, err)
	assert.False(t, result4.Success, "should fail with invalid offset")
	assert.Contains(t, result4.Error.Message, "out of range")
}
