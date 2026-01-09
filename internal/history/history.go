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
// Package history provides file history types.
package history

import (
	"context"

	"github.com/teradata-labs/loom/internal/pubsub"
)

// Entry represents a history entry.
type Entry struct {
	ID        string
	SessionID string
	Path      string
	CreatedAt int64
}

// File represents a file with version info.
type File struct {
	ID        string
	SessionID string
	Path      string
	CreatedAt int64
	Version   int
	Content   string
}

// Service defines the history service interface.
type Service interface {
	List(ctx context.Context, sessionID string) ([]Entry, error)
	ListBySession(ctx context.Context, sessionID string) ([]File, error)
	Subscribe(ctx context.Context) <-chan pubsub.Event[Entry]
}

// NoopService is a no-op history service.
type NoopService struct{}

// List returns an empty list.
func (s *NoopService) List(ctx context.Context, sessionID string) ([]Entry, error) {
	return nil, nil
}

// ListBySession returns an empty list.
func (s *NoopService) ListBySession(ctx context.Context, sessionID string) ([]File, error) {
	return nil, nil
}

// Subscribe returns a closed channel.
func (s *NoopService) Subscribe(ctx context.Context) <-chan pubsub.Event[Entry] {
	ch := make(chan pubsub.Event[Entry])
	close(ch)
	return ch
}
