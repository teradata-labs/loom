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
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

const (
	// DefaultMaxDiskBytes is 10GB
	DefaultMaxDiskBytes = 10 * 1024 * 1024 * 1024
)

// getDefaultDiskCachePath returns the OS-appropriate default disk cache path.
func getDefaultDiskCachePath() string {
	return filepath.Join(os.TempDir(), "loom", "cache")
}

// DiskOverflowManager manages overflow data on disk.
type DiskOverflowManager struct {
	mu          sync.RWMutex
	cachePath   string
	maxDiskSize int64
	currentSize int64
	metadata    map[string]*DiskMetadata
	ttl         time.Duration
}

// DiskMetadata tracks metadata for disk-stored data.
type DiskMetadata struct {
	ID          string
	FilePath    string
	Size        int64
	StoredAt    time.Time
	AccessedAt  time.Time
	Checksum    string
	ContentType string
	Compressed  bool
}

// DiskOverflowConfig configures the disk overflow manager.
type DiskOverflowConfig struct {
	CachePath   string
	MaxDiskSize int64
	TTLSeconds  int64
}

// NewDiskOverflowManager creates a new disk overflow manager.
func NewDiskOverflowManager(config *DiskOverflowConfig) (*DiskOverflowManager, error) {
	if config == nil {
		config = &DiskOverflowConfig{}
	}

	cachePath := config.CachePath
	if cachePath == "" {
		cachePath = getDefaultDiskCachePath()
	}

	maxDiskSize := config.MaxDiskSize
	if maxDiskSize <= 0 {
		maxDiskSize = DefaultMaxDiskBytes
	}

	ttl := time.Duration(config.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = DefaultTTLSeconds * time.Second
	}

	// Create cache directory
	if err := os.MkdirAll(cachePath, 0750); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	manager := &DiskOverflowManager{
		cachePath:   cachePath,
		maxDiskSize: maxDiskSize,
		metadata:    make(map[string]*DiskMetadata),
		ttl:         ttl,
	}

	// Start cleanup goroutine
	go manager.cleanupLoop()

	return manager, nil
}

// Store stores data to disk.
func (d *DiskOverflowManager) Store(id string, data []byte, ref *loomv1.DataReference) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check if we have space
	dataSize := int64(len(data))
	if d.currentSize+dataSize > d.maxDiskSize {
		// Try cleanup first
		d.cleanupExpired()
		if d.currentSize+dataSize > d.maxDiskSize {
			return fmt.Errorf("disk cache full: current=%d, need=%d, max=%d",
				d.currentSize, dataSize, d.maxDiskSize)
		}
	}

	// Generate file path
	filePath := filepath.Join(d.cachePath, id+".dat")

	// Write to disk
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write to disk: %w", err)
	}

	// Update metadata
	now := time.Now()
	d.metadata[id] = &DiskMetadata{
		ID:          id,
		FilePath:    filePath,
		Size:        dataSize,
		StoredAt:    now,
		AccessedAt:  now,
		Checksum:    ref.Checksum,
		ContentType: ref.ContentType,
		Compressed:  ref.Compressed,
	}
	d.currentSize += dataSize

	return nil
}

// Retrieve retrieves data from disk.
func (d *DiskOverflowManager) Retrieve(id string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	meta, exists := d.metadata[id]
	if !exists {
		return nil, fmt.Errorf("data not found on disk: %s", id)
	}

	// Read from disk
	data, err := os.ReadFile(meta.FilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read from disk: %w", err)
	}

	// Update access time
	meta.AccessedAt = time.Now()

	return data, nil
}

// Delete removes data from disk.
func (d *DiskOverflowManager) Delete(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	meta, exists := d.metadata[id]
	if !exists {
		return fmt.Errorf("data not found on disk: %s", id)
	}

	// Remove file
	if err := os.Remove(meta.FilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove file: %w", err)
	}

	// Update metadata
	delete(d.metadata, id)
	d.currentSize -= meta.Size

	return nil
}

// cleanupExpired removes expired data. Must be called with lock held.
func (d *DiskOverflowManager) cleanupExpired() {
	now := time.Now()
	toDelete := []string{}

	for id, meta := range d.metadata {
		if now.Sub(meta.AccessedAt) > d.ttl {
			toDelete = append(toDelete, id)
		}
	}

	for _, id := range toDelete {
		meta := d.metadata[id]
		os.Remove(meta.FilePath) // Ignore errors
		delete(d.metadata, id)
		d.currentSize -= meta.Size
	}
}

// cleanupLoop periodically removes expired data.
func (d *DiskOverflowManager) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		d.mu.Lock()
		d.cleanupExpired()
		d.mu.Unlock()
	}
}

// Stats returns statistics about the disk overflow manager.
func (d *DiskOverflowManager) Stats() DiskStats {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return DiskStats{
		CurrentSize: d.currentSize,
		MaxSize:     d.maxDiskSize,
		ItemCount:   len(d.metadata),
	}
}

// DiskStats holds statistics about the disk overflow manager.
type DiskStats struct {
	CurrentSize int64
	MaxSize     int64
	ItemCount   int
}

// Promote retrieves data from disk and removes it (for promoting back to memory).
func (d *DiskOverflowManager) Promote(id string) ([]byte, error) {
	data, err := d.Retrieve(id)
	if err != nil {
		return nil, err
	}

	// Delete from disk after successful retrieval
	if err := d.Delete(id); err != nil {
		// Log but don't fail - we have the data
		return data, nil
	}

	return data, nil
}
