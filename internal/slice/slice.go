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
// Package slice provides slice utilities (stub for github.com/charmbracelet/x/exp/slice).
package slice

// Map applies a function to each element.
func Map[T, U any](s []T, fn func(T) U) []U {
	result := make([]U, len(s))
	for i, v := range s {
		result[i] = fn(v)
	}
	return result
}

// Filter filters a slice.
func Filter[T any](s []T, fn func(T) bool) []T {
	var result []T
	for _, v := range s {
		if fn(v) {
			result = append(result, v)
		}
	}
	return result
}

// Find finds an element.
func Find[T any](s []T, fn func(T) bool) (T, bool) {
	for _, v := range s {
		if fn(v) {
			return v, true
		}
	}
	var zero T
	return zero, false
}

// Contains checks if slice contains element.
func Contains[T comparable](s []T, v T) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Unique returns unique elements.
func Unique[T comparable](s []T) []T {
	seen := make(map[T]bool)
	var result []T
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}

// Reverse reverses a slice.
func Reverse[T any](s []T) []T {
	result := make([]T, len(s))
	for i, v := range s {
		result[len(s)-1-i] = v
	}
	return result
}

// Flatten flattens nested slices.
func Flatten[T any](s [][]T) []T {
	var result []T
	for _, inner := range s {
		result = append(result, inner...)
	}
	return result
}

// Chunk splits a slice into chunks.
func Chunk[T any](s []T, size int) [][]T {
	if size <= 0 {
		return nil
	}
	var result [][]T
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		result = append(result, s[i:end])
	}
	return result
}

// First returns the first element.
func First[T any](s []T) (T, bool) {
	if len(s) == 0 {
		var zero T
		return zero, false
	}
	return s[0], true
}

// Last returns the last element.
func Last[T any](s []T) (T, bool) {
	if len(s) == 0 {
		var zero T
		return zero, false
	}
	return s[len(s)-1], true
}

// Intersperse inserts sep between each element.
func Intersperse[T any](s []T, sep T) []T {
	if len(s) <= 1 {
		return s
	}
	result := make([]T, 0, len(s)*2-1)
	for i, v := range s {
		if i > 0 {
			result = append(result, sep)
		}
		result = append(result, v)
	}
	return result
}
