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
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"gopkg.in/yaml.v3"
)

// Library manages pattern loading, caching, and search.
// It supports both embedded patterns (compiled into binary) and filesystem patterns (loaded at runtime).
type Library struct {
	mu               sync.RWMutex
	patternCache     map[string]*Pattern
	patternIndex     []PatternSummary
	indexInitialized bool

	// Embedded patterns (optional)
	embeddedFS *embed.FS

	// Filesystem patterns (optional)
	patternsDir string

	// Pattern search paths within embedded FS or filesystem
	searchPaths []string

	// Observability
	tracer observability.Tracer
}

// NewLibrary creates a new pattern library.
// If embeddedFS is provided, patterns will be loaded from embedded filesystem.
// If patternsDir is provided, patterns will be loaded from filesystem.
// Both can be provided - embedded patterns are checked first, then filesystem.
func NewLibrary(embeddedFS *embed.FS, patternsDir string) *Library {
	return &Library{
		patternCache: make(map[string]*Pattern),
		embeddedFS:   embeddedFS,
		patternsDir:  patternsDir,
		searchPaths: []string{
			"analytics",
			"ml",
			"timeseries",
			"text",
			"data_quality",
			"rest_api",
			"document",
			"etl",
			"prompt_engineering",
			"code",
			"debugging",
			"vision",
			"evaluation",
		},
		tracer: observability.NewNoOpTracer(),
	}
}

// WithTracer sets the observability tracer for the library.
func (lib *Library) WithTracer(tracer observability.Tracer) *Library {
	lib.tracer = tracer
	return lib
}

// Load reads a pattern by name.
// Patterns are cached after first load for performance.
// Searches in order: cache → embedded FS → filesystem.
func (lib *Library) Load(name string) (*Pattern, error) {
	startTime := time.Now()
	_, span := lib.tracer.StartSpan(context.Background(), "patterns.library.load")
	defer lib.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("pattern.name", name)
	}

	// Check cache first
	lib.mu.RLock()
	cached, found := lib.patternCache[name]
	lib.mu.RUnlock()

	if found {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("cache.hit", "true")
			span.SetAttribute("source", "cache")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		}
		lib.tracer.RecordMetric("patterns.library.load", 1.0, map[string]string{
			"cache_hit": "true",
			"source":    "cache",
		})
		return cached, nil
	}

	// Try loading from embedded FS first
	if lib.embeddedFS != nil {
		pattern, err := lib.loadFromEmbedded(name)
		if err == nil {
			lib.cachePattern(name, pattern)
			duration := time.Since(startTime)
			if span != nil {
				span.SetAttribute("cache.hit", "false")
				span.SetAttribute("source", "embedded")
				span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			}
			lib.tracer.RecordMetric("patterns.library.load", 1.0, map[string]string{
				"cache_hit": "false",
				"source":    "embedded",
			})
			return pattern, nil
		}
	}

	// Fall back to filesystem
	if lib.patternsDir != "" {
		pattern, err := lib.loadFromFilesystem(name)
		if err == nil {
			lib.cachePattern(name, pattern)
			duration := time.Since(startTime)
			if span != nil {
				span.SetAttribute("cache.hit", "false")
				span.SetAttribute("source", "filesystem")
				span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			}
			lib.tracer.RecordMetric("patterns.library.load", 1.0, map[string]string{
				"cache_hit": "false",
				"source":    "filesystem",
			})
			return pattern, nil
		}
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("cache.hit", "false")
		span.SetAttribute("source", "not_found")
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		span.RecordError(fmt.Errorf("pattern not found: %s", name))
	}
	lib.tracer.RecordMetric("patterns.library.load", 1.0, map[string]string{
		"cache_hit": "false",
		"source":    "not_found",
		"error":     "true",
	})

	return nil, fmt.Errorf("pattern not found: %s", name)
}

