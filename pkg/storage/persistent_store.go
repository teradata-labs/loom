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
	"os"
	"path/filepath"
	"sync"

	"go.uber.org/zap"
)

var (
	globalSharedMemory *SharedMemoryStore
	globalMemoryOnce   sync.Once
)

// GetGlobalSharedMemory returns a singleton shared memory store with disk persistence.
// This ensures that tool result references work across agent instances and survive restarts.
//
// The global store uses a two-tier architecture:
// 1. Hot data (frequently accessed) stays in memory (fast, up to MaxMemoryBytes)
// 2. Cold data (LRU evicted) overflows to disk (persistent, up to MaxDiskSize)
//
// This design solves the reference-not-found problem where:
// - Multiple agent instances share the same reference store
// - References survive agent restarts (disk-backed)
// - References survive memory pressure (disk overflow)
//
// Observability: The store automatically tracks hits, misses, evictions, and compressions.
// Use the Stats() method to retrieve metrics for monitoring.
func GetGlobalSharedMemory(config *Config) *SharedMemoryStore {
	globalMemoryOnce.Do(func() {
		logger, _ := zap.NewProduction()
		if logger == nil {
			logger = zap.NewNop()
		}
		defer func() { _ = logger.Sync() }()

		logger.Info("Initializing global SharedMemoryStore for tool results",
			zap.Int64("max_memory_bytes", config.MaxMemoryBytes),
			zap.Int64("compression_threshold", config.CompressionThreshold),
			zap.Int64("ttl_seconds", config.TTLSeconds))

		// Determine cache directory
		cacheDir := os.Getenv("LOOM_TOOL_CACHE_DIR")
		if cacheDir == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				logger.Warn("Failed to get home directory, using temp dir",
					zap.Error(err))
				homeDir = os.TempDir()
			}
			cacheDir = filepath.Join(homeDir, ".loom", "tool_results")
		}

		// Create disk overflow handler for persistence
		logger.Info("Configuring disk overflow for persistent storage",
			zap.String("cache_dir", cacheDir),
			zap.String("max_disk_size", "1GB"))

		diskOverflow, err := NewDiskOverflowManager(&DiskOverflowConfig{
			CachePath:   cacheDir,
			MaxDiskSize: 1024 * 1024 * 1024, // 1GB disk cache
			TTLSeconds:  config.TTLSeconds,
		})
		if err != nil {
			// Fallback to memory-only if disk fails
			// This is safe - the system degrades gracefully
			logger.Warn("Failed to create disk overflow manager, falling back to memory-only mode",
				zap.Error(err),
				zap.String("cache_dir", cacheDir))
			diskOverflow = nil
		} else {
			logger.Info("Disk overflow configured successfully",
				zap.String("cache_dir", cacheDir))
		}

		// Create global store with disk backing
		globalSharedMemory = NewSharedMemoryStore(&Config{
			MaxMemoryBytes:       config.MaxMemoryBytes,
			CompressionThreshold: config.CompressionThreshold,
			TTLSeconds:           config.TTLSeconds,
			OverflowHandler:      diskOverflow,
		})

		logger.Info("Global SharedMemoryStore initialized successfully",
			zap.Bool("disk_backed", diskOverflow != nil),
			zap.String("mode", func() string {
				if diskOverflow != nil {
					return "memory+disk"
				}
				return "memory-only"
			}()))
	})
	return globalSharedMemory
}

// GetGlobalSharedMemoryStats returns statistics from the global shared memory store.
// Returns nil if the global store hasn't been initialized yet.
// Useful for monitoring and debugging tool result storage.
func GetGlobalSharedMemoryStats() *Stats {
	if globalSharedMemory == nil {
		return nil
	}
	stats := globalSharedMemory.Stats()
	return &stats
}

// ResetGlobalSharedMemory resets the global singleton (for testing only).
// This should NOT be used in production code.
func ResetGlobalSharedMemory() {
	globalSharedMemory = nil
	globalMemoryOnce = sync.Once{}
}
