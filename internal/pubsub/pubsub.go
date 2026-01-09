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
// Package pubsub provides event pub/sub types compatible with Crush's interface.
package pubsub

// EventType represents the type of event.
type EventType int

const (
	// CreatedEvent indicates a new item was created.
	CreatedEvent EventType = iota
	// UpdatedEvent indicates an existing item was updated.
	UpdatedEvent
	// DeletedEvent indicates an item was deleted.
	DeletedEvent
)

// Event wraps an event with type information.
// Matches Crush's pubsub.Event[T] pattern.
type Event[T any] struct {
	Type    EventType
	Payload T
}

// NewCreatedEvent creates a new "created" event.
func NewCreatedEvent[T any](payload T) Event[T] {
	return Event[T]{Type: CreatedEvent, Payload: payload}
}

// NewUpdatedEvent creates a new "updated" event.
func NewUpdatedEvent[T any](payload T) Event[T] {
	return Event[T]{Type: UpdatedEvent, Payload: payload}
}

// NewDeletedEvent creates a new "deleted" event.
func NewDeletedEvent[T any](payload T) Event[T] {
	return Event[T]{Type: DeletedEvent, Payload: payload}
}

// UpdateAvailableMsg is sent when an update is available.
type UpdateAvailableMsg struct {
	CurrentVersion string
	LatestVersion  string
	IsDevelopment  bool
}