// loadFromEmbedded loads a pattern from embedded filesystem.
func (lib *Library) loadFromEmbedded(name string) (*Pattern, error) {
	startTime := time.Now()
	_, span := lib.tracer.StartSpan(context.Background(), "patterns.library.load_embedded")
	defer lib.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("pattern.name", name)
	}

	// Try direct name first, then search in subdirectories
	possiblePaths := []string{name + ".yaml"}
	for _, searchPath := range lib.searchPaths {
		possiblePaths = append(possiblePaths, filepath.Join(searchPath, name+".yaml"))
	}

	if span != nil {
		span.SetAttribute("search.paths_checked", fmt.Sprintf("%d", len(possiblePaths)))
	}

	for _, path := range possiblePaths {
		data, err := lib.embeddedFS.ReadFile(path)
		if err == nil {
			var pattern Pattern
			if err := yaml.Unmarshal(data, &pattern); err != nil {
				if span != nil {
					span.RecordError(fmt.Errorf("failed to parse pattern %s: %w", name, err))
				}
				return nil, fmt.Errorf("failed to parse pattern %s: %w", name, err)
			}
			duration := time.Since(startTime)
			if span != nil {
				span.SetAttribute("pattern.path", path)
				span.SetAttribute("pattern.size_bytes", fmt.Sprintf("%d", len(data)))
				span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			}
			lib.tracer.RecordMetric("patterns.library.load_embedded", 1.0, map[string]string{
				"success": "true",
			})
			return &pattern, nil
		}
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		span.RecordError(fmt.Errorf("pattern not found in embedded FS: %s", name))
	}
	lib.tracer.RecordMetric("patterns.library.load_embedded", 1.0, map[string]string{
		"success": "false",
	})

	return nil, fmt.Errorf("pattern not found in embedded FS: %s", name)
}

// loadFromFilesystem loads a pattern from filesystem.
func (lib *Library) loadFromFilesystem(name string) (*Pattern, error) {
	startTime := time.Now()
	_, span := lib.tracer.StartSpan(context.Background(), "patterns.library.load_filesystem")
	defer lib.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("pattern.name", name)
	}

	// Try direct path first, then search in subdirectories
	possiblePaths := []string{filepath.Join(lib.patternsDir, name+".yaml")}
	for _, searchPath := range lib.searchPaths {
		possiblePaths = append(possiblePaths, filepath.Join(lib.patternsDir, searchPath, name+".yaml"))
	}

	if span != nil {
		span.SetAttribute("search.paths_checked", fmt.Sprintf("%d", len(possiblePaths)))
	}

	for _, path := range possiblePaths {
		data, err := os.ReadFile(path)
		if err == nil {
			var pattern Pattern
			if err := yaml.Unmarshal(data, &pattern); err != nil {
				if span != nil {
					span.RecordError(fmt.Errorf("failed to parse pattern %s: %w", name, err))
				}
				return nil, fmt.Errorf("failed to parse pattern %s: %w", name, err)
			}
			duration := time.Since(startTime)
			if span != nil {
				span.SetAttribute("pattern.path", path)
				span.SetAttribute("pattern.size_bytes", fmt.Sprintf("%d", len(data)))
				span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			}
			lib.tracer.RecordMetric("patterns.library.load_filesystem", 1.0, map[string]string{
				"success": "true",
			})
			return &pattern, nil
		}
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		span.RecordError(fmt.Errorf("pattern not found in filesystem: %s", name))
	}
	lib.tracer.RecordMetric("patterns.library.load_filesystem", 1.0, map[string]string{
		"success": "false",
	})

	return nil, fmt.Errorf("pattern not found in filesystem: %s", name)
}

// cachePattern stores a pattern in the cache.
func (lib *Library) cachePattern(name string, pattern *Pattern) {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	lib.patternCache[name] = pattern
}

// ListAll returns metadata for all available patterns.
// Results are cached for performance.
func (lib *Library) ListAll() []PatternSummary {
	startTime := time.Now()
	_, span := lib.tracer.StartSpan(context.Background(), "patterns.library.list_all")
	defer lib.tracer.EndSpan(span)

	lib.mu.RLock()
	if lib.indexInitialized {
		index := lib.patternIndex
		lib.mu.RUnlock()
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("index.cached", "true")
			span.SetAttribute("result.count", fmt.Sprintf("%d", len(index)))
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		}
		lib.tracer.RecordMetric("patterns.library.list_all", 1.0, map[string]string{
			"cached": "true",
		})
		return index
	}
	lib.mu.RUnlock()

	if span != nil {
		span.SetAttribute("index.cached", "false")
	}

	summaries := make([]PatternSummary, 0)

	// Load from embedded FS
	if lib.embeddedFS != nil {
		embeddedSummaries := lib.indexEmbedded()
		summaries = append(summaries, embeddedSummaries...)
		if span != nil {
			span.SetAttribute("index.embedded_count", fmt.Sprintf("%d", len(embeddedSummaries)))
		}
	}

	// Load from filesystem
	if lib.patternsDir != "" {
		fsSummaries := lib.indexFilesystem()
		summaries = append(summaries, fsSummaries...)
		if span != nil {
			span.SetAttribute("index.filesystem_count", fmt.Sprintf("%d", len(fsSummaries)))
		}
	}

	// Cache the index
	lib.mu.Lock()
	lib.patternIndex = summaries
	lib.indexInitialized = true
	lib.mu.Unlock()

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("result.count", fmt.Sprintf("%d", len(summaries)))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}
	lib.tracer.RecordMetric("patterns.library.list_all", 1.0, map[string]string{
		"cached":       "false",
		"result_count": fmt.Sprintf("%d", len(summaries)),
	})

	return summaries
}

