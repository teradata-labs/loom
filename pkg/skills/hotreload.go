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

package skills

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

// SkillUpdateCallback is called when a skill file is created, modified, or deleted.
// Parameters: eventType (create/modify/delete/validation_failed), skillName, error (if validation failed).
type SkillUpdateCallback func(eventType string, skillName string, err error)

// HotReloadConfig configures hot-reload behavior for the skill library.
type HotReloadConfig struct {
	Enabled    bool                // Enable hot-reload watching
	DebounceMs int                 // Debounce delay in milliseconds (default: 500ms)
	Logger     *zap.Logger         // Logger for reload events
	OnUpdate   SkillUpdateCallback // Callback for skill updates (optional)
}

// HotReloader watches skill YAML files for changes and updates the library cache.
type HotReloader struct {
	library *Library
	watcher *fsnotify.Watcher
	config  HotReloadConfig
	logger  *zap.Logger
	tracer  observability.Tracer

	// Debouncer to handle rapid-fire changes
	debounceTimers map[string]*time.Timer
	mu             sync.Mutex

	// Lifecycle
	stopCh  chan struct{}
	doneCh  chan struct{}
	stopped bool
	stopMu  sync.Mutex
}

// NewHotReloader creates a new hot-reloader for the skill library.
// Returns an error if the library has no filesystem search paths configured.
func NewHotReloader(library *Library, config HotReloadConfig, tracer observability.Tracer) (*HotReloader, error) {
	if len(library.searchPaths) == 0 {
		return nil, fmt.Errorf("hot-reload requires at least one filesystem search path")
	}

	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}
	if config.DebounceMs == 0 {
		config.DebounceMs = 500
	}
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
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
		tracer:         tracer,
		debounceTimers: make(map[string]*time.Timer),
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
	}, nil
}

// Start begins watching skill directories for file changes.
// It watches all configured search paths on the library.
func (hr *HotReloader) Start(ctx context.Context) error {
	startTime := time.Now()
	_, span := hr.tracer.StartSpan(context.Background(), "skills.hotreload.start")
	defer hr.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("hotreload.enabled", fmt.Sprintf("%t", hr.config.Enabled))
		span.SetAttribute("hotreload.debounce_ms", fmt.Sprintf("%d", hr.config.DebounceMs))
	}

	if !hr.config.Enabled {
		hr.logger.Info("Hot-reload disabled for skills")
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		}
		hr.tracer.RecordMetric("skills.hotreload.start", 1.0, map[string]string{
			"enabled": "false",
		})
		return nil
	}

	// Watch all library search paths.
	watchedDirs := 0
	for _, dir := range hr.library.searchPaths {
		if err := hr.watcher.Add(dir); err != nil {
			hr.logger.Warn("Failed to watch skills directory",
				zap.String("path", dir),
				zap.Error(err))
			// Continue even if some directories don't exist yet.
		} else {
			watchedDirs++
		}
	}

	if watchedDirs == 0 {
		err := fmt.Errorf("no skill directories could be watched")
		if span != nil {
			span.RecordError(err)
		}
		return err
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("hotreload.watched_directories", fmt.Sprintf("%d", watchedDirs))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	hr.logger.Info("Started skill hot-reload watcher",
		zap.Int("watched_dirs", watchedDirs),
		zap.Int("debounce_ms", hr.config.DebounceMs))

	hr.tracer.RecordMetric("skills.hotreload.start", 1.0, map[string]string{
		"enabled":             "true",
		"watched_directories": fmt.Sprintf("%d", watchedDirs),
	})

	// Start watch loop.
	go hr.watchLoop(ctx)

	return nil
}

// Stop stops the hot-reload watcher. It is safe to call multiple times.
func (hr *HotReloader) Stop() error {
	hr.stopMu.Lock()
	defer hr.stopMu.Unlock()

	if hr.stopped {
		return nil
	}
	hr.stopped = true

	if !hr.config.Enabled {
		return nil
	}

	close(hr.stopCh)

	// Wait for the watch loop to finish with a timeout.
	select {
	case <-hr.doneCh:
		// Clean exit.
	case <-time.After(5 * time.Second):
		hr.logger.Warn("Skill hot-reload stop timed out")
	}

	return hr.watcher.Close()
}

