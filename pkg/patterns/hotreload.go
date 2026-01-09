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
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// PatternUpdateCallback is called when a pattern is created, modified, or deleted.
// Parameters: eventType (create/modify/delete), patternName, filePath, error (if validation failed).
type PatternUpdateCallback func(eventType string, patternName string, filePath string, err error)

// HotReloadConfig configures hot-reload behavior for pattern library.
type HotReloadConfig struct {
	Enabled    bool                  // Enable hot-reload
	DebounceMs int                   // Debounce delay in milliseconds (default: 500ms)
	Logger     *zap.Logger           // Logger for reload events
	OnUpdate   PatternUpdateCallback // Callback for pattern updates (optional)
}

// HotReloader manages hot-reload for pattern library files.
type HotReloader struct {
	library *Library
	watcher *fsnotify.Watcher
	config  HotReloadConfig
	logger  *zap.Logger
	tracer  observability.Tracer

	// Debouncer to handle rapid-fire changes
	debounceTimers map[string]*time.Timer
	debounceMu     sync.Mutex

	// Lifecycle
	stopCh  chan struct{}
	doneCh  chan struct{}
	stopped bool
	stopMu  sync.Mutex
}

// NewHotReloader creates a new hot-reloader for the pattern library.
func NewHotReloader(library *Library, config HotReloadConfig) (*HotReloader, error) {
	if library.patternsDir == "" {
		return nil, fmt.Errorf("hot-reload requires filesystem patterns directory")
	}

	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}
	if config.DebounceMs == 0 {
		config.DebounceMs = 500
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create file watcher: %w", err)
	}

	return &HotReloader{
		library:        library,
		watcher:        watcher,
		config:         config,
		logger:         config.Logger,
		tracer:         observability.NewNoOpTracer(),
		debounceTimers: make(map[string]*time.Timer),
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
	}, nil
}

// WithTracer sets the observability tracer for the hot-reloader.
func (hr *HotReloader) WithTracer(tracer observability.Tracer) *HotReloader {
	hr.tracer = tracer
	return hr
}

// Start begins watching for pattern file changes.
func (hr *HotReloader) Start(ctx context.Context) error {
	startTime := time.Now()
	_, span := hr.tracer.StartSpan(context.Background(), "patterns.hotreload.start")
	defer hr.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("hotreload.enabled", fmt.Sprintf("%t", hr.config.Enabled))
		span.SetAttribute("hotreload.patterns_dir", hr.library.patternsDir)
		span.SetAttribute("hotreload.debounce_ms", fmt.Sprintf("%d", hr.config.DebounceMs))
	}

	if !hr.config.Enabled {
		hr.logger.Info("Hot-reload disabled for patterns")
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		}
		hr.tracer.RecordMetric("patterns.hotreload.start", 1.0, map[string]string{
			"enabled": "false",
		})
		return nil
	}

	// Add pattern directory to watcher
	if err := hr.watcher.Add(hr.library.patternsDir); err != nil {
		if span != nil {
			span.RecordError(err)
		}
		return fmt.Errorf("failed to watch patterns directory: %w", err)
	}

	// Watch all subdirectories (analytics, ml, etc.)
	watchedDirs := 1 // Main directory
	for _, searchPath := range hr.library.searchPaths {
		subDir := filepath.Join(hr.library.patternsDir, searchPath)
		if err := hr.watcher.Add(subDir); err != nil {
			hr.logger.Warn("Failed to watch pattern subdirectory",
				zap.String("path", subDir),
				zap.Error(err))
			// Continue even if some subdirectories don't exist
		} else {
			watchedDirs++
		}
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("hotreload.watched_directories", fmt.Sprintf("%d", watchedDirs))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	hr.logger.Info("Started pattern hot-reload watcher",
		zap.String("patterns_dir", hr.library.patternsDir),
		zap.Int("debounce_ms", hr.config.DebounceMs))

	hr.tracer.RecordMetric("patterns.hotreload.start", 1.0, map[string]string{
		"enabled":             "true",
		"watched_directories": fmt.Sprintf("%d", watchedDirs),
	})

	// Start watch loop
	go hr.watchLoop(ctx)

	return nil
}

