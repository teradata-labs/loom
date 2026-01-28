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
	"fmt"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

const (
	// MaxPreviewChars limits progressive disclosure preview size
	// 20,000 chars ≈ 5K tokens (4 bytes/token estimate)
	// Large enough for full workflow examples (~20KB) with minimal headroom
	// Patterns are injected separately so don't need larger limits
	MaxPreviewChars = 20000
)

// DataMetadata contains metadata about stored data for progressive disclosure.
// This enables agents to inspect data structure and size before retrieving full content.
type DataMetadata struct {
	ID              string
	ContentType     string // "application/json", "text/csv", "text/plain"
	DataType        string // "json_array", "json_object", "csv", "text"
	SizeBytes       int64
	EstimatedTokens int64
	Schema          *SchemaInfo
	Preview         *PreviewData
	CreatedAt       time.Time
	ExpiresAt       time.Time
	Location        loomv1.StorageLocation
}

// SchemaInfo describes the structure of the data.
type SchemaInfo struct {
	Type       string      // "array", "object", "table"
	ItemCount  int64       // Number of items in array or rows in table
	Fields     []FieldInfo // For JSON objects
	Columns    []string    // For CSV/tabular data
	SampleItem any         // Representative sample
}

// FieldInfo describes a field in JSON data.
type FieldInfo struct {
	Name string
	Type string // "string", "number", "boolean", "object", "array", "null"
}

// PreviewData contains sample data for quick inspection.
type PreviewData struct {
	First5 []any
	Last5  []any
}

// GetMetadata returns metadata about stored data without loading full content.
// This enables progressive disclosure - agents can inspect data before retrieving.
func (s *SharedMemoryStore) GetMetadata(ref *loomv1.DataReference) (*DataMetadata, error) {
	if ref == nil || ref.Id == "" {
		return nil, fmt.Errorf("invalid reference: reference is nil or has empty ID")
	}

	// Fetch raw data to analyze
	// TODO: Optimize by caching metadata separately in SharedData struct
	data, err := s.Get(ref)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve data for metadata analysis: %w", err)
	}

	// Detect content type and generate metadata
	contentType, dataType := detectDataType(data)
	schema, preview := analyzeData(data, dataType)

	// Get creation time - look up from actual SharedData if ref.StoredAt is missing (epoch zero)
	// This fixes the issue where query_tool_result creates minimal DataReference without StoredAt
	var createdAt time.Time
	if ref.StoredAt > 0 {
		createdAt = time.Unix(0, ref.StoredAt*int64(time.Millisecond))
	} else {
		// StoredAt not provided in reference - look up from SharedData
		s.mu.RLock()
		if sharedData, exists := s.data[ref.Id]; exists {
			createdAt = sharedData.StoredAt
		}
		s.mu.RUnlock()

		// If still zero (shouldn't happen), use current time
		if createdAt.IsZero() {
			createdAt = time.Now()
		}
	}

	// Calculate expiration (use TTL if available)
	expiresAt := createdAt.Add(s.ttl)

	meta := &DataMetadata{
		ID:              ref.Id,
		ContentType:     contentType,
		DataType:        dataType,
		SizeBytes:       int64(len(data)),
		EstimatedTokens: int64(len(data)) / 4, // Rough estimate: 4 bytes per token
		Schema:          schema,
		Preview:         preview,
		Location:        ref.Location,
		CreatedAt:       createdAt,
		ExpiresAt:       expiresAt,
	}

	return meta, nil
}

// detectDataType detects the data type from raw bytes.
func detectDataType(data []byte) (contentType string, dataType string) {
	// Try JSON first
	var jsonObj interface{}
	if json.Unmarshal(data, &jsonObj) == nil {
		if _, isArray := jsonObj.([]interface{}); isArray {
			return "application/json", "json_array"
		}
		if _, isObject := jsonObj.(map[string]interface{}); isObject {
			return "application/json", "json_object"
		}
	}

	// Try CSV detection
	if looksLikeCSV(data) {
		return "text/csv", "csv"
	}

	// Default to plain text
	return "text/plain", "text"
}

// analyzeData generates schema and preview based on data type.
func analyzeData(data []byte, dataType string) (*SchemaInfo, *PreviewData) {
	switch dataType {
	case "json_array":
		return analyzeJSONArray(data)
	case "json_object":
		return analyzeJSONObject(data)
	case "csv":
		return analyzeCSV(data)
	default:
		return analyzeText(data)
	}
}

// analyzeJSONArray analyzes JSON array structure.
func analyzeJSONArray(data []byte) (*SchemaInfo, *PreviewData) {
	var items []any
	if err := json.Unmarshal(data, &items); err != nil {
		return &SchemaInfo{Type: "array", ItemCount: 0}, &PreviewData{}
	}

	schema := &SchemaInfo{
		Type:      "array",
		ItemCount: int64(len(items)),
	}

	// Infer schema from first item
	if len(items) > 0 {
		schema.SampleItem = items[0]
		if obj, ok := items[0].(map[string]any); ok {
			schema.Fields = inferFields(obj)
		}
	}

	// Generate preview
	preview := &PreviewData{}
	if len(items) > 0 {
		end := min(5, len(items))
		preview.First5 = items[:end]
	}
	if len(items) > 5 {
		start := max(0, len(items)-5)
		preview.Last5 = items[start:]
	}

	return schema, preview
}

