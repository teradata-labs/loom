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
package patterns

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestNewLibrary(t *testing.T) {
	lib := NewLibrary(nil, "")

	if lib == nil {
		t.Fatal("NewLibrary returned nil")
	}

	if lib.patternCache == nil {
		t.Error("patternCache not initialized")
	}

	if len(lib.searchPaths) == 0 {
		t.Error("searchPaths not initialized")
	}
}

func TestLibrary_LoadFromFilesystem(t *testing.T) {
	// Create temporary directory with test pattern
	tmpDir := t.TempDir()

	// Create a test pattern file
	pattern := `name: test_pattern
title: Test Pattern
description: A test pattern for unit testing
category: analytics
difficulty: beginner
backend_type: sql
use_cases:
  - testing
  - validation
parameters:
  - name: table_name
    type: string
    required: true
    description: Name of the table
    example: customers
templates:
  basic:
    description: Basic query template
    content: SELECT * FROM {{table_name}}
    required_parameters:
      - table_name
examples:
  - name: example1
    description: Basic example
    parameters:
      table_name: customers
    expected_result: All rows from customers table
`

	patternPath := filepath.Join(tmpDir, "test_pattern.yaml")
	if err := os.WriteFile(patternPath, []byte(pattern), 0644); err != nil {
		t.Fatalf("Failed to create test pattern: %v", err)
	}

	// Create library with filesystem path
	lib := NewLibrary(nil, tmpDir)

	// Load pattern
	loaded, err := lib.Load("test_pattern")
	if err != nil {
		t.Fatalf("Failed to load pattern: %v", err)
	}

	// Verify pattern contents
	if loaded.Name != "test_pattern" {
		t.Errorf("Expected name 'test_pattern', got '%s'", loaded.Name)
	}

	if loaded.Title != "Test Pattern" {
		t.Errorf("Expected title 'Test Pattern', got '%s'", loaded.Title)
	}

	if loaded.Category != "analytics" {
		t.Errorf("Expected category 'analytics', got '%s'", loaded.Category)
	}

	if loaded.BackendType != "sql" {
		t.Errorf("Expected backend_type 'sql', got '%s'", loaded.BackendType)
	}

	if len(loaded.Parameters) != 1 {
		t.Errorf("Expected 1 parameter, got %d", len(loaded.Parameters))
	}

	if loaded.Parameters[0].Name != "table_name" {
		t.Errorf("Expected parameter name 'table_name', got '%s'", loaded.Parameters[0].Name)
	}

	if len(loaded.Templates) != 1 {
		t.Errorf("Expected 1 template, got %d", len(loaded.Templates))
	}

	if _, ok := loaded.Templates["basic"]; !ok {
		t.Error("Expected template 'basic' not found")
	}
}

func TestLibrary_LoadNotFound(t *testing.T) {
	lib := NewLibrary(nil, "")

	_, err := lib.Load("nonexistent_pattern")
	if err == nil {
		t.Error("Expected error for nonexistent pattern, got nil")
	}
}

func TestLibrary_Caching(t *testing.T) {
	tmpDir := t.TempDir()

	pattern := `name: cached_pattern
title: Cached Pattern
description: Test caching
category: test
difficulty: beginner
backend_type: test
`

	patternPath := filepath.Join(tmpDir, "cached_pattern.yaml")
	if err := os.WriteFile(patternPath, []byte(pattern), 0644); err != nil {
		t.Fatalf("Failed to create test pattern: %v", err)
	}

	lib := NewLibrary(nil, tmpDir)

	// Load first time
	pattern1, err := lib.Load("cached_pattern")
	if err != nil {
		t.Fatalf("Failed to load pattern first time: %v", err)
	}

	// Load second time (should come from cache)
	pattern2, err := lib.Load("cached_pattern")
	if err != nil {
		t.Fatalf("Failed to load pattern second time: %v", err)
	}

	// Should be same instance (pointer equality)
	if pattern1 != pattern2 {
		t.Error("Expected cached pattern to return same instance")
	}
}

func TestLibrary_ListAll(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple test patterns
	patterns := []string{"pattern1", "pattern2", "pattern3"}
	for _, name := range patterns {
		content := `name: ` + name + `
title: ` + name + ` Title
description: Test pattern
category: test
difficulty: beginner
backend_type: test
`
		patternPath := filepath.Join(tmpDir, name+".yaml")
		if err := os.WriteFile(patternPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create test pattern %s: %v", name, err)
		}
	}

	lib := NewLibrary(nil, tmpDir)

	// List all patterns
	summaries := lib.ListAll()

	if len(summaries) != len(patterns) {
		t.Errorf("Expected %d patterns, got %d", len(patterns), len(summaries))
	}

	// Verify names
	foundNames := make(map[string]bool)
	for _, summary := range summaries {
		foundNames[summary.Name] = true
	}

	for _, name := range patterns {
		if !foundNames[name] {
			t.Errorf("Expected to find pattern '%s' in list", name)
		}
	}

	// Second call should use cache
	summaries2 := lib.ListAll()
	if len(summaries2) != len(summaries) {
		t.Error("Cached ListAll returned different number of patterns")
	}
}

