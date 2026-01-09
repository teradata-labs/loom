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
package communication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Hawk span constants for shared memory operations
const (
	SpanSharedMemoryPut    = "shared_memory.put"
	SpanSharedMemoryGet    = "shared_memory.get"
	SpanSharedMemoryDelete = "shared_memory.delete"
	SpanSharedMemoryList   = "shared_memory.list"
	SpanSharedMemoryWatch  = "shared_memory.watch"
)

// CompressionThreshold is the minimum size in bytes to trigger automatic compression
const CompressionThreshold = 1024 // 1KB

// SharedMemoryStore provides zero-copy shared memory for agent communication.
// All operations are safe for concurrent use by multiple goroutines.
type SharedMemoryStore struct {
	mu sync.RWMutex

	// Per-namespace storage: namespace → key → value
	data map[loomv1.SharedMemoryNamespace]map[string]*loomv1.SharedMemoryValue

	// Per-namespace statistics
	stats map[loomv1.SharedMemoryNamespace]*SharedMemoryNamespaceStats

	// Watchers: namespace → watcher list
	watchers map[loomv1.SharedMemoryNamespace][]*SharedMemoryWatcher

	// Dependencies
	tracer observability.Tracer
	logger *zap.Logger

	// Compression encoder/decoder (reusable, thread-safe)
	encoder *zstd.Encoder
	decoder *zstd.Decoder

	// Lifecycle
	closed atomic.Bool
}

// SharedMemoryNamespaceStats tracks statistics for a single namespace.
type SharedMemoryNamespaceStats struct {
	namespace     loomv1.SharedMemoryNamespace
	keyCount      atomic.Int64
	totalBytes    atomic.Int64
	readCount     atomic.Int64
	writeCount    atomic.Int64
	conflictCount atomic.Int64
	lastAccessAt  atomic.Value // time.Time
}

// SharedMemoryWatcher represents an active watcher for a namespace.
type SharedMemoryWatcher struct {
	id         string
	agentID    string
	namespace  loomv1.SharedMemoryNamespace
	keyPattern string
	channel    chan *loomv1.SharedMemoryValue
	created    time.Time
}

// NewSharedMemoryStore creates a new shared memory store.
func NewSharedMemoryStore(tracer observability.Tracer, logger *zap.Logger) (*SharedMemoryStore, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	// Create zstd encoder/decoder (reusable, thread-safe)
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd encoder: %w", err)
	}

	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd decoder: %w", err)
	}

	store := &SharedMemoryStore{
		data:     make(map[loomv1.SharedMemoryNamespace]map[string]*loomv1.SharedMemoryValue),
		stats:    make(map[loomv1.SharedMemoryNamespace]*SharedMemoryNamespaceStats),
		watchers: make(map[loomv1.SharedMemoryNamespace][]*SharedMemoryWatcher),
		tracer:   tracer,
		logger:   logger,
		encoder:  encoder,
		decoder:  decoder,
	}

	// Initialize stats for all namespaces
	for _, ns := range []loomv1.SharedMemoryNamespace{
		loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
		loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_SWARM,
		loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_DEBATE,
		loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_SESSION,
		loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
	} {
		store.stats[ns] = &SharedMemoryNamespaceStats{namespace: ns}
		store.data[ns] = make(map[string]*loomv1.SharedMemoryValue)
	}

	return store, nil
}

// scopeKey automatically prefixes the key with agent ID for AGENT namespace.
// For other namespaces, returns the key unchanged.
// This ensures strict isolation - agents cannot access each other's private data.
func scopeKey(namespace loomv1.SharedMemoryNamespace, agentID string, key string) string {
	if namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT {
		return fmt.Sprintf("agent:%s:%s", agentID, key)
	}
	return key
}

