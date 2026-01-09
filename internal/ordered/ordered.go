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
// Package ordered provides ordered map utilities (stub for github.com/charmbracelet/x/exp/ordered).
package ordered

// Map is an ordered map.
type Map[K comparable, V any] struct {
	keys   []K
	values map[K]V
}

// New creates a new ordered map.
func New[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{
		keys:   make([]K, 0),
		values: make(map[K]V),
	}
}

// Set sets a value.
func (m *Map[K, V]) Set(key K, value V) {
	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

// Get gets a value.
func (m *Map[K, V]) Get(key K) (V, bool) {
	v, ok := m.values[key]
	return v, ok
}

// Delete deletes a value.
func (m *Map[K, V]) Delete(key K) {
	if _, exists := m.values[key]; exists {
		delete(m.values, key)
		for i, k := range m.keys {
			if k == key {
				m.keys = append(m.keys[:i], m.keys[i+1:]...)
				break
			}
		}
	}
}

// Keys returns all keys in order.
func (m *Map[K, V]) Keys() []K {
	result := make([]K, len(m.keys))
	copy(result, m.keys)
	return result
}

// Values returns all values in order.
func (m *Map[K, V]) Values() []V {
	result := make([]V, 0, len(m.keys))
	for _, k := range m.keys {
		result = append(result, m.values[k])
	}
	return result
}

// Len returns the map length.
func (m *Map[K, V]) Len() int {
	return len(m.keys)
}

// Clear clears the map.
func (m *Map[K, V]) Clear() {
	m.keys = m.keys[:0]
	m.values = make(map[K]V)
}

// Range iterates over the map in order.
func (m *Map[K, V]) Range(fn func(key K, value V) bool) {
	for _, k := range m.keys {
		if !fn(k, m.values[k]) {
			break
		}
	}
}

// Clamp constrains a value within a range [min, max].
func Clamp[T ~int | ~int64 | ~float64](val, minVal, maxVal T) T {
	if val < minVal {
		return minVal
	}
	if val > maxVal {
		return maxVal
	}
	return val
}
