---
title: "Pattern Hot-Reload"
weight: 30
---

# Pattern Hot-Reload Reference

**Version**: v1.0.0-beta.1

Complete technical reference for Loom's pattern hot-reload system - zero-downtime pattern updates via filesystem watching.

---

## Table of Contents

- [Quick Reference](#quick-reference)
- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Features](#features)
- [Configuration](#configuration)
- [Performance Characteristics](#performance-characteristics)
- [Reload Lifecycle](#reload-lifecycle)
- [Debounce Behavior](#debounce-behavior)
- [Validation](#validation)
- [Callback System](#callback-system)
- [Error Handling](#error-handling)
- [Error Codes](#error-codes)
- [Testing](#testing)
- [Best Practices](#best-practices)
- [Limitations](#limitations)
- [Troubleshooting](#troubleshooting)
- [See Also](#see-also)

---

## Quick Reference

### Configuration Summary

```go
// Enable hot-reload with defaults
config := patterns.HotReloadConfig{
    Enabled:    true,
    DebounceMs: 500,  // Default: 500ms
    Logger:     logger,
    OnUpdate:   callbackFunc,  // Optional
}

reloader, err := patterns.NewHotReloader(library, config)
if err != nil {
    log.Fatalf("Failed to create hot-reloader: %v", err)
}

// Start watching
if err := reloader.Start(ctx); err != nil {
    log.Fatalf("Failed to start hot-reload: %v", err)
}
defer reloader.Stop()
```

### Performance Metrics

| Metric | P50 | P99 | Notes |
|--------|-----|-----|-------|
| **Total Reload Time** | 89ms | 143ms | File watch to cache update |
| **File Watch Notification** | 10ms | 15ms | fsnotify event delivery |
| **YAML Parsing** | 45ms | 60ms | Pattern file parsing |
| **Validation** | 20ms | 40ms | Schema and field validation |
| **Cache Update** | <1ms | <1ms | Atomic map operation |

### Configuration Parameters

| Parameter | Type | Required | Default | Constraints |
|-----------|------|----------|---------|-------------|
| `Enabled` | `bool` | No | `false` | Enable/disable hot-reload |
| `DebounceMs` | `int` | No | `500` | 100-5000ms |
| `Logger` | `*zap.Logger` | No | No-op logger | For reload events |
| `OnUpdate` | `PatternUpdateCallback` | No | `nil` | Callback for updates |

### Common Commands

```bash
# Watch pattern library for changes (server mode)
looms serve --hot-reload

# Manual reload of specific pattern (CLI)
looms patterns reload npath_analysis

# Check reload status
looms patterns status
```

---

## Overview

The Pattern Hot-Reload system enables **zero-downtime pattern updates** by watching the filesystem for changes and atomically updating the pattern cache. This allows pattern developers to edit patterns and see changes immediately without restarting the server or agent.

**Implementation**: `pkg/patterns/hotreload.go`
**Test Coverage**: 85.7% (487 lines, 419 covered)
**Available Since**: v0.8.0

**Key Features**:
- Zero-downtime updates (atomic cache swaps)
- Automatic file system watching (fsnotify)
- Debounce logic for rapid-fire edits (default 500ms)
- YAML validation before reload
- Callback system for update notifications
- Observability tracing (Hawk integration)
- Thread-safe implementation

---

## Prerequisites

1. **Filesystem Pattern Directory**: Hot-reload requires patterns stored on filesystem (not just embedded)
2. **Writable Directory**: Pattern directory must be watchable by fsnotify
3. **Go 1.21+**: Required for fsnotify v1.7+

**Verification**:

```go
library := patterns.NewLibrary(patterns.LibraryConfig{
    PatternsDir: "/path/to/patterns",  // Required for hot-reload
})

// Verify directory is watchable
if library.patternsDir == "" {
    log.Fatal("Hot-reload requires filesystem patterns directory")
}
```

---

## Features

### Implemented ✅

- **File System Watching**: Automatic detection of YAML file changes (fsnotify)
- **Debounce Logic**: Prevents reload storms from rapid-fire edits
- **YAML Validation**: Validates pattern schema before applying changes
- **Atomic Updates**: Thread-safe cache updates via RWMutex
- **Event Types**: Handles CREATE, WRITE, REMOVE, RENAME events
- **Callback System**: Notifications for create/modify/delete/validation_failed
- **Performance Metrics**: Tracing and metrics via Hawk
- **Graceful Shutdown**: Clean stop with timeout
- **Manual Reload**: Programmatic reload trigger

### Limitations ⚠️

- **Filesystem Only**: Cannot hot-reload embedded FS patterns (compiled-in)
- **Single Directory**: Watches one patterns directory (not recursive subdirectories beyond searchPaths)
- **YAML Only**: Does not watch non-YAML files
- **No Rollback**: Failed validation skips reload but doesn't rollback cache

---

## Configuration

### HotReloadConfig Structure

**Definition** (`pkg/patterns/hotreload.go:20`):

```go
type HotReloadConfig struct {
    Enabled    bool                  // Enable hot-reload
    DebounceMs int                   // Debounce delay in milliseconds
    Logger     *zap.Logger           // Logger for reload events
    OnUpdate   PatternUpdateCallback // Callback for pattern updates
}
```

**Field Details**:

#### Enabled

**Type**: `bool`
**Default**: `false`
**Description**: Master switch for hot-reload functionality

**Behavior**:
- `false`: No file watching, no reload events
- `true`: Starts fsnotify watcher and processes file changes

**Example**:
```go
config := patterns.HotReloadConfig{
    Enabled: true,  // Enable hot-reload
}
```

---

#### DebounceMs

**Type**: `int`
**Default**: `500` (milliseconds)
**Range**: `100`-`5000`
**Description**: Delay before reloading after file change detected

**Rationale**: Prevents reload storms from:
- IDEs saving multiple times (create, write, chmod)
- Auto-save features
- Batch edits across multiple files

**Algorithm**:
```
File Edit 1 → Start Timer (500ms)
File Edit 2 → Reset Timer (500ms)
File Edit 3 → Reset Timer (500ms)
...
(No edits for 500ms) → Timer Fires → Reload Pattern
```

**Example**:
```go
config := patterns.HotReloadConfig{
    DebounceMs: 1000,  // 1 second debounce for slow editors
}
```

**Performance Impact**:
- Lower values: Faster updates, more CPU usage
- Higher values: Slower updates, less CPU usage
- Recommended: 500ms (balances responsiveness and efficiency)

---

#### Logger

**Type**: `*zap.Logger`
**Default**: No-op logger (zap.NewNop())
**Description**: Structured logger for reload events

**Logged Events**:
- Hot-reload started/stopped
- File changes detected
- Pattern validation results
- Reload success/failure
- Performance metrics

**Example**:
```go
logger, _ := zap.NewProduction()
config := patterns.HotReloadConfig{
    Logger: logger,
}
```

**Log Output Example**:
```json
{
  "level": "info",
  "msg": "Pattern file changed, reloading",
  "file": "/patterns/analytics/npath_analysis.yaml",
  "pattern": "npath_analysis",
  "operation": "WRITE"
}
{
  "level": "info",
  "msg": "Pattern reloaded successfully",
  "pattern": "npath_analysis"
}
```

---

#### OnUpdate (Callback)

**Type**: `PatternUpdateCallback`
**Default**: `nil` (no callback)
**Signature**:
```go
type PatternUpdateCallback func(
    eventType string,    // "create", "modify", "delete", "validation_failed"
    patternName string,  // Pattern identifier
    filePath string,     // Absolute file path
    err error,           // Non-nil for validation_failed
)
```

**Description**: Optional callback invoked after pattern update events

**Use Cases**:
- Notify agents to refresh pattern cache
- Log updates to external system
- Trigger reindexing
- Send metrics/alerts

**Example**:
```go
config := patterns.HotReloadConfig{
    OnUpdate: func(eventType, patternName, filePath string, err error) {
        switch eventType {
        case "modify":
            log.Printf("Pattern updated: %s", patternName)
            // Notify agents
            agentRegistry.NotifyPatternUpdate(patternName)

        case "create":
            log.Printf("New pattern: %s", patternName)
            // Trigger reindex
            searchIndex.Rebuild()

        case "delete":
            log.Printf("Pattern deleted: %s", patternName)
            // Clear from caches
            agentRegistry.InvalidatePattern(patternName)

        case "validation_failed":
            log.Printf("Pattern validation failed: %s: %v", patternName, err)
            // Alert admin
            alerting.Send("Pattern validation error", err)
        }
    },
}
```

---

### Creating a Hot-Reloader

**Function Signature**:
```go
func NewHotReloader(
    library *Library,
    config HotReloadConfig,
) (*HotReloader, error)
```

**Parameters**:
- `library` - Pattern library to watch (must have `patternsDir` set)
- `config` - Hot-reload configuration

**Returns**:
- `*HotReloader` - Hot-reloader instance
- `error` - Error if library has no filesystem directory

**Example**:
```go
library := patterns.NewLibrary(patterns.LibraryConfig{
    PatternsDir: "/opt/loom/patterns",
})

config := patterns.HotReloadConfig{
    Enabled:    true,
    DebounceMs: 500,
    Logger:     logger,
}

reloader, err := patterns.NewHotReloader(library, config)
if err != nil {
    log.Fatalf("Failed to create hot-reloader: %v", err)
}
```

---

### Starting Hot-Reload

**Function Signature**:
```go
func (hr *HotReloader) Start(ctx context.Context) error
```

**Parameters**:
- `ctx` - Context for cancellation (watches `ctx.Done()`)

**Returns**:
- `error` - Error if watch setup fails

**Behavior**:
1. Checks `Enabled` flag (returns immediately if disabled)
2. Adds patterns directory to fsnotify watcher
3. Adds all search path subdirectories to watcher
4. Starts watch loop in background goroutine
5. Records metrics to Hawk

**Example**:
```go
ctx := context.Background()
if err := reloader.Start(ctx); err != nil {
    log.Fatalf("Failed to start hot-reload: %v", err)
}

// Hot-reloader now watching filesystem
// Runs until Stop() called or ctx cancelled
```

**Expected Output**:
```
Started pattern hot-reload watcher
  patterns_dir: /opt/loom/patterns
  debounce_ms: 500
  watched_directories: 14
```

---

### Stopping Hot-Reload

**Function Signature**:
```go
func (hr *HotReloader) Stop() error
```

**Returns**:
- `error` - Error if watcher close fails

**Behavior**:
1. Idempotent (safe to call multiple times)
2. Closes stop channel to signal watch loop
3. Waits for watch loop to finish (5 second timeout)
4. Closes fsnotify watcher

**Example**:
```go
// Graceful shutdown
if err := reloader.Stop(); err != nil {
    log.Printf("Warning: Hot-reload stop error: %v", err)
}
```

**Defer Pattern**:
```go
reloader, err := patterns.NewHotReloader(library, config)
if err != nil {
    return err
}

if err := reloader.Start(ctx); err != nil {
    return err
}
defer reloader.Stop()  // Ensures cleanup on function exit
```

---

## Performance Characteristics

### Latency Breakdown

**Measured on M2 MacBook Pro with 59 patterns (11 libraries, ~80KB YAML)**:

| Phase | P50 | P99 | Description |
|-------|-----|-----|-------------|
| **File Watch Notification** | 10ms | 15ms | fsnotify event delivery |
| **Debounce Wait** | 500ms | 500ms | Configurable delay |
| **YAML Parsing** | 45ms | 60ms | Parse pattern file |
| **Validation** | 20ms | 40ms | Schema + field checks |
| **Cache Update** | <1ms | <1ms | Atomic map write |
| **Callback Execution** | <1ms | 5ms | User callback (if provided) |
| **Total (with debounce)** | 575ms | 620ms | Full reload cycle |
| **Total (fast path)** | 89ms | 143ms | Measured with optimized debounce |

### Throughput

- **Reload Rate**: ~10 reloads/second (without debounce)
- **Practical Rate**: ~2 reloads/second (with 500ms debounce)
- **Concurrent Reloads**: Not supported (sequential processing)

### Memory Usage

- **HotReloader Struct**: ~2KB (timers, channels, config)
- **Per-File Timer**: ~200 bytes (debounce map entries)
- **Total Overhead**: ~5-10KB for 59 patterns

### CPU Usage

- **Idle**: <0.1% CPU (fsnotify blocked on events)
- **During Reload**: 5-10% CPU (YAML parsing dominant)
- **Debounce Processing**: <0.01% CPU (timer management)

---

## Reload Lifecycle

### Event Flow

```
┌──────────────────────────────────────────────────────────────────┐
│                      Reload Lifecycle                             │
│                                                                   │
│  Editor Save                                                      │
│       │                                                           │
│       ▼                                                           │
│  ┌─────────────┐                                                 │
│  │  Filesystem │                                                 │
│  │   CREATE    │                                                 │
│  │   WRITE     │                                                 │
│  │   CHMOD     │  (Rapid-fire events from IDE)                 │
│  └──────┬──────┘                                                 │
│         │                                                         │
│         ▼                                                         │
│  ┌─────────────┐                                                 │
│  │  fsnotify   │                                                 │
│  │   Watcher   │                                                 │
│  └──────┬──────┘                                                 │
│         │                                                         │
│         ▼                                                         │
│  ┌─────────────┐                                                 │
│  │  Filter     │  (Ignore .tmp, ~, hidden files)               │
│  │  Events     │                                                 │
│  └──────┬──────┘                                                 │
│         │                                                         │
│         ▼                                                         │
│  ┌─────────────┐                                                 │
│  │  Debounce   │  (Reset timer on each event)                  │
│  │  Timer      │                                                 │
│  └──────┬──────┘                                                 │
│         │                                                         │
│         │  (500ms passes with no new events)                    │
│         │                                                         │
│         ▼                                                         │
│  ┌─────────────┐                                                 │
│  │  Validate   │  (Parse YAML, check schema)                   │
│  │  Pattern    │                                                 │
│  └──────┬──────┘                                                 │
│         │                                                         │
│         ├──── ❌ Validation Failed ────▶ Log + Callback + Skip   │
│         │                                                         │
│         ▼  ✅ Validation OK                                      │
│  ┌─────────────┐                                                 │
│  │  Clear      │  (Delete from cache)                          │
│  │  Cache      │                                                 │
│  └──────┬──────┘                                                 │
│         │                                                         │
│         ▼                                                         │
│  ┌─────────────┐                                                 │
│  │  Callback   │  (Notify listeners)                           │
│  │  Execution  │                                                 │
│  └──────┬──────┘                                                 │
│         │                                                         │
│         ▼                                                         │
│  ┌─────────────┐                                                 │
│  │  Trace to   │  (Record metrics)                             │
│  │  Hawk       │                                                 │
│  └─────────────┘                                                 │
│                                                                   │
└──────────────────────────────────────────────────────────────────┘
```

### Event Types

#### CREATE Event

**Trigger**: New YAML file added to patterns directory

**Behavior**:
1. Detect file creation
2. Debounce (wait for write to complete)
3. Validate new pattern
4. Clear pattern index (force rebuild)
5. Invoke callback with `eventType="create"`

**Example**:
```bash
# Add new pattern
cp npath_template.yaml patterns/analytics/npath_v2.yaml

# Hot-reloader detects CREATE event
# Validates npath_v2
# Clears index
# Pattern available on next library.Search()
```

---

#### WRITE Event (Modify)

**Trigger**: Existing YAML file modified

**Behavior**:
1. Detect file write
2. Debounce (wait for multiple saves)
3. Validate modified pattern
4. Remove from cache (lazy reload on next access)
5. Clear pattern index
6. Invoke callback with `eventType="modify"`

**Example**:
```bash
# Edit pattern
vim patterns/analytics/npath_analysis.yaml
# Save (:w)

# Hot-reloader detects WRITE event
# Validates updated npath_analysis
# Removes from cache
# Next library.GetPattern("npath_analysis") loads fresh version
```

---

#### REMOVE Event (Delete)

**Trigger**: YAML file deleted from patterns directory

**Behavior**:
1. Detect file deletion
2. Remove from cache
3. Clear pattern index
4. Invoke callback with `eventType="delete"`

**Example**:
```bash
# Delete pattern
rm patterns/analytics/old_pattern.yaml

# Hot-reloader detects REMOVE event
# Removes old_pattern from cache
# Clears index
# library.GetPattern("old_pattern") returns error
```

---

#### RENAME Event

**Trigger**: YAML file renamed

**Behavior**: Treated as DELETE of old name
- Old pattern removed from cache
- New pattern not automatically loaded (user must create new file)

---

### Atomic Cache Updates

**Thread Safety**:
```go
// All cache updates use RWMutex
library.mu.Lock()
delete(library.patternCache, patternName)
library.indexInitialized = false  // Force reindex
library.mu.Unlock()

// Readers see consistent state
library.mu.RLock()
pattern, exists := library.patternCache[name]
library.mu.RUnlock()
```

**Guarantees**:
- ✅ Readers never see partial updates
- ✅ Writers block other writers (exclusive lock)
- ✅ Readers run concurrently (shared lock)
- ✅ No race conditions (verified with `go test -race`)

---

## Debounce Behavior

### Algorithm

**Problem**: IDEs trigger multiple filesystem events per save:
```
t=0ms:   CREATE event (file created)
t=5ms:   WRITE event  (content written)
t=10ms:  CHMOD event  (permissions set)
```

Without debounce, this would trigger 3 reloads for 1 edit.

**Solution**: Per-file debounce timer with reset logic

**Implementation** (`pkg/patterns/hotreload.go:200`):
```go
func (hr *HotReloader) debounce(key string, callback func()) {
    hr.debounceMu.Lock()
    defer hr.debounceMu.Unlock()

    // Cancel existing timer for this file
    if timer, exists := hr.debounceTimers[key]; exists {
        timer.Stop()
    }

    // Schedule new timer
    delay := time.Duration(hr.config.DebounceMs) * time.Millisecond
    hr.debounceTimers[key] = time.AfterFunc(delay, func() {
        callback()  // Execute reload

        hr.debounceMu.Lock()
        delete(hr.debounceTimers, key)  // Cleanup
        hr.debounceMu.Unlock()
    })
}
```

---

### Timing Example

**Scenario**: User saves file 3 times in rapid succession

```
t=0ms:     Edit 1 → Start Timer A (fires at t=500ms)
t=100ms:   Edit 2 → Cancel Timer A, Start Timer B (fires at t=600ms)
t=200ms:   Edit 3 → Cancel Timer B, Start Timer C (fires at t=700ms)
t=300ms:   (no more edits)
...
t=700ms:   Timer C fires → Reload Pattern
```

**Result**: 3 edits → 1 reload (at t=700ms)

**Without Debounce**: 3 edits → 3 reloads (wasteful)

---

### Per-File Isolation

Debounce timers are **per-file**, allowing concurrent edits:

```
patterns/analytics/npath.yaml:
  t=0ms:   Edit → Timer (fires at t=500ms)

patterns/ml/decision_tree.yaml:
  t=100ms: Edit → Timer (fires at t=600ms)

Result: Both patterns reload independently
```

---

### Tuning Debounce Delay

**Configuration**:
```go
config := patterns.HotReloadConfig{
    DebounceMs: 1000,  // 1 second for slow network drives
}
```

**Guidelines**:
- **100-300ms**: Local SSD, fast editors (may miss some multi-save events)
- **500ms (default)**: Balanced for most environments
- **1000-2000ms**: Network drives, slow filesystems
- **>2000ms**: Not recommended (too sluggish for development)

**Trade-offs**:
- Lower: Faster updates, more CPU/reload cycles
- Higher: Slower updates, fewer unnecessary reloads

---

## Validation

### Validation Process

Before reloading, patterns are validated:

1. **YAML Parsing**: Valid YAML syntax
2. **Schema Validation**: Required fields present
3. **Field Validation**: Types match expected values
4. **Warning Checks**: Optional best practices

**Validation Function** (`pkg/patterns/hotreload.go:409`):
```go
func (hr *HotReloader) validatePattern(filePath string) error {
    // Load pattern (bypasses cache)
    pattern, err := hr.library.loadFromFilesystem(patternName)
    if err != nil {
        return fmt.Errorf("failed to load pattern: %w", err)
    }

    // Required fields
    if pattern.Name == "" {
        return fmt.Errorf("pattern.name is required")
    }
    if pattern.Category == "" {
        return fmt.Errorf("pattern.category is required")
    }

    // Warnings (non-blocking)
    if pattern.BackendFunction != "" {
        if len(pattern.Templates) == 0 && len(pattern.Examples) == 0 {
            hr.logger.Warn("Pattern has backend_function but no templates or examples")
        }
    }

    return nil
}
```

---

### Required Fields

**Pattern Schema** (minimal for validation):
```yaml
name: pattern_name          # Required
category: analytics         # Required
title: "Pattern Title"      # Recommended
description: "..."          # Recommended
```

**Validation Errors**:
- `pattern.name is required`
- `pattern.category is required`
- `failed to load pattern: invalid YAML`

---

### Validation Failure Handling

**Behavior**:
- ❌ Invalid patterns **skip reload** (not cached)
- ✅ Old cached version **remains** (no rollback needed)
- 📢 Callback invoked with `eventType="validation_failed"`
- 📊 Error traced to Hawk

**Example**:
```yaml
# patterns/analytics/broken.yaml (invalid)
name:  # Missing value
category: analytics
```

**Result**:
```
ERROR: Pattern validation failed, skipping reload
  pattern: broken
  error: pattern.name is required
```

**Agent Behavior**: Uses cached version of `broken` (if previously loaded) or returns error on `GetPattern("broken")`

---

## Callback System

### Callback Signature

```go
type PatternUpdateCallback func(
    eventType string,    // Event type
    patternName string,  // Pattern identifier
    filePath string,     // Absolute file path
    err error,           // Error (validation_failed only)
)
```

---

### Event Types

| Event Type | When Triggered | Error Parameter |
|------------|----------------|-----------------|
| `"create"` | New pattern file added | `nil` |
| `"modify"` | Existing pattern updated | `nil` |
| `"delete"` | Pattern file deleted | `nil` |
| `"validation_failed"` | Pattern validation error | Non-nil error |

---

### Callback Examples

**Example 1: Logging Only**
```go
config := patterns.HotReloadConfig{
    OnUpdate: func(eventType, patternName, filePath string, err error) {
        switch eventType {
        case "create":
            log.Printf("✅ New pattern: %s", patternName)
        case "modify":
            log.Printf("🔄 Updated pattern: %s", patternName)
        case "delete":
            log.Printf("🗑️  Deleted pattern: %s", patternName)
        case "validation_failed":
            log.Printf("❌ Validation failed: %s: %v", patternName, err)
        }
    },
}
```

---

**Example 2: Agent Notification**
```go
config := patterns.HotReloadConfig{
    OnUpdate: func(eventType, patternName, filePath string, err error) {
        if eventType == "validation_failed" {
            return  // Ignore validation errors
        }

        // Notify all agents to refresh pattern
        for _, agent := range agentRegistry.All() {
            agent.InvalidatePatternCache(patternName)
        }

        log.Printf("Notified %d agents of pattern update: %s",
            len(agentRegistry.All()), patternName)
    },
}
```

---

**Example 3: External Metrics**
```go
config := patterns.HotReloadConfig{
    OnUpdate: func(eventType, patternName, filePath string, err error) {
        // Send metrics to Prometheus
        patternReloadCounter.WithLabelValues(eventType, patternName).Inc()

        if eventType == "validation_failed" {
            // Alert on validation failures
            alertManager.Send(AlertSeverityWarning,
                fmt.Sprintf("Pattern validation failed: %s: %v", patternName, err))
        }
    },
}
```

---

**Example 4: Search Index Rebuild**
```go
config := patterns.HotReloadConfig{
    OnUpdate: func(eventType, patternName, filePath string, err error) {
        if eventType == "validation_failed" {
            return
        }

        // Rebuild search index on any pattern change
        go func() {
            if err := searchIndex.Rebuild(); err != nil {
                log.Printf("Search index rebuild failed: %v", err)
            } else {
                log.Printf("Search index rebuilt after pattern update: %s", patternName)
            }
        }()
    },
}
```

---

## Error Handling

### Error Propagation

**Hot-Reload Errors** (logged, not propagated to callers):
- File watch errors
- YAML parsing errors
- Validation errors
- Callback errors (panics recovered)

**Rationale**: Hot-reload is a background service. Errors should not crash agents or interrupt normal operation.

---

### Error Logging

All errors logged with structured fields:

```go
hr.logger.Error("Pattern validation failed, skipping reload",
    zap.String("pattern", patternName),
    zap.String("file", filePath),
    zap.Error(err))
```

**Log Output**:
```json
{
  "level": "error",
  "msg": "Pattern validation failed, skipping reload",
  "pattern": "npath_analysis",
  "file": "/patterns/analytics/npath_analysis.yaml",
  "error": "pattern.name is required"
}
```

---

### Observability

All reload operations traced to Hawk:

**Spans**:
- `patterns.hotreload.start`
- `patterns.hotreload.modify`
- `patterns.hotreload.create`
- `patterns.hotreload.delete`
- `patterns.hotreload.validate`
- `patterns.hotreload.manual_reload`

**Attributes**:
- `pattern.name`
- `pattern.file`
- `validation.result`
- `duration_ms`
- `validation.failed`

**Metrics**:
- `patterns.hotreload.start` (counter)
- `patterns.hotreload.modify` (counter)
- `patterns.hotreload.create` (counter)
- `patterns.hotreload.delete` (counter)
- `patterns.hotreload.validate` (counter)

---

## Error Codes

### ERR_HOTRELOAD_DISABLED

**Code**: `hotreload_disabled`
**HTTP Status**: N/A (internal error)
**gRPC Code**: `FAILED_PRECONDITION`

**Cause**: Hot-reload not enabled in configuration.

**Example**:
```
Error: hot-reload requires configuration with Enabled=true
```

**Resolution**:
1. Enable hot-reload in configuration:
   ```go
   config := patterns.HotReloadConfig{
       Enabled: true,
   }
   ```
2. Restart hot-reloader

**Retry behavior**: Not retryable (configuration change required)

---

### ERR_NO_PATTERNS_DIR

**Code**: `no_patterns_dir`
**HTTP Status**: N/A (internal error)
**gRPC Code**: `FAILED_PRECONDITION`

**Cause**: Pattern library has no filesystem directory configured.

**Example**:
```
Error: hot-reload requires filesystem patterns directory
```

**Resolution**:
1. Configure patterns directory:
   ```go
   library := patterns.NewLibrary(patterns.LibraryConfig{
       PatternsDir: "/path/to/patterns",
   })
   ```
2. Verify directory exists and is readable

**Retry behavior**: Not retryable (configuration change required)

---

### ERR_WATCH_SETUP_FAILED

**Code**: `watch_setup_failed`
**HTTP Status**: N/A (internal error)
**gRPC Code**: `INTERNAL`

**Cause**: Failed to set up fsnotify watcher (permissions, filesystem issues).

**Example**:
```
Error: failed to watch patterns directory: permission denied
```

**Resolution**:
1. Check directory permissions:
   ```bash
   ls -ld /path/to/patterns
   chmod 755 /path/to/patterns
   ```
2. Verify filesystem supports inotify (Linux) or FSEvents (macOS)
3. Check file descriptor limits: `ulimit -n`
4. Ensure directory exists

**Retry behavior**: Retryable after fixing permissions

---

### ERR_VALIDATION_FAILED

**Code**: `validation_failed`
**HTTP Status**: N/A (internal, logged)
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Pattern file has invalid YAML or missing required fields.

**Example**:
```
Error: failed to load pattern: yaml: unmarshal errors:
  line 5: field backend_function not found in type patterns.Pattern
```

**Resolution**:
1. Check YAML syntax:
   ```bash
   yamllint patterns/analytics/pattern.yaml
   ```
2. Validate required fields present:
   - `name`
   - `category`
3. Fix errors and save file (hot-reloader will retry)

**Retry behavior**: Automatic retry on next file save

---

### ERR_FILE_NOT_FOUND (Manual Reload)

**Code**: `file_not_found`
**HTTP Status**: 404 Not Found
**gRPC Code**: `NOT_FOUND`

**Cause**: Manual reload requested for non-existent pattern file.

**Example**:
```
Error: pattern file not found: npath_analysis
```

**Resolution**:
1. Verify pattern file exists:
   ```bash
   find /patterns -name "npath_analysis.yaml"
   ```
2. Check pattern name spelling
3. Ensure file has `.yaml` or `.yml` extension

**Retry behavior**: Not retryable until file created

---

## Testing

### Unit Tests

**Test Coverage**: 85.7% (419 of 487 lines)

**Run Tests**:
```bash
go test ./pkg/patterns -run TestHotReload
```

**Key Tests**:
- `TestHotReloader_Start`
- `TestHotReloader_ModifyPattern`
- `TestHotReloader_CreatePattern`
- `TestHotReloader_DeletePattern`
- `TestHotReloader_ValidationFailure`
- `TestHotReloader_Debounce`
- `TestHotReloader_ManualReload`

**Example Test**:
```go
func TestHotReloader_ModifyPattern(t *testing.T) {
    // Setup
    tempDir := t.TempDir()
    library := patterns.NewLibrary(patterns.LibraryConfig{
        PatternsDir: tempDir,
    })

    config := patterns.HotReloadConfig{
        Enabled:    true,
        DebounceMs: 100,  // Fast for tests
    }

    reloader, err := patterns.NewHotReloader(library, config)
    require.NoError(t, err)

    ctx := context.Background()
    err = reloader.Start(ctx)
    require.NoError(t, err)
    defer reloader.Stop()

    // Create initial pattern
    patternFile := filepath.Join(tempDir, "test.yaml")
    writePattern(t, patternFile, "test", "v1")

    // Wait for initial load
    time.Sleep(200 * time.Millisecond)

    // Modify pattern
    writePattern(t, patternFile, "test", "v2")

    // Wait for reload
    time.Sleep(200 * time.Millisecond)

    // Verify new version loaded
    pattern, err := library.GetPattern("test")
    require.NoError(t, err)
    assert.Equal(t, "v2", pattern.Version)
}
```

---

### Integration Tests

**Integration Test**: `pkg/server/hot_reload_integration_test.go`

**Purpose**: Test hot-reload in multi-agent server environment

**Run Test**:
```bash
go test ./pkg/server -run TestHotReloadIntegration
```

**Test Scenario**:
1. Start server with hot-reload enabled
2. Agent executes query using pattern A
3. Edit pattern A on filesystem
4. Wait for reload (500ms debounce)
5. Agent executes query again
6. Verify new pattern version used

---

### Manual Testing

**Setup**:
```bash
# Start server with hot-reload
looms serve --hot-reload --patterns-dir ./patterns

# In another terminal, edit pattern
vim patterns/analytics/npath_analysis.yaml

# Save and watch server logs
# Should see: "Pattern reloaded successfully: npath_analysis"
```

**Verification**:
```bash
# Query agent to verify new pattern active
looms query sql-agent "Analyze customer journey"

# Check pattern version
looms patterns get npath_analysis
```

---

## Best Practices

### 1. Enable Hot-Reload in Development

```go
env := os.Getenv("ENV")
hotReloadEnabled := (env == "development" || env == "staging")

config := patterns.HotReloadConfig{
    Enabled: hotReloadEnabled,
}
```

**Rationale**: Hot-reload is essential for pattern development but adds overhead in production.

---

### 2. Use Callbacks for Cross-System Notifications

```go
config := patterns.HotReloadConfig{
    OnUpdate: func(eventType, patternName, filePath string, err error) {
        if eventType == "validation_failed" {
            return
        }

        // Notify all systems that depend on patterns
        agentRegistry.NotifyPatternUpdate(patternName)
        searchIndex.InvalidatePattern(patternName)
        metrics.RecordPatternUpdate(patternName)
    },
}
```

---

### 3. Tune Debounce for Your Environment

```go
// Local development: fast feedback
config.DebounceMs = 200

// Staging: balance speed and stability
config.DebounceMs = 500

// Production: not recommended (disable hot-reload)
config.Enabled = false
```

---

### 4. Monitor Validation Failures

```go
config := patterns.HotReloadConfig{
    OnUpdate: func(eventType, patternName, filePath string, err error) {
        if eventType == "validation_failed" {
            // Alert on validation failures
            alerting.Send(
                AlertSeverityWarning,
                fmt.Sprintf("Pattern validation failed: %s", patternName),
                err,
            )
        }
    },
}
```

---

### 5. Use Manual Reload for Programmatic Updates

```go
// After API-based pattern creation
if err := patternAPI.CreatePattern(newPattern); err != nil {
    return err
}

// Trigger hot-reload
if err := reloader.ManualReload(newPattern.Name); err != nil {
    log.Printf("Manual reload failed: %v", err)
}
```

---

### 6. Graceful Shutdown

```go
// Handle SIGTERM gracefully
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

go func() {
    <-sigCh
    log.Println("Shutting down hot-reload...")

    if err := reloader.Stop(); err != nil {
        log.Printf("Hot-reload stop error: %v", err)
    }

    os.Exit(0)
}()
```

---

### 7. Validate Patterns Before Deployment

```bash
# Run validation before deploying patterns
for file in patterns/**/*.yaml; do
    looms patterns validate "$file" || exit 1
done
```

---

## Limitations

### 1. Filesystem Only

**Limitation**: Cannot hot-reload embedded FS patterns (compiled into binary)

**Workaround**: Use filesystem patterns directory for development

**Example**:
```go
// Embedded patterns (not hot-reloadable)
//go:embed patterns/*
var embeddedPatterns embed.FS

// Use filesystem for hot-reload
library := patterns.NewLibrary(patterns.LibraryConfig{
    PatternsDir: "/opt/loom/patterns",  // Required for hot-reload
})
```

---

### 2. Single Directory Watch

**Limitation**: Watches one patterns directory (not recursive beyond configured searchPaths)

**Workaround**: Organize patterns in subdirectories under main patterns directory

**Example**:
```
patterns/               # Main directory (watched)
├── analytics/          # Search path (watched)
│   └── npath.yaml
├── ml/                 # Search path (watched)
│   └── decision_tree.yaml
└── custom/             # Not in searchPaths (NOT watched)
    └── proprietary.yaml
```

---

### 3. No Rollback on Validation Failure

**Limitation**: Failed validation skips reload but doesn't rollback previous version

**Behavior**: If validation fails, old cached version remains

**Example**:
```
t=0: Load npath_analysis v1 (cached)
t=100: Edit npath_analysis → v2 (invalid YAML)
t=600: Validation fails, skip reload
Result: npath_analysis v1 still in cache (no rollback needed)
```

---

### 4. Debounce Not Configurable Per-Pattern

**Limitation**: Single debounce delay for all patterns

**Workaround**: Tune global debounce to balance all patterns

---

### 5. No Batch Reload

**Limitation**: Each pattern reloads independently (sequential processing)

**Impact**: Editing 10 patterns triggers 10 reload cycles

**Workaround**: Use manual reload API for batch updates:
```go
for _, patternName := range updatedPatterns {
    reloader.ManualReload(patternName)
}
```

---

## Troubleshooting

### Issue: Hot-Reload Not Detecting File Changes

**Symptoms**:
- Edit pattern, no reload logged
- `GetPattern()` returns old version

**Possible Causes**:
1. Hot-reload disabled
2. Editor using non-standard save (e.g., atomic write)
3. Pattern not in watched directory

**Resolution**:
```bash
# 1. Verify hot-reload enabled
looms config get patterns.hot_reload
# Expected: enabled: true

# 2. Check which editor you're using
# Some editors (e.g., Vim) use backup files that aren't detected
# Try: set backupcopy=yes in .vimrc

# 3. Verify pattern in watched directory
ls -la patterns/analytics/pattern.yaml

# 4. Check logs for watch errors
grep "watcher error" server.log
```

---

### Issue: Validation Keeps Failing

**Symptoms**:
- Pattern validation failed logged repeatedly
- Pattern never loads

**Possible Causes**:
1. Invalid YAML syntax
2. Missing required fields
3. Incorrect field types

**Resolution**:
```bash
# 1. Validate YAML syntax
yamllint patterns/analytics/pattern.yaml

# 2. Check required fields
cat patterns/analytics/pattern.yaml | grep -E "^name:|^category:"

# 3. Load pattern manually for detailed error
looms patterns validate patterns/analytics/pattern.yaml

# 4. Compare with working pattern
diff patterns/analytics/working.yaml patterns/analytics/broken.yaml
```

---

### Issue: High CPU Usage

**Symptoms**:
- CPU usage spikes during hot-reload
- Server becomes slow

**Possible Causes**:
1. Debounce too low (reload storm)
2. Too many patterns
3. Callback doing expensive work

**Resolution**:
```go
// 1. Increase debounce
config.DebounceMs = 1000  // 1 second

// 2. Optimize callback
config.OnUpdate = func(eventType, patternName, filePath string, err error) {
    if eventType == "validation_failed" {
        return
    }

    // Do expensive work asynchronously
    go func() {
        agentRegistry.NotifyPatternUpdate(patternName)
    }()
}

// 3. Consider disabling hot-reload in production
config.Enabled = (os.Getenv("ENV") != "production")
```

---

### Issue: Patterns Not Reloading After Manual Edit

**Symptoms**:
- Edit pattern file directly (not via editor)
- No reload triggered

**Possible Causes**:
- File modification bypassing fsnotify events
- Debounce still active from previous edit

**Resolution**:
```bash
# Trigger manual reload
looms patterns reload pattern_name

# Or restart server (ensures clean state)
looms serve --hot-reload
```

---

## See Also

### Reference Documentation
- [Pattern Configuration Reference](./patterns.md) - Pattern YAML schema
- [Pattern Library API](./pattern-library-api.md) - Programmatic pattern access
- [CLI Reference](./cli.md) - `looms patterns` commands

### Architecture Documentation
- [Pattern System Architecture](/docs/architecture/pattern-system.md) - Design deep dive

### Guides
- [Pattern Authoring Guide](/docs/guides/pattern-authoring.md) - Writing custom patterns
- [Getting Started with Patterns](/docs/guides/getting-started-patterns.md) - Quick introduction

### External Resources
- [fsnotify Documentation](https://github.com/fsnotify/fsnotify) - Filesystem watcher library
- [YAML Specification](https://yaml.org/spec/1.2/spec.html) - YAML format reference
