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

package storage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestGetMetadata_JSONArray(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Create test JSON array
	testData := []map[string]any{
		{"id": float64(1), "name": "Alice", "score": float64(95)},
		{"id": float64(2), "name": "Bob", "score": float64(87)},
		{"id": float64(3), "name": "Charlie", "score": float64(92)},
	}
	jsonData, err := json.Marshal(testData)
	require.NoError(t, err)

	// Store data
	ref, err := store.Store("test1", jsonData, "application/json", nil)
	require.NoError(t, err)

	// Get metadata
	meta, err := store.GetMetadata(ref)
	require.NoError(t, err)

	// Verify metadata
	assert.Equal(t, "test1", meta.ID)
	assert.Equal(t, "application/json", meta.ContentType)
	assert.Equal(t, "json_array", meta.DataType)
	assert.Equal(t, int64(len(jsonData)), meta.SizeBytes)
	assert.Greater(t, meta.EstimatedTokens, int64(0))

	// Verify schema
	require.NotNil(t, meta.Schema)
	assert.Equal(t, "array", meta.Schema.Type)
	assert.Equal(t, int64(3), meta.Schema.ItemCount)
	assert.Len(t, meta.Schema.Fields, 3) // id, name, score

	// Verify preview
	require.NotNil(t, meta.Preview)
	assert.Len(t, meta.Preview.First5, 3)
	assert.Empty(t, meta.Preview.Last5) // Less than 10 items total
}

func TestGetMetadata_JSONObject(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Create test JSON object
	testData := map[string]any{
		"name":   "Test User",
		"email":  "test@example.com",
		"active": true,
		"age":    float64(30),
	}
	jsonData, err := json.Marshal(testData)
	require.NoError(t, err)

	// Store data
	ref, err := store.Store("test2", jsonData, "application/json", nil)
	require.NoError(t, err)

	// Get metadata
	meta, err := store.GetMetadata(ref)
	require.NoError(t, err)

	// Verify metadata
	assert.Equal(t, "application/json", meta.ContentType)
	assert.Equal(t, "json_object", meta.DataType)

	// Verify schema
	require.NotNil(t, meta.Schema)
	assert.Equal(t, "object", meta.Schema.Type)
	assert.Len(t, meta.Schema.Fields, 4) // name, email, active, age

	// Verify preview
	require.NotNil(t, meta.Preview)
	assert.Len(t, meta.Preview.First5, 1) // Single object
}

func TestGetMetadata_CSV(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Create test CSV
	csvData := `id,name,score
1,Alice,95
2,Bob,87
3,Charlie,92
4,David,88
5,Eve,91`

	// Store data
	ref, err := store.Store("test3", []byte(csvData), "text/csv", nil)
	require.NoError(t, err)

	// Get metadata
	meta, err := store.GetMetadata(ref)
	require.NoError(t, err)

	// Verify metadata
	assert.Equal(t, "text/csv", meta.ContentType)
	assert.Equal(t, "csv", meta.DataType)

	// Verify schema
	require.NotNil(t, meta.Schema)
	assert.Equal(t, "table", meta.Schema.Type)
	assert.Equal(t, int64(5), meta.Schema.ItemCount) // 5 data rows
	assert.Equal(t, []string{"id", "name", "score"}, meta.Schema.Columns)

	// Verify preview
	require.NotNil(t, meta.Preview)
	assert.Len(t, meta.Preview.First5, 5)
	assert.Empty(t, meta.Preview.Last5) // Less than 10 data rows
}

func TestGetMetadata_Text(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Create test text
	textData := `Line 1
Line 2
Line 3
Line 4
Line 5`

	// Store data
	ref, err := store.Store("test4", []byte(textData), "text/plain", nil)
	require.NoError(t, err)

	// Get metadata
	meta, err := store.GetMetadata(ref)
	require.NoError(t, err)

	// Verify metadata
	assert.Equal(t, "text/plain", meta.ContentType)
	assert.Equal(t, "text", meta.DataType)

	// Verify schema
	require.NotNil(t, meta.Schema)
	assert.Equal(t, "text", meta.Schema.Type)
	assert.Equal(t, int64(5), meta.Schema.ItemCount) // 5 lines

	// Verify preview
	require.NotNil(t, meta.Preview)
	assert.Len(t, meta.Preview.First5, 5)
	assert.Empty(t, meta.Preview.Last5) // Less than 10 lines
}