// Put writes or updates a value in shared memory with optimistic concurrency control.
func (s *SharedMemoryStore) Put(ctx context.Context, req *loomv1.PutSharedMemoryRequest) (*loomv1.PutSharedMemoryResponse, error) {
	if s.closed.Load() {
		return nil, fmt.Errorf("shared memory store is closed")
	}

	if req.Namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_UNSPECIFIED {
		return nil, fmt.Errorf("namespace cannot be unspecified")
	}
	if req.Key == "" {
		return nil, fmt.Errorf("key cannot be empty")
	}
	if req.AgentId == "" {
		return nil, fmt.Errorf("agent ID cannot be empty")
	}

	// Auto-scope key for AGENT namespace
	scopedKey := scopeKey(req.Namespace, req.AgentId, req.Key)

	// Instrument with Hawk
	var span *observability.Span
	if s.tracer != nil {
		_, span = s.tracer.StartSpan(ctx, SpanSharedMemoryPut)
		defer s.tracer.EndSpan(span)
		span.SetAttribute("namespace", req.Namespace.String())
		span.SetAttribute("key", req.Key)
		span.SetAttribute("scoped_key", scopedKey)
		span.SetAttribute("agent_id", req.AgentId)
		span.SetAttribute("value_size", len(req.Value))
	}

	start := time.Now()

	// Compression
	value := req.Value
	compressed := false
	if req.Compress || len(req.Value) >= CompressionThreshold {
		compressedValue := s.encoder.EncodeAll(req.Value, nil)
		if len(compressedValue) < len(req.Value) {
			value = compressedValue
			compressed = true
		}
	}

	// Checksum
	hash := sha256.Sum256(value)
	checksum := hex.EncodeToString(hash[:])

	s.mu.Lock()
	defer s.mu.Unlock()

	// Get namespace data
	nsData := s.data[req.Namespace]
	nsStats := s.stats[req.Namespace]

	// Check if key exists (using scoped key)
	existing, exists := nsData[scopedKey]
	created := !exists

	// Optimistic concurrency check
	if req.ExpectedVersion > 0 {
		if !exists {
			nsStats.conflictCount.Add(1)
			return nil, fmt.Errorf("version conflict: key does not exist (expected version %d)", req.ExpectedVersion)
		}
		if existing.Version != req.ExpectedVersion {
			nsStats.conflictCount.Add(1)
			return nil, fmt.Errorf("version conflict: expected %d, found %d", req.ExpectedVersion, existing.Version)
		}
	}

	// Compute new version
	newVersion := int64(1)
	if exists {
		newVersion = existing.Version + 1
	}

	// Create new value
	now := time.Now().UnixMilli()
	newValue := &loomv1.SharedMemoryValue{
		Key:        req.Key,
		Value:      value,
		Version:    newVersion,
		Compressed: compressed,
		Checksum:   checksum,
		Metadata:   req.Metadata,
		Namespace:  req.Namespace,
		UpdatedBy:  req.AgentId,
		UpdatedAt:  now,
	}

	if created {
		newValue.CreatedBy = req.AgentId
		newValue.CreatedAt = now
	} else {
		newValue.CreatedBy = existing.CreatedBy
		newValue.CreatedAt = existing.CreatedAt
	}

	// Update storage (using scoped key for isolation)
	nsData[scopedKey] = newValue

	// Update statistics
	nsStats.writeCount.Add(1)
	nsStats.lastAccessAt.Store(time.Now())
	if created {
		nsStats.keyCount.Add(1)
		nsStats.totalBytes.Add(int64(len(value)))
	} else {
		nsStats.totalBytes.Add(int64(len(value) - len(existing.Value)))
	}

	// Notify watchers (pass scopedKey for AGENT namespace filtering)
	s.notifyWatchers(req.Namespace, scopedKey, newValue)

	latency := time.Since(start)
	if span != nil {
		span.SetAttribute("latency_us", latency.Microseconds())
		span.SetAttribute("compressed", compressed)
		span.SetAttribute("created", created)
		span.SetAttribute("version", newVersion)
	}

	s.logger.Debug("shared memory put",
		zap.String("namespace", req.Namespace.String()),
		zap.String("key", req.Key),
		zap.String("agent_id", req.AgentId),
		zap.Int("value_size", len(req.Value)),
		zap.Int("stored_size", len(value)),
		zap.Bool("compressed", compressed),
		zap.Bool("created", created),
		zap.Int64("version", newVersion),
		zap.Duration("latency", latency))

	return &loomv1.PutSharedMemoryResponse{
		Version:   newVersion,
		Checksum:  checksum,
		Created:   created,
		SizeBytes: int64(len(value)),
	}, nil
}