// watchLoop processes file system events.
func (hr *HotReloader) watchLoop(ctx context.Context) {
	defer close(hr.doneCh)

	for {
		select {
		case event, ok := <-hr.watcher.Events:
			if !ok {
				return
			}

			hr.handleEvent(event)

		case err, ok := <-hr.watcher.Errors:
			if !ok {
				return
			}
			hr.logger.Error("File watcher error", zap.Error(err))

		case <-hr.stopCh:
			hr.logger.Info("Stopping pattern hot-reload watcher")
			return

		case <-ctx.Done():
			hr.logger.Info("Pattern hot-reload context cancelled")
			return
		}
	}
}

// handleEvent processes a filesystem event.
func (hr *HotReloader) handleEvent(event fsnotify.Event) {
	// Only watch YAML files
	if !strings.HasSuffix(event.Name, ".yaml") && !strings.HasSuffix(event.Name, ".yml") {
		return
	}

	// Ignore temporary files (editors create these)
	if strings.Contains(filepath.Base(event.Name), ".tmp") ||
		strings.Contains(filepath.Base(event.Name), "~") ||
		strings.HasPrefix(filepath.Base(event.Name), ".") {
		return
	}

	// Debounce rapid changes (editor auto-save, multiple files)
	hr.debounce(event.Name, func() {
		hr.reload(event)
	})
}

// debounce delays execution until changes settle.
func (hr *HotReloader) debounce(key string, callback func()) {
	hr.debounceMu.Lock()
	defer hr.debounceMu.Unlock()

	// Cancel existing timer
	if timer, exists := hr.debounceTimers[key]; exists {
		timer.Stop()
	}

	// Schedule new timer
	delay := time.Duration(hr.config.DebounceMs) * time.Millisecond
	hr.debounceTimers[key] = time.AfterFunc(delay, func() {
		callback()
		hr.debounceMu.Lock()
		delete(hr.debounceTimers, key)
		hr.debounceMu.Unlock()
	})
}

// reload handles the actual reload operation.
func (hr *HotReloader) reload(event fsnotify.Event) {
	patternName := hr.extractPatternName(event.Name)

	hr.logger.Info("Pattern file changed, reloading",
		zap.String("file", event.Name),
		zap.String("pattern", patternName),
		zap.String("operation", event.Op.String()))

	switch {
	case event.Op&fsnotify.Write == fsnotify.Write:
		// File modified
		hr.handleModify(patternName, event.Name)

	case event.Op&fsnotify.Create == fsnotify.Create:
		// New file created
		hr.handleCreate(patternName, event.Name)

	case event.Op&fsnotify.Remove == fsnotify.Remove:
		// File deleted
		hr.handleDelete(patternName, event.Name)

	case event.Op&fsnotify.Rename == fsnotify.Rename:
		// File renamed (treat as delete)
		hr.handleDelete(patternName, event.Name)
	}
}

// handleModify reloads a modified pattern.
func (hr *HotReloader) handleModify(patternName, filePath string) {
	startTime := time.Now()
	_, span := hr.tracer.StartSpan(context.Background(), "patterns.hotreload.modify")
	defer hr.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("pattern.name", patternName)
		span.SetAttribute("pattern.file", filePath)
	}

	// Validate pattern before clearing cache
	if err := hr.validatePattern(filePath); err != nil {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("validation.failed", "true")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}

		hr.logger.Error("Pattern validation failed, skipping reload",
			zap.String("pattern", patternName),
			zap.String("file", filePath),
			zap.Error(err))

		hr.tracer.RecordMetric("patterns.hotreload.modify", 1.0, map[string]string{
			"validation_failed": "true",
		})

		// Notify callback of validation failure
		if hr.config.OnUpdate != nil {
			hr.config.OnUpdate("validation_failed", patternName, filePath, err)
		}
		return
	}

	// Remove from cache - will be lazy-loaded on next access
	hr.library.mu.Lock()
	delete(hr.library.patternCache, patternName)
	// Clear index to force re-indexing
	hr.library.indexInitialized = false
	hr.library.mu.Unlock()

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("validation.failed", "false")
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	hr.logger.Info("Pattern reloaded successfully",
		zap.String("pattern", patternName))

	hr.tracer.RecordMetric("patterns.hotreload.modify", 1.0, map[string]string{
		"validation_failed": "false",
		"success":           "true",
	})

	// Notify callback of modification
	if hr.config.OnUpdate != nil {
		hr.config.OnUpdate("modify", patternName, filePath, nil)
	}
}

