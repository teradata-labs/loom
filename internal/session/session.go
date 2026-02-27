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
// Package session provides session types compatible with Crush's interface.
package session

import (
	"context"

	"github.com/teradata-labs/loom/internal/pubsub"
)

// Session represents a chat session.
type Session struct {
	ID               string
	Title            string
	CreatedAt        int64
	UpdatedAt        int64
	CompletionTokens int
	PromptTokens     int
	Cost             float64
	Todos            []Todo
	Model            string // Model used in this session (e.g., "claude-sonnet-4-6")
	Provider         string // Provider used in this session (e.g., "anthropic")
}

// Merge returns a copy of s with non-zero fields from update applied.
// This preserves existing fields like Title and Todos when receiving
// partial updates (e.g., cost/token updates from the coordinator).
func (s Session) Merge(update Session) Session {
	result := s
	if update.CompletionTokens > 0 {
		result.CompletionTokens = update.CompletionTokens
	}
	if update.PromptTokens > 0 {
		result.PromptTokens = update.PromptTokens
	}
	if update.Cost > 0 {
		result.Cost = update.Cost
	}
	if update.Model != "" {
		result.Model = update.Model
	}
	if update.Provider != "" {
		result.Provider = update.Provider
	}
	if update.Title != "" {
		result.Title = update.Title
	}
	if update.UpdatedAt > 0 {
		result.UpdatedAt = update.UpdatedAt
	}
	if len(update.Todos) > 0 {
		result.Todos = update.Todos
	}
	return result
}

// Todo represents a todo item.
type Todo struct {
	Content    string
	ActiveForm string
	Status     TodoStatus
}

// TodoStatus represents the status of a todo item.
type TodoStatus string

const (
	TodoStatusPending    TodoStatus = "pending"
	TodoStatusInProgress TodoStatus = "in_progress"
	TodoStatusCompleted  TodoStatus = "completed"
)

// Service defines the session service interface.
type Service interface {
	Create(ctx context.Context, title string) (Session, error)
	Get(ctx context.Context, id string) (Session, error)
	List(ctx context.Context) ([]Session, error)
	Delete(ctx context.Context, id string) error
	Subscribe(ctx context.Context) <-chan pubsub.Event[Session]
	ParseAgentToolSessionID(sessionID string) (string, string, bool)
	CreateAgentToolSessionID(messageID, toolCallID string) string
}