func TestLibrary_FilterByCategory(t *testing.T) {
	tmpDir := t.TempDir()

	// Create patterns with different categories
	patterns := map[string]string{
		"analytics1": "analytics",
		"analytics2": "analytics",
		"ml1":        "ml",
		"etl1":       "etl",
	}

	for name, category := range patterns {
		content := `name: ` + name + `
title: ` + name + ` Title
description: Test pattern
category: ` + category + `
difficulty: beginner
backend_type: test
`
		patternPath := filepath.Join(tmpDir, name+".yaml")
		if err := os.WriteFile(patternPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create test pattern: %v", err)
		}
	}

	lib := NewLibrary(nil, tmpDir)

	// Filter by analytics category
	filtered := lib.FilterByCategory("analytics")
	if len(filtered) != 2 {
		t.Errorf("Expected 2 analytics patterns, got %d", len(filtered))
	}

	// Filter by ml category
	filtered = lib.FilterByCategory("ml")
	if len(filtered) != 1 {
		t.Errorf("Expected 1 ml pattern, got %d", len(filtered))
	}

	// Empty filter should return all
	filtered = lib.FilterByCategory("")
	if len(filtered) != len(patterns) {
		t.Errorf("Expected %d patterns with empty filter, got %d", len(patterns), len(filtered))
	}
}

func TestLibrary_FilterByBackendType(t *testing.T) {
	tmpDir := t.TempDir()

	// Create patterns with different backend types
	patterns := map[string]string{
		"sql1":  "sql",
		"sql2":  "sql",
		"rest1": "rest",
		"doc1":  "document",
	}

	for name, backendType := range patterns {
		content := `name: ` + name + `
title: ` + name + ` Title
description: Test pattern
category: test
difficulty: beginner
backend_type: ` + backendType + `
`
		patternPath := filepath.Join(tmpDir, name+".yaml")
		if err := os.WriteFile(patternPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create test pattern: %v", err)
		}
	}

	lib := NewLibrary(nil, tmpDir)

	// Filter by SQL backend
	filtered := lib.FilterByBackendType("sql")
	if len(filtered) != 2 {
		t.Errorf("Expected 2 SQL patterns, got %d", len(filtered))
	}

	// Filter by REST backend
	filtered = lib.FilterByBackendType("rest")
	if len(filtered) != 1 {
		t.Errorf("Expected 1 REST pattern, got %d", len(filtered))
	}
}

func TestLibrary_FilterByDifficulty(t *testing.T) {
	tmpDir := t.TempDir()

	// Create patterns with different difficulties
	patterns := map[string]string{
		"easy1":   "beginner",
		"easy2":   "beginner",
		"medium1": "intermediate",
		"hard1":   "advanced",
	}

	for name, difficulty := range patterns {
		content := `name: ` + name + `
title: ` + name + ` Title
description: Test pattern
category: test
difficulty: ` + difficulty + `
backend_type: test
`
		patternPath := filepath.Join(tmpDir, name+".yaml")
		if err := os.WriteFile(patternPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create test pattern: %v", err)
		}
	}

	lib := NewLibrary(nil, tmpDir)

	// Filter by beginner difficulty
	filtered := lib.FilterByDifficulty("beginner")
	if len(filtered) != 2 {
		t.Errorf("Expected 2 beginner patterns, got %d", len(filtered))
	}

	// Filter by advanced difficulty
	filtered = lib.FilterByDifficulty("advanced")
	if len(filtered) != 1 {
		t.Errorf("Expected 1 advanced pattern, got %d", len(filtered))
	}
}

