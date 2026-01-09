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
package artifacts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// ArtifactUpdateCallback is called when an artifact is created, modified, or deleted.
// Parameters: artifact metadata, event type (create/modify/delete).
type ArtifactUpdateCallback func(artifact *Artifact, eventType string)

// WatcherConfig configures hot-reload behavior for artifact directory.
type WatcherConfig struct {
	Enabled    bool                   // Enable hot-reload
	DebounceMs int                    // Debounce delay in milliseconds (default: 500ms)
	Logger     *zap.Logger            // Logger for events
	OnCreate   ArtifactUpdateCallback // Callback for new artifacts (optional)
	OnModify   ArtifactUpdateCallback // Callback for modified artifacts (optional)
	OnDelete   ArtifactUpdateCallback // Callback for deleted artifacts (optional)
}

// Watcher manages hot-reload for artifacts directory.
type Watcher struct {
	store        ArtifactStore
	analyzer     *Analyzer
	watcher      *fsnotify.Watcher
	artifactsDir string
	config       WatcherConfig
	logger       *zap.Logger
	tracer       observability.Tracer

	// Debouncer to handle rapid-fire changes (e.g., editor auto-save)
	debounceTimers map[string]*time.Timer
	debounceMu     sync.Mutex

	// Lifecycle
	stopCh  chan struct{}
	doneCh  chan struct{}
	stopped bool
	stopMu  sync.Mutex
}

// NewWatcher creates a new hot-reload watcher for the artifacts directory.
func NewWatcher(store ArtifactStore, config WatcherConfig) (*Watcher, error) {
	artifactsDir, err := GetArtifactsDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get artifacts directory: %w", err)
	}

	// Ensure artifacts directory exists
	if err := os.MkdirAll(artifactsDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create artifacts directory: %w", err)
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

	return &Watcher{
		store:          store,
		analyzer:       NewAnalyzer(),
		watcher:        watcher,
		artifactsDir:   artifactsDir,
		config:         config,
		logger:         config.Logger,
		tracer:         observability.NewNoOpTracer(),
		debounceTimers: make(map[string]*time.Timer),
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
	}, nil
}

// WithTracer sets the observability tracer for the watcher.
func (w *Watcher) WithTracer(tracer observability.Tracer) *Watcher {
	w.tracer = tracer
	return w
}

// Start begins watching for artifact file changes.
func (w *Watcher) Start(ctx context.Context) error {
	startTime := time.Now()
	_, span := w.tracer.StartSpan(context.Background(), "artifacts.watcher.start")
	defer w.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("watcher.enabled", fmt.Sprintf("%t", w.config.Enabled))
		span.SetAttribute("watcher.artifacts_dir", w.artifactsDir)
		span.SetAttribute("watcher.debounce_ms", fmt.Sprintf("%d", w.config.DebounceMs))
	}

	if !w.config.Enabled {
		w.logger.Info("Hot-reload disabled for artifacts")
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		}
		close(w.doneCh)
		return nil
	}

	// Add artifacts directory to watcher
	if err := w.watcher.Add(w.artifactsDir); err != nil {
		return fmt.Errorf("failed to watch artifacts directory: %w", err)
	}

	w.logger.Info("Artifact hot-reload started",
		zap.String("directory", w.artifactsDir),
		zap.Int("debounce_ms", w.config.DebounceMs))

	// Start watch loop in goroutine
	go w.watchLoop(ctx)

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	return nil
}

// Stop stops the watcher.
func (w *Watcher) Stop() error {
	w.stopMu.Lock()
	defer w.stopMu.Unlock()

	if w.stopped {
		return nil
	}

	w.stopped = true
	close(w.stopCh)
	<-w.doneCh // Wait for watch loop to finish

	if w.watcher != nil {
		return w.watcher.Close()
	}

	return nil
}

// watchLoop is the main event loop for file watching.
func (w *Watcher) watchLoop(ctx context.Context) {
	defer close(w.doneCh)

	w.logger.Info("Artifact watcher loop started")

	for {
		select {
		case <-w.stopCh:
			w.logger.Info("Artifact watcher stopped")
			return

		case <-ctx.Done():
			w.logger.Info("Artifact watcher context cancelled")
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				w.logger.Warn("Artifact watcher events channel closed")
				return
			}
			w.handleEvent(ctx, event)

		case err, ok := <-w.watcher.Errors:
			if !ok {
				w.logger.Warn("Artifact watcher errors channel closed")
				return
			}
			w.logger.Error("Artifact watcher error", zap.Error(err))
		}
	}
}

// handleEvent processes a single fsnotify event.
func (w *Watcher) handleEvent(ctx context.Context, event fsnotify.Event) {
	// Ignore hidden files and non-artifact files
	filename := filepath.Base(event.Name)
	if filename[0] == '.' || filename == "metadata.json" {
		return
	}

	// Ignore directories
	info, err := os.Stat(event.Name)
	if err == nil && info.IsDir() {
		return
	}

	// Debounce rapid changes (e.g., editor auto-save)
	w.debounceEvent(ctx, event)
}

// debounceEvent debounces rapid file changes.
func (w *Watcher) debounceEvent(ctx context.Context, event fsnotify.Event) {
	w.debounceMu.Lock()
	defer w.debounceMu.Unlock()

	// Cancel existing timer for this file
	if timer, exists := w.debounceTimers[event.Name]; exists {
		timer.Stop()
	}

	// Create new timer
	w.debounceTimers[event.Name] = time.AfterFunc(
		time.Duration(w.config.DebounceMs)*time.Millisecond,
		func() {
			w.processEvent(ctx, event)

			// Cleanup timer
			w.debounceMu.Lock()
			delete(w.debounceTimers, event.Name)
			w.debounceMu.Unlock()
		},
	)
}