// watchLoop processes filesystem events until stopped or context is cancelled.
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
			hr.logger.Error("Skill file watcher error", zap.Error(err))

		case <-hr.stopCh:
			hr.logger.Info("Stopping skill hot-reload watcher")
			return

		case <-ctx.Done():
			hr.logger.Info("Skill hot-reload context cancelled")
			return
		}
	}
}

// handleEvent filters and dispatches filesystem events for YAML files.
func (hr *HotReloader) handleEvent(event fsnotify.Event) {
	// Only watch YAML files.
	if !strings.HasSuffix(event.Name, ".yaml") && !strings.HasSuffix(event.Name, ".yml") {
		return
	}

	// Ignore temporary files (editors create these).
	base := filepath.Base(event.Name)
	if strings.Contains(base, ".tmp") ||
		strings.Contains(base, "~") ||
		strings.HasPrefix(base, ".") {
		return
	}

	// Debounce rapid changes (editor auto-save, multiple writes).
	hr.debounce(event.Name, func() {
		hr.reload(event)
	})
}

// debounce delays execution until changes settle, using a per-key timer.
func (hr *HotReloader) debounce(key string, callback func()) {
	hr.mu.Lock()
	defer hr.mu.Unlock()

	// Cancel existing timer for this key.
	if timer, exists := hr.debounceTimers[key]; exists {
		timer.Stop()
	}

	// Schedule new timer.
	delay := time.Duration(hr.config.DebounceMs) * time.Millisecond
	hr.debounceTimers[key] = time.AfterFunc(delay, func() {
		callback()
		hr.mu.Lock()
		delete(hr.debounceTimers, key)
		hr.mu.Unlock()
	})
}

// reload dispatches the event to the appropriate handler based on operation type.
func (hr *HotReloader) reload(event fsnotify.Event) {
	skillName := hr.extractSkillName(event.Name)

	hr.logger.Info("Skill file changed, reloading",
		zap.String("file", event.Name),
		zap.String("skill", skillName),
		zap.String("operation", event.Op.String()))

	switch {
	case event.Op&fsnotify.Write == fsnotify.Write:
		hr.handleModify(skillName, event.Name)

	case event.Op&fsnotify.Create == fsnotify.Create:
		hr.handleCreate(skillName, event.Name)

	case event.Op&fsnotify.Remove == fsnotify.Remove:
		hr.handleDelete(skillName, event.Name)

	case event.Op&fsnotify.Rename == fsnotify.Rename:
		// Treat rename as delete; the new name will trigger a Create event.
		hr.handleDelete(skillName, event.Name)
	}
}

// handleModify validates a modified skill file and removes it from cache so the
// next Load call re-reads from disk.
func (hr *HotReloader) handleModify(name, path string) {
	startTime := time.Now()
	_, span := hr.tracer.StartSpan(context.Background(), "skills.hotreload.modify")
	defer hr.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("skill.name", name)
		span.SetAttribute("skill.file", path)
	}

	// Validate before clearing cache.
	if err := hr.validateSkill(path); err != nil {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("validation.failed", "true")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}

		hr.logger.Error("Skill validation failed, skipping reload",
			zap.String("skill", name),
			zap.String("file", path),
			zap.Error(err))

		hr.tracer.RecordMetric("skills.hotreload.modify", 1.0, map[string]string{
			"validation_failed": "true",
		})

		if hr.config.OnUpdate != nil {
			hr.config.OnUpdate("validation_failed", name, err)
		}
		return
	}

	// Remove from cache; next Load will re-read from disk.
	hr.library.RemoveFromCache(name)

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("validation.failed", "false")
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	hr.logger.Info("Skill reloaded successfully",
		zap.String("skill", name))

	hr.tracer.RecordMetric("skills.hotreload.modify", 1.0, map[string]string{
		"validation_failed": "false",
		"success":           "true",
	})

	if hr.config.OnUpdate != nil {
		hr.config.OnUpdate("modify", name, nil)
	}
}