// indexEmbedded indexes all patterns in embedded filesystem.
func (lib *Library) indexEmbedded() []PatternSummary {
	summaries := make([]PatternSummary, 0)

	err := fs.WalkDir(lib.embeddedFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}

		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		pattern, loadErr := lib.Load(name)
		if loadErr != nil {
			return nil // Skip patterns that fail to load
		}

		summaries = append(summaries, lib.createSummary(pattern))
		return nil
	})

	if err != nil {
		// Log error but continue
		return summaries
	}

	return summaries
}

// indexFilesystem indexes all patterns in filesystem directory.
func (lib *Library) indexFilesystem() []PatternSummary {
	summaries := make([]PatternSummary, 0)

	err := filepath.WalkDir(lib.patternsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}

		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}

		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		pattern, loadErr := lib.Load(name)
		if loadErr != nil {
			return nil // Skip patterns that fail to load
		}

		summaries = append(summaries, lib.createSummary(pattern))
		return nil
	})

	if err != nil {
		// Log error but continue
		return summaries
	}

	return summaries
}

// createSummary creates a PatternSummary from a full Pattern.
func (lib *Library) createSummary(pattern *Pattern) PatternSummary {
	return PatternSummary{
		Name:            pattern.Name,
		Title:           pattern.Title,
		Description:     truncateDescription(pattern.Description, 200),
		Category:        pattern.Category,
		Difficulty:      pattern.Difficulty,
		BackendType:     pattern.BackendType,
		UseCases:        pattern.UseCases,
		BackendFunction: pattern.BackendFunction,
	}
}

// FilterByCategory returns patterns matching the specified category.
func (lib *Library) FilterByCategory(category string) []PatternSummary {
	startTime := time.Now()
	_, span := lib.tracer.StartSpan(context.Background(), "patterns.library.filter_by_category")
	defer lib.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("filter.category", category)
	}

	all := lib.ListAll()
	if category == "" {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("filter.empty", "true")
			span.SetAttribute("result.count", fmt.Sprintf("%d", len(all)))
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		}
		lib.tracer.RecordMetric("patterns.library.filter_by_category", 1.0, map[string]string{
			"empty_filter": "true",
		})
		return all
	}

	filtered := make([]PatternSummary, 0)
	categoryLower := strings.ToLower(category)

	for _, p := range all {
		if strings.ToLower(p.Category) == categoryLower {
			filtered = append(filtered, p)
		}
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("filter.total_patterns", fmt.Sprintf("%d", len(all)))
		span.SetAttribute("result.count", fmt.Sprintf("%d", len(filtered)))
		span.SetAttribute("result.match_rate", fmt.Sprintf("%.2f", float64(len(filtered))/float64(len(all))))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}
	lib.tracer.RecordMetric("patterns.library.filter_by_category", 1.0, map[string]string{
		"empty_filter": "false",
		"category":     category,
		"result_count": fmt.Sprintf("%d", len(filtered)),
	})

	return filtered
}

// FilterByBackendType returns patterns for a specific backend type.
func (lib *Library) FilterByBackendType(backendType string) []PatternSummary {
	startTime := time.Now()
	_, span := lib.tracer.StartSpan(context.Background(), "patterns.library.filter_by_backend_type")
	defer lib.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("filter.backend_type", backendType)
	}

	all := lib.ListAll()
	if backendType == "" {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("filter.empty", "true")
			span.SetAttribute("result.count", fmt.Sprintf("%d", len(all)))
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		}
		lib.tracer.RecordMetric("patterns.library.filter_by_backend_type", 1.0, map[string]string{
			"empty_filter": "true",
		})
		return all
	}

	filtered := make([]PatternSummary, 0)
	backendLower := strings.ToLower(backendType)

	for _, p := range all {
		if strings.ToLower(p.BackendType) == backendLower {
			filtered = append(filtered, p)
		}
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("filter.total_patterns", fmt.Sprintf("%d", len(all)))
		span.SetAttribute("result.count", fmt.Sprintf("%d", len(filtered)))
		span.SetAttribute("result.match_rate", fmt.Sprintf("%.2f", float64(len(filtered))/float64(len(all))))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}
	lib.tracer.RecordMetric("patterns.library.filter_by_backend_type", 1.0, map[string]string{
		"empty_filter": "false",
		"backend_type": backendType,
		"result_count": fmt.Sprintf("%d", len(filtered)),
	})

	return filtered
}