func TestGetMetadata_LargeArray(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Create large JSON array (15 items)
	testData := make([]map[string]any, 15)
	for i := range 15 {
		testData[i] = map[string]any{
			"id":    float64(i + 1),
			"value": float64(i * 10),
		}
	}
	jsonData, err := json.Marshal(testData)
	require.NoError(t, err)

	// Store data
	ref, err := store.Store("test5", jsonData, "application/json", nil)
	require.NoError(t, err)

	// Get metadata
	meta, err := store.GetMetadata(ref)
	require.NoError(t, err)

	// Verify preview has both first 5 and last 5
	require.NotNil(t, meta.Preview)
	assert.Len(t, meta.Preview.First5, 5)
	assert.Len(t, meta.Preview.Last5, 5)

	// Verify first and last items
	firstArray, ok := meta.Preview.First5[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1), firstArray["id"])

	lastArray, ok := meta.Preview.Last5[4].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(15), lastArray["id"])
}

func TestGetMetadata_InvalidReference(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	// Try to get metadata for non-existent reference
	ref := &loomv1.DataReference{
		Id:       "nonexistent",
		Location: loomv1.StorageLocation_STORAGE_LOCATION_MEMORY,
	}

	_, err := store.GetMetadata(ref)
	assert.Error(t, err)
}

func TestGetMetadata_NilReference(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           3600,
	})

	_, err := store.GetMetadata(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid reference")
}

func TestGetMetadata_Expiration(t *testing.T) {
	store := NewSharedMemoryStore(&Config{
		MaxMemoryBytes:       1024 * 1024,
		CompressionThreshold: 1024 * 1024,
		TTLSeconds:           1, // 1 second TTL
	})

	// Store data
	testData := []map[string]any{{"id": float64(1)}}
	jsonData, _ := json.Marshal(testData)
	ref, err := store.Store("test6", jsonData, "application/json", nil)
	require.NoError(t, err)

	// Get metadata immediately
	meta1, err := store.GetMetadata(ref)
	require.NoError(t, err)
	assert.True(t, meta1.ExpiresAt.After(time.Now()))

	// Wait for expiration
	time.Sleep(2 * time.Second)

	// Try to get metadata after expiration (should fail)
	_, err = store.GetMetadata(ref)
	assert.Error(t, err)
}

func TestDetectDataType_JSON(t *testing.T) {
	tests := []struct {
		name         string
		data         string
		wantContent  string
		wantDataType string
	}{
		{
			name:         "json array",
			data:         `[{"id":1},{"id":2}]`,
			wantContent:  "application/json",
			wantDataType: "json_array",
		},
		{
			name:         "json object",
			data:         `{"name":"test","value":123}`,
			wantContent:  "application/json",
			wantDataType: "json_object",
		},
		{
			name:         "csv",
			data:         "id,name\n1,Alice\n2,Bob",
			wantContent:  "text/csv",
			wantDataType: "csv",
		},
		{
			name:         "plain text",
			data:         "Just some plain text",
			wantContent:  "text/plain",
			wantDataType: "text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contentType, dataType := detectDataType([]byte(tt.data))
			assert.Equal(t, tt.wantContent, contentType)
			assert.Equal(t, tt.wantDataType, dataType)
		})
	}
}

func TestInferFields(t *testing.T) {
	obj := map[string]any{
		"string_field":  "value",
		"number_field":  float64(123),
		"boolean_field": true,
		"null_field":    nil,
		"array_field":   []any{1, 2, 3},
		"object_field":  map[string]any{"nested": "value"},
	}

	fields := inferFields(obj)

	// Should have all 6 fields
	assert.Len(t, fields, 6)

	// Check field types
	fieldMap := make(map[string]string)
	for _, f := range fields {
		fieldMap[f.Name] = f.Type
	}

	assert.Equal(t, "string", fieldMap["string_field"])
	assert.Equal(t, "number", fieldMap["number_field"])
	assert.Equal(t, "boolean", fieldMap["boolean_field"])
	assert.Equal(t, "null", fieldMap["null_field"])
	assert.Equal(t, "array", fieldMap["array_field"])
	assert.Equal(t, "object", fieldMap["object_field"])
}

func TestLooksLikeCSV(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{
			name: "valid csv",
			data: "id,name,score\n1,Alice,95\n2,Bob,87",
			want: true,
		},
		{
			name: "single line",
			data: "id,name,score",
			want: false,
		},
		{
			name: "no commas",
			data: "header\ndata",
			want: false,
		},
		{
			name: "inconsistent commas",
			data: "id,name,score\n1,Alice",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeCSV([]byte(tt.data))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMinMax(t *testing.T) {
	assert.Equal(t, 2, min(2, 5))
	assert.Equal(t, 2, min(5, 2))
	assert.Equal(t, 3, min(3, 3))

	assert.Equal(t, 5, max(2, 5))
	assert.Equal(t, 5, max(5, 2))
	assert.Equal(t, 3, max(3, 3))
}