// processEvent processes a debounced file event.
func (w *Watcher) processEvent(ctx context.Context, event fsnotify.Event) {
	_, span := w.tracer.StartSpan(ctx, "artifacts.watcher.process_event")
	defer w.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("event.name", event.Name)
		span.SetAttribute("event.op", event.Op.String())
	}

	switch {
	case event.Op&fsnotify.Create == fsnotify.Create:
		w.handleCreate(ctx, event.Name, span)
	case event.Op&fsnotify.Write == fsnotify.Write:
		w.handleModify(ctx, event.Name, span)
	case event.Op&fsnotify.Remove == fsnotify.Remove:
		w.handleDelete(ctx, event.Name, span)
	case event.Op&fsnotify.Rename == fsnotify.Rename:
		// Rename is treated as delete (old name) + create (new name)
		w.handleDelete(ctx, event.Name, span)
	}
}

// handleCreate handles new file creation.
func (w *Watcher) handleCreate(ctx context.Context, path string, span *observability.Span) {
	w.logger.Info("New artifact detected", zap.String("path", path))

	// Analyze file
	result, err := w.analyzer.Analyze(path)
	if err != nil {
		w.logger.Error("Failed to analyze artifact",
			zap.String("path", path),
			zap.Error(err))
		if span != nil {
			span.RecordError(err)
		}
		return
	}

	// Create artifact metadata
	now := time.Now()
	artifact := &Artifact{
		ID:          GenerateArtifactID(),
		Name:        filepath.Base(path),
		Path:        path,
		Source:      SourceUser, // Assume user-created files
		ContentType: result.ContentType,
		SizeBytes:   result.SizeBytes,
		Checksum:    result.Checksum,
		CreatedAt:   now,
		UpdatedAt:   now,
		Tags:        result.Tags,
		Metadata:    result.Metadata,
	}

	// Index in database
	if err := w.store.Index(ctx, artifact); err != nil {
		w.logger.Error("Failed to index artifact",
			zap.String("name", artifact.Name),
			zap.Error(err))
		if span != nil {
			span.RecordError(err)
		}
		return
	}

	w.logger.Info("Artifact indexed",
		zap.String("id", artifact.ID),
		zap.String("name", artifact.Name),
		zap.String("content_type", artifact.ContentType),
		zap.Int64("size_bytes", artifact.SizeBytes))

	// Call callback if provided
	if w.config.OnCreate != nil {
		w.config.OnCreate(artifact, "create")
	}
}

// handleModify handles file modifications.
func (w *Watcher) handleModify(ctx context.Context, path string, span *observability.Span) {
	w.logger.Info("Artifact modified", zap.String("path", path))

	// Get existing artifact by path
	filename := filepath.Base(path)
	existing, err := w.store.GetByName(ctx, filename)
	if err != nil {
		// If not found, treat as create
		w.handleCreate(ctx, path, span)
		return
	}

	// Re-analyze file
	result, err := w.analyzer.Analyze(path)
	if err != nil {
		w.logger.Error("Failed to analyze modified artifact",
			zap.String("path", path),
			zap.Error(err))
		if span != nil {
			span.RecordError(err)
		}
		return
	}

	// Update artifact metadata
	now := time.Now()
	existing.SizeBytes = result.SizeBytes
	existing.Checksum = result.Checksum
	existing.ContentType = result.ContentType
	existing.UpdatedAt = now
	existing.Tags = result.Tags
	existing.Metadata = result.Metadata

	// Update in database
	if err := w.store.Update(ctx, existing); err != nil {
		w.logger.Error("Failed to update artifact",
			zap.String("name", existing.Name),
			zap.Error(err))
		if span != nil {
			span.RecordError(err)
		}
		return
	}

	w.logger.Info("Artifact updated",
		zap.String("id", existing.ID),
		zap.String("name", existing.Name),
		zap.String("checksum", existing.Checksum))

	// Call callback if provided
	if w.config.OnModify != nil {
		w.config.OnModify(existing, "modify")
	}
}

// handleDelete handles file deletions.
func (w *Watcher) handleDelete(ctx context.Context, path string, span *observability.Span) {
	w.logger.Debug("Artifact file removed", zap.String("path", path))

	// Get existing artifact by path
	filename := filepath.Base(path)
	existing, err := w.store.GetByName(ctx, filename)
	if err != nil {
		// Artifact not indexed yet - this can happen if file was deleted quickly
		// after creation, or if it was never a tracked artifact. Not an error.
		w.logger.Debug("File removed but not found in artifact index (may not have been indexed)",
			zap.String("path", path))
		return
	}

	// Soft delete
	if err := w.store.Delete(ctx, existing.ID, false); err != nil {
		w.logger.Error("Failed to soft-delete artifact",
			zap.String("id", existing.ID),
			zap.Error(err))
		if span != nil {
			span.RecordError(err)
		}
		return
	}

	w.logger.Info("Artifact soft-deleted",
		zap.String("id", existing.ID),
		zap.String("name", existing.Name))

	// Call callback if provided
	if w.config.OnDelete != nil {
		w.config.OnDelete(existing, "delete")
	}
}