// truncateObjectForPreview creates a truncated copy of an object for preview display.
// Uses character-based limits to prevent massive previews.
func truncateObjectForPreview(obj map[string]any, maxChars int) map[string]any {
	if obj == nil {
		return nil
	}

	truncated := make(map[string]any)
	totalChars := 0

	for key, value := range obj {
		keyValueStr := fmt.Sprintf("%s:%v", key, value)
		if totalChars+len(keyValueStr) > maxChars {
			truncated["..."] = fmt.Sprintf("(%d more fields, %d chars over limit)",
				len(obj)-len(truncated),
				totalChars+len(keyValueStr)-maxChars)
			break
		}

		// Allocate remaining budget proportionally
		remainingChars := maxChars - totalChars
		remainingFields := len(obj) - len(truncated)
		if remainingFields > 0 {
			truncated[key] = truncateValue(value, remainingChars/remainingFields)
		} else {
			truncated[key] = truncateValue(value, remainingChars)
		}
		totalChars += len(keyValueStr)
	}

	return truncated
}

// truncateValue truncates a value to a maximum length for preview display.
func truncateValue(value any, maxLen int) any {
	switch v := value.(type) {
	case string:
		if len(v) > maxLen {
			return v[:maxLen] + "... (truncated)"
		}
		return v

	case []any:
		// Character-based truncation for arrays
		var totalChars int
		var truncated []any

		for i, item := range v {
			itemStr := fmt.Sprintf("%v", item)
			if totalChars+len(itemStr) > maxLen {
				truncated = append(truncated, fmt.Sprintf("... (%d more items, %d chars remaining)", len(v)-i, maxLen-totalChars))
				break
			}
			// Allocate remaining budget proportionally
			remainingItems := len(v) - i
			if remainingItems > 0 {
				truncated = append(truncated, truncateValue(item, (maxLen-totalChars)/remainingItems))
			} else {
				truncated = append(truncated, truncateValue(item, maxLen-totalChars))
			}
			totalChars += len(itemStr)
		}

		if len(truncated) == 0 {
			return v
		}
		return truncated

	case map[string]any:
		return truncateObjectForPreview(v, maxLen)

	default:
		// Numbers, booleans, null - return as is
		return v
	}
}

// analyzeJSONObject analyzes JSON object structure.
func analyzeJSONObject(data []byte) (*SchemaInfo, *PreviewData) {
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return &SchemaInfo{Type: "object"}, &PreviewData{}
	}

	schema := &SchemaInfo{
		Type:       "object",
		Fields:     inferFields(obj),
		SampleItem: obj,
	}

	// CRITICAL FIX: Truncate object preview to prevent 1MB+ previews from accumulating in conversation
	// Use character-based limit (20,000 chars ≈ 5K tokens)
	truncatedObj := truncateObjectForPreview(obj, MaxPreviewChars)

	preview := &PreviewData{
		First5: []any{truncatedObj},
	}

	return schema, preview
}

// analyzeCSV analyzes CSV structure.
func analyzeCSV(data []byte) (*SchemaInfo, *PreviewData) {
	lines := strings.Split(string(data), "\n")
	if len(lines) < 1 {
		return &SchemaInfo{Type: "table"}, &PreviewData{}
	}

	// Extract headers (first line)
	headers := strings.Split(lines[0], ",")
	for i, h := range headers {
		headers[i] = strings.TrimSpace(h)
	}

	schema := &SchemaInfo{
		Type:      "table",
		ItemCount: int64(len(lines) - 1), // Exclude header
		Columns:   headers,
	}

	// Generate preview (first 5 + last 5 rows)
	preview := &PreviewData{}

	// First 5 data rows (skip header)
	first5End := min(6, len(lines)) // 1 header + 5 data rows
	if first5End > 1 {
		for i := 1; i < first5End; i++ {
			preview.First5 = append(preview.First5, lines[i])
		}
	}

	// Last 5 rows
	if len(lines) > 11 { // header + 5 first + 5 last
		last5Start := max(1, len(lines)-5)
		for i := last5Start; i < len(lines); i++ {
			preview.Last5 = append(preview.Last5, lines[i])
		}
	}

	return schema, preview
}

// analyzeText analyzes plain text structure.
func analyzeText(data []byte) (*SchemaInfo, *PreviewData) {
	text := string(data)
	lines := strings.Split(text, "\n")

	schema := &SchemaInfo{
		Type:      "text",
		ItemCount: int64(len(lines)),
	}

	// Preview: first 5 and last 5 lines
	preview := &PreviewData{}

	if len(lines) > 0 {
		end := min(5, len(lines))
		for i := 0; i < end; i++ {
			preview.First5 = append(preview.First5, lines[i])
		}
	}

	if len(lines) > 10 {
		start := max(0, len(lines)-5)
		for i := start; i < len(lines); i++ {
			preview.Last5 = append(preview.Last5, lines[i])
		}
	}

	return schema, preview
}

// inferFields infers field types from a JSON object.
func inferFields(obj map[string]any) []FieldInfo {
	fields := make([]FieldInfo, 0, len(obj))
	for key, value := range obj {
		fields = append(fields, FieldInfo{
			Name: key,
			Type: getJSONType(value),
		})
	}
	return fields
}

// getJSONType returns the JSON type name for a value.
func getJSONType(value any) string {
	if value == nil {
		return "null"
	}
	switch value.(type) {
	case bool:
		return "boolean"
	case float64, int, int64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

// looksLikeCSV checks if data looks like CSV format.
func looksLikeCSV(data []byte) bool {
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return false
	}

	// Check if first line has commas and second line has same number of commas
	firstCommas := strings.Count(lines[0], ",")
	secondCommas := strings.Count(lines[1], ",")

	// At least one comma and consistent comma count
	return firstCommas > 0 && firstCommas == secondCommas
}

// Helper functions
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