// Get retrieves a value from shared memory.
func (s *SharedMemoryStore) Get(ctx context.Context, req *loomv1.GetSharedMemoryRequest) (*loomv1.GetSharedMemoryResponse, error) {
	if s.closed.Load() {
		return nil, fmt.Errorf("shared memory store is closed")
	}

	if req.Namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_UNSPECIFIED {
		return nil, fmt.Errorf("namespace cannot be unspecified")
	}
	if req.Key == "" {
		return nil, fmt.Errorf("key cannot be empty")
	}

	// Auto-scope key for AGENT namespace
	scopedKey := scopeKey(req.Namespace, req.AgentId, req.Key)

	// Instrument with Hawk
	var span *observability.Span
	if s.tracer != nil {
		_, span = s.tracer.StartSpan(ctx, SpanSharedMemoryGet)
		defer s.tracer.EndSpan(span)
		span.SetAttribute("namespace", req.Namespace.String())
		span.SetAttribute("key", req.Key)
		span.SetAttribute("scoped_key", scopedKey)
		span.SetAttribute("agent_id", req.AgentId)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	nsData := s.data[req.Namespace]
	nsStats := s.stats[req.Namespace]

	value, found := nsData[scopedKey]

	// Update statistics
	nsStats.readCount.Add(1)
	nsStats.lastAccessAt.Store(time.Now())

	if !found {
		if span != nil {
			span.SetAttribute("found", false)
		}
		return &loomv1.GetSharedMemoryResponse{Found: false}, nil
	}

	// Decompress if needed
	resultValue := &loomv1.SharedMemoryValue{
		Key:        value.Key,
		Value:      value.Value,
		Version:    value.Version,
		Compressed: false, // Always return decompressed
		Checksum:   value.Checksum,
		Metadata:   value.Metadata,
		CreatedBy:  value.CreatedBy,
		CreatedAt:  value.CreatedAt,
		UpdatedBy:  value.UpdatedBy,
		UpdatedAt:  value.UpdatedAt,
		Namespace:  value.Namespace,
	}

	if value.Compressed {
		decompressed, err := s.decoder.DecodeAll(value.Value, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to decompress value: %w", err)
		}
		resultValue.Value = decompressed
	}

	if span != nil {
		span.SetAttribute("found", true)
		span.SetAttribute("version", value.Version)
		span.SetAttribute("size_bytes", len(resultValue.Value))
	}

	s.logger.Debug("shared memory get",
		zap.String("namespace", req.Namespace.String()),
		zap.String("key", req.Key),
		zap.String("agent_id", req.AgentId),
		zap.Bool("found", found),
		zap.Int64("version", value.Version))

	return &loomv1.GetSharedMemoryResponse{
		Value: resultValue,
		Found: true,
	}, nil
}

// Delete removes a value from shared memory with optimistic concurrency control.
func (s *SharedMemoryStore) Delete(ctx context.Context, req *loomv1.DeleteSharedMemoryRequest) (*loomv1.DeleteSharedMemoryResponse, error) {
	if s.closed.Load() {
		return nil, fmt.Errorf("shared memory store is closed")
	}

	if req.Namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_UNSPECIFIED {
		return nil, fmt.Errorf("namespace cannot be unspecified")
	}
	if req.Key == "" {
		return nil, fmt.Errorf("key cannot be empty")
	}

	// Auto-scope key for AGENT namespace
	scopedKey := scopeKey(req.Namespace, req.AgentId, req.Key)

	// Instrument with Hawk
	var span *observability.Span
	if s.tracer != nil {
		_, span = s.tracer.StartSpan(ctx, SpanSharedMemoryDelete)
		defer s.tracer.EndSpan(span)
		span.SetAttribute("namespace", req.Namespace.String())
		span.SetAttribute("key", req.Key)
		span.SetAttribute("scoped_key", scopedKey)
		span.SetAttribute("agent_id", req.AgentId)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	nsData := s.data[req.Namespace]
	nsStats := s.stats[req.Namespace]

	existing, exists := nsData[scopedKey]
	if !exists {
		if span != nil {
			span.SetAttribute("deleted", false)
		}
		return &loomv1.DeleteSharedMemoryResponse{
			Deleted:        false,
			DeletedVersion: 0,
		}, nil
	}

	// Optimistic concurrency check
	if req.ExpectedVersion > 0 && existing.Version != req.ExpectedVersion {
		nsStats.conflictCount.Add(1)
		return nil, fmt.Errorf("version conflict: expected %d, found %d", req.ExpectedVersion, existing.Version)
	}

	deletedVersion := existing.Version

	// Remove from storage (using scoped key)
	delete(nsData, scopedKey)

	// Update statistics
	nsStats.keyCount.Add(-1)
	nsStats.totalBytes.Add(-int64(len(existing.Value)))
	nsStats.lastAccessAt.Store(time.Now())

	if span != nil {
		span.SetAttribute("deleted", true)
		span.SetAttribute("version", deletedVersion)
	}

	s.logger.Debug("shared memory delete",
		zap.String("namespace", req.Namespace.String()),
		zap.String("key", req.Key),
		zap.String("agent_id", req.AgentId),
		zap.Int64("deleted_version", deletedVersion))

	return &loomv1.DeleteSharedMemoryResponse{
		Deleted:        true,
		DeletedVersion: deletedVersion,
	}, nil
}

// List returns all keys matching a pattern in a namespace.
func (s *SharedMemoryStore) List(ctx context.Context, req *loomv1.ListSharedMemoryKeysRequest) (*loomv1.ListSharedMemoryKeysResponse, error) {
	if s.closed.Load() {
		return nil, fmt.Errorf("shared memory store is closed")
	}

	if req.Namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_UNSPECIFIED {
		return nil, fmt.Errorf("namespace cannot be unspecified")
	}

	// Instrument with Hawk
	var span *observability.Span
	if s.tracer != nil {
		_, span = s.tracer.StartSpan(ctx, SpanSharedMemoryList)
		defer s.tracer.EndSpan(span)
		span.SetAttribute("namespace", req.Namespace.String())
		span.SetAttribute("key_pattern", req.KeyPattern)
		span.SetAttribute("agent_id", req.AgentId)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	nsData := s.data[req.Namespace]
	nsStats := s.stats[req.Namespace]

	var keys []string
	agentPrefix := fmt.Sprintf("agent:%s:", req.AgentId)

	for key := range nsData {
		// For AGENT namespace, only return keys for this agent
		if req.Namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT {
			if !strings.HasPrefix(key, agentPrefix) {
				continue // Skip keys from other agents
			}
			// Strip the agent prefix from the returned key
			key = strings.TrimPrefix(key, agentPrefix)
		}

		if req.KeyPattern == "" || matchesKeyPattern(req.KeyPattern, key) {
			keys = append(keys, key)
		}
	}

	// Update statistics
	nsStats.readCount.Add(1)
	nsStats.lastAccessAt.Store(time.Now())

	if span != nil {
		span.SetAttribute("key_count", len(keys))
	}

	s.logger.Debug("shared memory list",
		zap.String("namespace", req.Namespace.String()),
		zap.String("key_pattern", req.KeyPattern),
		zap.String("agent_id", req.AgentId),
		zap.Int("key_count", len(keys)))

	return &loomv1.ListSharedMemoryKeysResponse{
		Keys:       keys,
		TotalCount: int32(len(keys)),
	}, nil
}

// Watch creates a watcher for changes in a namespace.
// Returns a channel that receives SharedMemoryValue updates.
func (s *SharedMemoryStore) Watch(ctx context.Context, req *loomv1.WatchSharedMemoryRequest) (<-chan *loomv1.SharedMemoryValue, error) {
	if s.closed.Load() {
		return nil, fmt.Errorf("shared memory store is closed")
	}

	if req.Namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_UNSPECIFIED {
		return nil, fmt.Errorf("namespace cannot be unspecified")
	}
	if req.AgentId == "" {
		return nil, fmt.Errorf("agent ID cannot be empty")
	}

	// Instrument with Hawk
	var span *observability.Span
	if s.tracer != nil {
		_, span = s.tracer.StartSpan(ctx, SpanSharedMemoryWatch)
		defer s.tracer.EndSpan(span)
		span.SetAttribute("namespace", req.Namespace.String())
		span.SetAttribute("key_pattern", req.KeyPattern)
		span.SetAttribute("agent_id", req.AgentId)
		span.SetAttribute("include_initial", req.IncludeInitial)
	}

	watcherID := fmt.Sprintf("%s-%s-%d", req.AgentId, req.Namespace.String(), time.Now().UnixNano())
	channel := make(chan *loomv1.SharedMemoryValue, 100) // Buffered channel

	watcher := &SharedMemoryWatcher{
		id:         watcherID,
		agentID:    req.AgentId,
		namespace:  req.Namespace,
		keyPattern: req.KeyPattern,
		channel:    channel,
		created:    time.Now(),
	}

	s.mu.Lock()
	s.watchers[req.Namespace] = append(s.watchers[req.Namespace], watcher)
	s.mu.Unlock()

	// Send initial values if requested
	if req.IncludeInitial {
		s.mu.RLock()
		nsData := s.data[req.Namespace]
		agentPrefix := fmt.Sprintf("agent:%s:", req.AgentId)

		for key, value := range nsData {
			// For AGENT namespace, only watch keys for this agent
			if req.Namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT {
				if !strings.HasPrefix(key, agentPrefix) {
					continue // Skip keys from other agents
				}
			}

			if req.KeyPattern == "" || matchesKeyPattern(req.KeyPattern, key) {
				// Non-blocking send
				select {
				case channel <- value:
				default:
					s.logger.Warn("watcher channel full, dropping initial value",
						zap.String("watcher_id", watcherID),
						zap.String("key", key))
				}
			}
		}
		s.mu.RUnlock()
	}

	s.logger.Info("shared memory watch created",
		zap.String("watcher_id", watcherID),
		zap.String("namespace", req.Namespace.String()),
		zap.String("key_pattern", req.KeyPattern),
		zap.String("agent_id", req.AgentId))

	return channel, nil
}

// GetStats retrieves statistics for a namespace.
func (s *SharedMemoryStore) GetStats(ctx context.Context, namespace loomv1.SharedMemoryNamespace) (*loomv1.SharedMemoryStats, error) {
	if s.closed.Load() {
		return nil, fmt.Errorf("shared memory store is closed")
	}

	if namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_UNSPECIFIED {
		return nil, fmt.Errorf("namespace cannot be unspecified")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	nsStats := s.stats[namespace]
	watchers := s.watchers[namespace]

	lastAccess := int64(0)
	if val := nsStats.lastAccessAt.Load(); val != nil {
		if t, ok := val.(time.Time); ok {
			lastAccess = t.UnixMilli()
		}
	}

	return &loomv1.SharedMemoryStats{
		Namespace:     namespace,
		KeyCount:      nsStats.keyCount.Load(),
		TotalBytes:    nsStats.totalBytes.Load(),
		ReadCount:     nsStats.readCount.Load(),
		WriteCount:    nsStats.writeCount.Load(),
		ConflictCount: nsStats.conflictCount.Load(),
		WatcherCount:  int32(len(watchers)),
		LastAccessAt:  lastAccess,
	}, nil
}

// Close closes the shared memory store and all watchers.
func (s *SharedMemoryStore) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil // Already closed
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Close all watcher channels
	for namespace, watchers := range s.watchers {
		for _, watcher := range watchers {
			close(watcher.channel)
		}
		s.logger.Info("closed watchers for namespace",
			zap.String("namespace", namespace.String()),
			zap.Int("count", len(watchers)))
	}
	s.watchers = make(map[loomv1.SharedMemoryNamespace][]*SharedMemoryWatcher)

	// Close encoder/decoder
	if s.encoder != nil {
		s.encoder.Close()
	}
	if s.decoder != nil {
		s.decoder.Close()
	}

	s.logger.Info("shared memory store closed")

	return nil
}

// notifyWatchers sends a value update to all matching watchers.
// Must be called with s.mu locked.
// scopedKey is the actual storage key (may include agent prefix for AGENT namespace).
func (s *SharedMemoryStore) notifyWatchers(namespace loomv1.SharedMemoryNamespace, scopedKey string, value *loomv1.SharedMemoryValue) {
	watchers := s.watchers[namespace]
	for _, watcher := range watchers {
		// For AGENT namespace, only notify watchers for the same agent
		if namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT {
			agentPrefix := fmt.Sprintf("agent:%s:", watcher.agentID)
			if !strings.HasPrefix(scopedKey, agentPrefix) {
				continue // Skip notification for other agents
			}
		}

		// Match against the user-visible key (not the scoped storage key)
		if watcher.keyPattern == "" || matchesKeyPattern(watcher.keyPattern, value.Key) {
			// Non-blocking send
			select {
			case watcher.channel <- value:
			default:
				s.logger.Warn("watcher channel full, dropping update",
					zap.String("watcher_id", watcher.id),
					zap.String("key", value.Key))
			}
		}
	}
}

// matchesKeyPattern checks if a key matches a pattern with wildcard support.
// Supports patterns like "config.*", "session.user.*", etc.
func matchesKeyPattern(pattern, key string) bool {
	// Exact match
	if pattern == key {
		return true
	}

	// Wildcard match using path.Match semantics
	matched, err := path.Match(pattern, key)
	if err != nil {
		return false
	}
	return matched
}