// handleCreate adds a new pattern to the library.
func (hr *HotReloader) handleCreate(patternName, filePath string) {
	startTime := time.Now()
	_, span := hr.tracer.StartSpan(context.Background(), "patterns.hotreload.create")
	defer hr.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("pattern.name", patternName)
		span.SetAttribute("pattern.file", filePath)
	}

	// Validate new pattern
	if err := hr.validatePattern(filePath); err != nil {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("validation.failed", "true")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}

		hr.logger.Error("New pattern validation failed",
			zap.String("pattern", patternName),
			zap.String("file", filePath),
			zap.Error(err))

		hr.tracer.RecordMetric("patterns.hotreload.create", 1.0, map[string]string{
			"validation_failed": "true",
		})

		// Notify callback of validation failure
		if hr.config.OnUpdate != nil {
			hr.config.OnUpdate("validation_failed", patternName, filePath, err)
		}
		return
	}

	// Clear index to include new pattern
	hr.library.mu.Lock()
	hr.library.indexInitialized = false
	hr.library.mu.Unlock()

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("validation.failed", "false")
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	hr.logger.Info("New pattern detected and indexed",
		zap.String("pattern", patternName))

	hr.tracer.RecordMetric("patterns.hotreload.create", 1.0, map[string]string{
		"validation_failed": "false",
		"success":           "true",
	})

	// Notify callback of creation
	if hr.config.OnUpdate != nil {
		hr.config.OnUpdate("create", patternName, filePath, nil)
	}
}

// handleDelete removes a pattern from cache.
func (hr *HotReloader) handleDelete(patternName, filePath string) {
	startTime := time.Now()
	_, span := hr.tracer.StartSpan(context.Background(), "patterns.hotreload.delete")
	defer hr.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("pattern.name", patternName)
		span.SetAttribute("pattern.file", filePath)
	}

	hr.library.mu.Lock()
	_, existed := hr.library.patternCache[patternName]
	delete(hr.library.patternCache, patternName)
	hr.library.indexInitialized = false
	hr.library.mu.Unlock()

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("pattern.existed_in_cache", fmt.Sprintf("%t", existed))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	hr.logger.Info("Pattern removed",
		zap.String("pattern", patternName))

	hr.tracer.RecordMetric("patterns.hotreload.delete", 1.0, map[string]string{
		"existed_in_cache": fmt.Sprintf("%t", existed),
	})

	// Notify callback of deletion
	if hr.config.OnUpdate != nil {
		hr.config.OnUpdate("delete", patternName, filePath, nil)
	}
}

// validatePattern validates a pattern file before reload.
func (hr *HotReloader) validatePattern(filePath string) error {
	startTime := time.Now()
	_, span := hr.tracer.StartSpan(context.Background(), "patterns.hotreload.validate")
	defer hr.tracer.EndSpan(span)

	patternName := hr.extractPatternName(filePath)
	if span != nil {
		span.SetAttribute("pattern.name", patternName)
		span.SetAttribute("pattern.file", filePath)
	}

	// Try loading the pattern
	// Temporarily bypass cache for validation
	pattern, err := hr.library.loadFromFilesystem(patternName)
	if err != nil {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("validation.result", "load_failed")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}
		hr.tracer.RecordMetric("patterns.hotreload.validate", 1.0, map[string]string{
			"result": "load_failed",
		})
		return fmt.Errorf("failed to load pattern: %w", err)
	}

	// Validate required fields
	if pattern.Name == "" {
		err := fmt.Errorf("pattern.name is required")
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("validation.result", "missing_name")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}
		hr.tracer.RecordMetric("patterns.hotreload.validate", 1.0, map[string]string{
			"result": "missing_name",
		})
		return err
	}
	if pattern.Category == "" {
		err := fmt.Errorf("pattern.category is required")
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("validation.result", "missing_category")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}
		hr.tracer.RecordMetric("patterns.hotreload.validate", 1.0, map[string]string{
			"result": "missing_category",
		})
		return err
	}

	// Additional validation based on pattern type
	hasWarnings := false
	if pattern.BackendFunction != "" {
		// SQL/backend patterns should have templates or examples
		if len(pattern.Templates) == 0 && len(pattern.Examples) == 0 {
			hasWarnings = true
			hr.logger.Warn("Pattern has backend_function but no templates or examples",
				zap.String("pattern", pattern.Name))
		}
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("validation.result", "success")
		span.SetAttribute("validation.has_warnings", fmt.Sprintf("%t", hasWarnings))
		span.SetAttribute("pattern.category", pattern.Category)
		span.SetAttribute("pattern.has_backend_function", fmt.Sprintf("%t", pattern.BackendFunction != ""))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}
	hr.tracer.RecordMetric("patterns.hotreload.validate", 1.0, map[string]string{
		"result":       "success",
		"has_warnings": fmt.Sprintf("%t", hasWarnings),
	})

	return nil
}