// handleCreate validates a newly created skill file and invalidates the library
// index so ListAll picks it up.
func (hr *HotReloader) handleCreate(name, path string) {
	startTime := time.Now()
	_, span := hr.tracer.StartSpan(context.Background(), "skills.hotreload.create")
	defer hr.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("skill.name", name)
		span.SetAttribute("skill.file", path)
	}

	// Validate new skill file.
	if err := hr.validateSkill(path); err != nil {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("validation.failed", "true")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}

		hr.logger.Error("New skill validation failed",
			zap.String("skill", name),
			zap.String("file", path),
			zap.Error(err))

		hr.tracer.RecordMetric("skills.hotreload.create", 1.0, map[string]string{
			"validation_failed": "true",
		})

		if hr.config.OnUpdate != nil {
			hr.config.OnUpdate("validation_failed", name, err)
		}
		return
	}

	// Invalidate the index so the new skill appears in ListAll.
	hr.library.InvalidateCache()

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("validation.failed", "false")
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	hr.logger.Info("New skill detected and indexed",
		zap.String("skill", name))

	hr.tracer.RecordMetric("skills.hotreload.create", 1.0, map[string]string{
		"validation_failed": "false",
		"success":           "true",
	})

	if hr.config.OnUpdate != nil {
		hr.config.OnUpdate("create", name, nil)
	}
}

// handleDelete removes a deleted skill from the library cache and invalidates
// the index.
func (hr *HotReloader) handleDelete(name, path string) {
	startTime := time.Now()
	_, span := hr.tracer.StartSpan(context.Background(), "skills.hotreload.delete")
	defer hr.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("skill.name", name)
		span.SetAttribute("skill.file", path)
	}

	// Remove from cache and invalidate index.
	hr.library.RemoveFromCache(name)

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	hr.logger.Info("Skill removed",
		zap.String("skill", name))

	hr.tracer.RecordMetric("skills.hotreload.delete", 1.0, map[string]string{
		"success": "true",
	})

	if hr.config.OnUpdate != nil {
		hr.config.OnUpdate("delete", name, nil)
	}
}

// validateSkill attempts to load and validate a skill file.
// Returns an error if the file cannot be parsed or fails validation.
func (hr *HotReloader) validateSkill(path string) error {
	startTime := time.Now()
	_, span := hr.tracer.StartSpan(context.Background(), "skills.hotreload.validate")
	defer hr.tracer.EndSpan(span)

	skillName := hr.extractSkillName(path)
	if span != nil {
		span.SetAttribute("skill.name", skillName)
		span.SetAttribute("skill.file", path)
	}

	// Try loading the skill (this runs YAML parse + validateSkillYAML).
	skill, err := LoadSkill(path)
	if err != nil {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("validation.result", "load_failed")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}
		hr.tracer.RecordMetric("skills.hotreload.validate", 1.0, map[string]string{
			"result": "load_failed",
		})
		return fmt.Errorf("failed to load skill: %w", err)
	}

	// Verify required fields that LoadSkill may not fully check.
	if skill.Name == "" {
		err := fmt.Errorf("skill.name is required")
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("validation.result", "missing_name")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}
		hr.tracer.RecordMetric("skills.hotreload.validate", 1.0, map[string]string{
			"result": "missing_name",
		})
		return err
	}

	if skill.Domain == "" {
		err := fmt.Errorf("skill.domain is required")
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("validation.result", "missing_domain")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}
		hr.tracer.RecordMetric("skills.hotreload.validate", 1.0, map[string]string{
			"result": "missing_domain",
		})
		return err
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("validation.result", "success")
		span.SetAttribute("skill.domain", skill.Domain)
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}
	hr.tracer.RecordMetric("skills.hotreload.validate", 1.0, map[string]string{
		"result": "success",
	})

	return nil
}

// extractSkillName extracts the skill name from a file path by removing the directory
// and extension components.
func (hr *HotReloader) extractSkillName(filePath string) string {
	base := filepath.Base(filePath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
