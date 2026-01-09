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
// Package csync provides concurrent data structures.
package csync

import (
	"iter"
	"sync"
)

// Map is a concurrent-safe map.
type Map[K comparable, V any] struct {
	mu   sync.RWMutex
	data map[K]V
}

// NewMap creates a new concurrent map.
func NewMap[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{
		data: make(map[K]V),
	}
}

// Get retrieves a value from the map.
func (m *Map[K, V]) Get(key K) (V, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[key]
	return v, ok
}

// Set stores a value in the map.
func (m *Map[K, V]) Set(key K, value V) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
}

// Delete removes a value from the map.
func (m *Map[K, V]) Delete(key K) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
}

// Seq2 returns an iterator over key-value pairs.
func (m *Map[K, V]) Seq2() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		for k, v := range m.data {
			if !yield(k, v) {
				return
			}
		}
	}
}

// Seq iterates over map entries using a callback.
func (m *Map[K, V]) Seq(fn func(K, V) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for k, v := range m.data {
		if !fn(k, v) {
			break
		}
	}
}

// Values returns an iterator over values only.
func (m *Map[K, V]) Values() iter.Seq[V] {
	return func(yield func(V) bool) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		for _, v := range m.data {
			if !yield(v) {
				return
			}
		}
	}
}

// Clear clears the map.
func (m *Map[K, V]) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = make(map[K]V)
}

// Slice is a concurrent-safe slice.
type Slice[T any] struct {
	mu    sync.RWMutex
	items []T
}

// NewSlice creates a new concurrent-safe slice.
func NewSlice[T any]() *Slice[T] {
	return &Slice[T]{
		items: make([]T, 0),
	}
}

// Append appends an item to the slice.
func (s *Slice[T]) Append(item T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, item)
}

// Get returns the item at the given index.
func (s *Slice[T]) Get(index int) (T, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if index < 0 || index >= len(s.items) {
		var zero T
		return zero, false
	}
	return s.items[index], true
}

// Len returns the length of the slice.
func (s *Slice[T]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// Range iterates over the slice.
func (s *Slice[T]) Range(fn func(int, T) bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i, item := range s.items {
		if !fn(i, item) {
			break
		}
	}
}

// Items returns a copy of all items.
func (s *Slice[T]) Items() []T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]T, len(s.items))
	copy(result, s.items)
	return result
}

// Set replaces all items in the slice.
func (s *Slice[T]) Set(items []T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make([]T, len(items))
	copy(s.items, items)
}

// Clear removes all items from the slice.
func (s *Slice[T]) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make([]T, 0)
}

// SetSlice replaces all items in the slice (alias for Set).
func (s *Slice[T]) SetSlice(items []T) {
	s.Set(items)
}

// Seq returns an iterator over the slice items.
func (s *Slice[T]) Seq() iter.Seq[T] {
	return func(yield func(T) bool) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		for _, item := range s.items {
			if !yield(item) {
				return
			}
		}
	}
}