// extractPatternName extracts the pattern name from file path.
func (hr *HotReloader) extractPatternName(filePath string) string {
	base := filepath.Base(filePath)
	// Remove extension (.yaml or .yml)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return name
}

// Stop stops the hot-reload watcher.
func (hr *HotReloader) Stop() error {
	hr.stopMu.Lock()
	defer hr.stopMu.Unlock()

	// Idempotent - can call multiple times safely
	if hr.stopped {
		return nil
	}
	hr.stopped = true

	if !hr.config.Enabled {
		return nil
	}

	close(hr.stopCh)

	// Wait for watch loop to finish (with timeout)
	select {
	case <-hr.doneCh:
		// Clean exit
	case <-time.After(5 * time.Second):
		hr.logger.Warn("Hot-reload stop timed out")
	}

	return hr.watcher.Close()
}

// ManualReload triggers a manual reload of a specific pattern.
// Useful for programmatic reload (e.g., after API-based pattern creation).
func (hr *HotReloader) ManualReload(patternName string) error {
	startTime := time.Now()
	_, span := hr.tracer.StartSpan(context.Background(), "patterns.hotreload.manual_reload")
	defer hr.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("pattern.name", patternName)
	}

	hr.logger.Info("Manual pattern reload triggered",
		zap.String("pattern", patternName))

	// Find pattern file
	possiblePaths := []string{filepath.Join(hr.library.patternsDir, patternName+".yaml")}
	for _, searchPath := range hr.library.searchPaths {
		possiblePaths = append(possiblePaths,
			filepath.Join(hr.library.patternsDir, searchPath, patternName+".yaml"))
	}

	if span != nil {
		span.SetAttribute("search.paths_checked", fmt.Sprintf("%d", len(possiblePaths)))
	}

	var filePath string
	for _, path := range possiblePaths {
		if _, err := filepath.Glob(path); err == nil {
			filePath = path
			break
		}
	}

	if filePath == "" {
		err := fmt.Errorf("pattern file not found: %s", patternName)
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("result", "file_not_found")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}
		hr.tracer.RecordMetric("patterns.hotreload.manual_reload", 1.0, map[string]string{
			"result": "file_not_found",
		})
		return err
	}

	if span != nil {
		span.SetAttribute("pattern.file", filePath)
	}

	// Validate and reload
	if err := hr.validatePattern(filePath); err != nil {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("result", "validation_failed")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}
		hr.tracer.RecordMetric("patterns.hotreload.manual_reload", 1.0, map[string]string{
			"result": "validation_failed",
		})
		return fmt.Errorf("validation failed: %w", err)
	}

	hr.library.mu.Lock()
	delete(hr.library.patternCache, patternName)
	hr.library.indexInitialized = false
	hr.library.mu.Unlock()

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("result", "success")
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	hr.logger.Info("Manual reload completed",
		zap.String("pattern", patternName))

	hr.tracer.RecordMetric("patterns.hotreload.manual_reload", 1.0, map[string]string{
		"result": "success",
	})

	return nil
}