// FilterByDifficulty returns patterns matching the specified difficulty level.
func (lib *Library) FilterByDifficulty(difficulty string) []PatternSummary {
	startTime := time.Now()
	_, span := lib.tracer.StartSpan(context.Background(), "patterns.library.filter_by_difficulty")
	defer lib.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("filter.difficulty", difficulty)
	}

	all := lib.ListAll()
	if difficulty == "" {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("filter.empty", "true")
			span.SetAttribute("result.count", fmt.Sprintf("%d", len(all)))
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		}
		lib.tracer.RecordMetric("patterns.library.filter_by_difficulty", 1.0, map[string]string{
			"empty_filter": "true",
		})
		return all
	}

	filtered := make([]PatternSummary, 0)
	difficultyLower := strings.ToLower(difficulty)

	for _, p := range all {
		if strings.ToLower(p.Difficulty) == difficultyLower {
			filtered = append(filtered, p)
		}
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("filter.total_patterns", fmt.Sprintf("%d", len(all)))
		span.SetAttribute("result.count", fmt.Sprintf("%d", len(filtered)))
		span.SetAttribute("result.match_rate", fmt.Sprintf("%.2f", float64(len(filtered))/float64(len(all))))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}
	lib.tracer.RecordMetric("patterns.library.filter_by_difficulty", 1.0, map[string]string{
		"empty_filter": "false",
		"difficulty":   difficulty,
		"result_count": fmt.Sprintf("%d", len(filtered)),
	})

	return filtered
}

// Search performs free-text search across pattern metadata.
func (lib *Library) Search(query string) []PatternSummary {
	startTime := time.Now()
	_, span := lib.tracer.StartSpan(context.Background(), "patterns.library.search")
	defer lib.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("search.query", query)
		span.SetAttribute("search.query_length", fmt.Sprintf("%d", len(query)))
	}

	if query == "" {
		all := lib.ListAll()
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("search.empty_query", "true")
			span.SetAttribute("result.count", fmt.Sprintf("%d", len(all)))
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		}
		lib.tracer.RecordMetric("patterns.library.search", 1.0, map[string]string{
			"empty_query": "true",
		})
		return all
	}

	all := lib.ListAll()
	results := make([]PatternSummary, 0)
	queryLower := strings.ToLower(query)

	if span != nil {
		span.SetAttribute("search.total_patterns", fmt.Sprintf("%d", len(all)))
	}

	for _, p := range all {
		// Search in name, title, description, function name
		if strings.Contains(strings.ToLower(p.Name), queryLower) ||
			strings.Contains(strings.ToLower(p.Title), queryLower) ||
			strings.Contains(strings.ToLower(p.Description), queryLower) ||
			strings.Contains(strings.ToLower(p.BackendFunction), queryLower) {
			results = append(results, p)
			continue
		}

		// Search in use cases
		for _, useCase := range p.UseCases {
			if strings.Contains(strings.ToLower(useCase), queryLower) {
				results = append(results, p)
				break
			}
		}
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("result.count", fmt.Sprintf("%d", len(results)))
		span.SetAttribute("result.match_rate", fmt.Sprintf("%.2f", float64(len(results))/float64(len(all))))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}
	lib.tracer.RecordMetric("patterns.library.search", 1.0, map[string]string{
		"empty_query":  "false",
		"result_count": fmt.Sprintf("%d", len(results)),
	})

	return results
}

// ClearCache clears the in-memory pattern cache.
// Useful for testing or hot-reloading patterns.
func (lib *Library) ClearCache() {
	startTime := time.Now()
	_, span := lib.tracer.StartSpan(context.Background(), "patterns.library.clear_cache")
	defer lib.tracer.EndSpan(span)

	lib.mu.Lock()
	cacheSize := len(lib.patternCache)
	indexSize := len(lib.patternIndex)

	lib.patternCache = make(map[string]*Pattern)
	lib.patternIndex = nil
	lib.indexInitialized = false
	lib.mu.Unlock()

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("cache.patterns_cleared", fmt.Sprintf("%d", cacheSize))
		span.SetAttribute("cache.index_entries_cleared", fmt.Sprintf("%d", indexSize))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}
	lib.tracer.RecordMetric("patterns.library.clear_cache", 1.0, map[string]string{
		"patterns_cleared": fmt.Sprintf("%d", cacheSize),
		"index_cleared":    fmt.Sprintf("%d", indexSize),
	})
}

// AddSearchPath adds a custom search path for pattern discovery.
func (lib *Library) AddSearchPath(path string) {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	lib.searchPaths = append(lib.searchPaths, path)
}

// truncateDescription truncates a description to maxLen characters with ellipsis.
func truncateDescription(desc string, maxLen int) string {
	if len(desc) <= maxLen {
		return desc
	}
	return desc[:maxLen-3] + "..."
}