func TestLibrary_Search(t *testing.T) {
	tmpDir := t.TempDir()

	// Create patterns with searchable content
	pattern1 := `name: time_series_analysis
title: Time Series Analysis
description: Advanced pattern for analyzing time series data
category: analytics
difficulty: advanced
backend_type: sql
use_cases:
  - forecasting
  - trend analysis
`

	pattern2 := `name: data_quality_check
title: Data Quality Validation
description: Basic pattern for data quality checks
category: data_quality
difficulty: beginner
backend_type: sql
use_cases:
  - validation
  - integrity checks
`

	for name, content := range map[string]string{
		"time_series_analysis": pattern1,
		"data_quality_check":   pattern2,
	} {
		patternPath := filepath.Join(tmpDir, name+".yaml")
		if err := os.WriteFile(patternPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create test pattern: %v", err)
		}
	}

	lib := NewLibrary(nil, tmpDir)

	// Search by keyword in title
	results := lib.Search("time series")
	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'time series', got %d", len(results))
	}
	if len(results) > 0 && results[0].Name != "time_series_analysis" {
		t.Errorf("Expected 'time_series_analysis', got '%s'", results[0].Name)
	}

	// Search by keyword in use case
	results = lib.Search("validation")
	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'validation', got %d", len(results))
	}

	// Search by keyword in description
	results = lib.Search("quality")
	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'quality', got %d", len(results))
	}

	// Empty search should return all
	results = lib.Search("")
	if len(results) != 2 {
		t.Errorf("Expected 2 results for empty search, got %d", len(results))
	}
}

func TestLibrary_ClearCache(t *testing.T) {
	tmpDir := t.TempDir()

	pattern := `name: test_pattern
title: Test
description: Test
category: test
difficulty: beginner
backend_type: test
`

	patternPath := filepath.Join(tmpDir, "test_pattern.yaml")
	if err := os.WriteFile(patternPath, []byte(pattern), 0644); err != nil {
		t.Fatalf("Failed to create test pattern: %v", err)
	}

	lib := NewLibrary(nil, tmpDir)

	// Load pattern (adds to cache)
	_, err := lib.Load("test_pattern")
	if err != nil {
		t.Fatalf("Failed to load pattern: %v", err)
	}

	// List all (indexes patterns)
	_ = lib.ListAll()

	// Clear cache
	lib.ClearCache()

	// Verify cache is empty
	lib.mu.RLock()
	cacheSize := len(lib.patternCache)
	indexInit := lib.indexInitialized
	lib.mu.RUnlock()

	if cacheSize != 0 {
		t.Errorf("Expected empty cache after clear, got %d entries", cacheSize)
	}

	if indexInit {
		t.Error("Expected index to be uninitialized after clear")
	}
}

func TestLibrary_ConcurrentLoad(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test pattern
	pattern := `name: concurrent_test
title: Concurrent Test
description: Test concurrent access
category: test
difficulty: beginner
backend_type: test
`

	patternPath := filepath.Join(tmpDir, "concurrent_test.yaml")
	if err := os.WriteFile(patternPath, []byte(pattern), 0644); err != nil {
		t.Fatalf("Failed to create test pattern: %v", err)
	}

	lib := NewLibrary(nil, tmpDir)

	// Load pattern concurrently
	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			_, err := lib.Load("concurrent_test")
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent load error: %v", err)
	}
}

func TestLibrary_ConcurrentListAll(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test patterns
	for i := 0; i < 5; i++ {
		pattern := `name: pattern` + string(rune('0'+i)) + `
title: Pattern ` + string(rune('0'+i)) + `
description: Test
category: test
difficulty: beginner
backend_type: test
`
		patternPath := filepath.Join(tmpDir, "pattern"+string(rune('0'+i))+".yaml")
		if err := os.WriteFile(patternPath, []byte(pattern), 0644); err != nil {
			t.Fatalf("Failed to create test pattern: %v", err)
		}
	}

	lib := NewLibrary(nil, tmpDir)

	// List all concurrently
	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	results := make(chan int, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			summaries := lib.ListAll()
			results <- len(summaries)
		}()
	}

	wg.Wait()
	close(results)

	// All goroutines should get same count
	firstCount := -1
	for count := range results {
		if firstCount == -1 {
			firstCount = count
		} else if count != firstCount {
			t.Errorf("Inconsistent counts from concurrent ListAll: %d vs %d", firstCount, count)
		}
	}
}

func TestTruncateDescription(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		maxLen    int
		wantLen   int
		wantEllip bool
	}{
		{
			name:      "short string",
			input:     "short",
			maxLen:    100,
			wantLen:   5,
			wantEllip: false,
		},
		{
			name:      "exact length",
			input:     "exactly ten",
			maxLen:    11,
			wantLen:   11,
			wantEllip: false,
		},
		{
			name:      "needs truncation",
			input:     "this is a very long description that needs truncation",
			maxLen:    20,
			wantLen:   20,
			wantEllip: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateDescription(tt.input, tt.maxLen)

			if len(result) != tt.wantLen {
				t.Errorf("Expected length %d, got %d", tt.wantLen, len(result))
			}

			if tt.wantEllip && result[len(result)-3:] != "..." {
				t.Error("Expected ellipsis at end of truncated string")
			}

			if !tt.wantEllip && result != tt.input {
				t.Errorf("Expected unchanged string, got %q", result)
			}
		})
	}
}
