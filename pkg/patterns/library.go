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
	"sort"
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

	// Path cache: pattern name -> relative path (populated during indexing)
	pathCache map[string]string

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
		pathCache:    make(map[string]string),
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
			// Nested vendor directories
			"teradata/analytics",
			"teradata/ml",
			"teradata/timeseries",
			"teradata/data_quality",
			"teradata/data_loading",
			"teradata/data_modeling",
			"teradata/code_migration",
			"teradata/data_discovery",
			"teradata/text",
			"teradata/performance",
			"postgres/analytics",
			"sql/timeseries",
			"sql/data_quality",
			"sql/text",
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
	cachedPath := lib.pathCache[name]
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

	// Try path cache first (populated during indexing)
	if cachedPath != "" {
		if lib.embeddedFS != nil {
			data, err := lib.embeddedFS.ReadFile(cachedPath)
			if err == nil {
				pattern, err := lib.parsePattern(data, name, cachedPath)
				if err == nil {
					lib.cachePattern(name, pattern)
					duration := time.Since(startTime)
					if span != nil {
						span.SetAttribute("cache.hit", "false")
						span.SetAttribute("source", "embedded_path_cache")
						span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
					}
					lib.tracer.RecordMetric("patterns.library.load", 1.0, map[string]string{
						"cache_hit": "false",
						"source":    "embedded_path_cache",
					})
					return pattern, nil
				}
			}
		}
		if lib.patternsDir != "" {
			fullPath := filepath.Join(lib.patternsDir, cachedPath)
			// Validate path is within patternsDir (prevent path traversal)
			cleanPath := filepath.Clean(fullPath)
			cleanBase := filepath.Clean(lib.patternsDir)
			if !strings.HasPrefix(cleanPath, cleanBase) {
				return nil, fmt.Errorf("pattern path outside patterns directory: %s", name)
			}
			// #nosec G304 -- Path validated to be within patternsDir
			data, err := os.ReadFile(cleanPath)
			if err == nil {
				pattern, err := lib.parsePattern(data, name, cachedPath)
				if err == nil {
					lib.cachePattern(name, pattern)
					duration := time.Since(startTime)
					if span != nil {
						span.SetAttribute("cache.hit", "false")
						span.SetAttribute("source", "filesystem_path_cache")
						span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
					}
					lib.tracer.RecordMetric("patterns.library.load", 1.0, map[string]string{
						"cache_hit": "false",
						"source":    "filesystem_path_cache",
					})
					return pattern, nil
				}
			}
		}
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
			pattern, err := lib.parsePattern(data, name, path)
			if err != nil {
				if span != nil {
					span.RecordError(err)
				}
				return nil, err
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
			return pattern, nil
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
			relPath := path
			if lib.patternsDir != "" && strings.HasPrefix(path, lib.patternsDir) {
				relPath = strings.TrimPrefix(path, lib.patternsDir)
				relPath = strings.TrimPrefix(relPath, string(filepath.Separator))
			}
			pattern, err := lib.parsePattern(data, name, relPath)
			if err != nil {
				if span != nil {
					span.RecordError(err)
				}
				return nil, err
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
			return pattern, nil
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

// parsePattern parses a pattern from YAML data and caches its path.
func (lib *Library) parsePattern(data []byte, name string, relPath string) (*Pattern, error) {
	var pattern Pattern
	if err := yaml.Unmarshal(data, &pattern); err != nil {
		return nil, fmt.Errorf("failed to parse pattern %s: %w", name, err)
	}

	// Cache the path for future loads
	lib.mu.Lock()
	lib.pathCache[name] = relPath
	lib.mu.Unlock()

	return &pattern, nil
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

		// Cache the path for this pattern
		lib.mu.Lock()
		lib.pathCache[name] = path
		lib.mu.Unlock()

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

		// Compute relative path from patternsDir
		relPath := path
		if lib.patternsDir != "" && strings.HasPrefix(path, lib.patternsDir) {
			relPath = strings.TrimPrefix(path, lib.patternsDir)
			relPath = strings.TrimPrefix(relPath, string(filepath.Separator))
		}

		// Cache the path for this pattern
		lib.mu.Lock()
		lib.pathCache[name] = relPath
		lib.mu.Unlock()

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
// Tokenizes the query and matches individual keywords for better recall.
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

	// Tokenize query into keywords (split on whitespace and common separators)
	keywords := strings.FieldsFunc(queryLower, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == '-' || r == '_'
	})

	// Filter out common stop words
	stopWords := map[string]bool{
		"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
		"be": true, "by": true, "for": true, "from": true, "has": true, "he": true,
		"in": true, "is": true, "it": true, "its": true, "of": true, "on": true,
		"that": true, "the": true, "to": true, "was": true, "will": true, "with": true,
	}

	filteredKeywords := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		if !stopWords[kw] && len(kw) > 2 { // Skip stop words and very short terms
			filteredKeywords = append(filteredKeywords, kw)
		}
	}

	if span != nil {
		span.SetAttribute("search.total_patterns", fmt.Sprintf("%d", len(all)))
		span.SetAttribute("search.keywords", strings.Join(filteredKeywords, ","))
		span.SetAttribute("search.keyword_count", fmt.Sprintf("%d", len(filteredKeywords)))
	}

	// If no useful keywords after filtering, fall back to original query
	if len(filteredKeywords) == 0 {
		filteredKeywords = []string{queryLower}
	}

	// Track patterns with their match scores
	type scoredResult struct {
		pattern    PatternSummary
		matchCount int
		score      float64
	}
	scoredResults := make([]scoredResult, 0)

	for _, p := range all {
		// Build searchable text from pattern metadata
		searchText := strings.ToLower(fmt.Sprintf("%s %s %s %s",
			p.Name, p.Title, p.Description, p.BackendFunction))

		// Add use cases to searchable text
		for _, useCase := range p.UseCases {
			searchText += " " + strings.ToLower(useCase)
		}

		// Count keyword matches
		matchCount := 0
		for _, keyword := range filteredKeywords {
			if strings.Contains(searchText, keyword) {
				matchCount++
			}
		}

		// Pattern matches if it contains any of the keywords
		if matchCount > 0 {
			// Calculate relevance score
			score := float64(matchCount) / float64(len(filteredKeywords))

			// Boost score for matches in name/title (more important than description)
			nameLower := strings.ToLower(p.Name)
			titleLower := strings.ToLower(p.Title)
			for _, keyword := range filteredKeywords {
				if strings.Contains(nameLower, keyword) {
					score += 0.5
				}
				if strings.Contains(titleLower, keyword) {
					score += 0.3
				}
			}

			scoredResults = append(scoredResults, scoredResult{
				pattern:    p,
				matchCount: matchCount,
				score:      score,
			})
		}
	}

	// Sort by score (descending), then by match count
	sort.Slice(scoredResults, func(i, j int) bool {
		if scoredResults[i].score != scoredResults[j].score {
			return scoredResults[i].score > scoredResults[j].score
		}
		return scoredResults[i].matchCount > scoredResults[j].matchCount
	})

	// Extract sorted patterns
	for _, sr := range scoredResults {
		results = append(results, sr.pattern)
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
	lib.pathCache = make(map[string]string)
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
