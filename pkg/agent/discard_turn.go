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
package agent

import (
	"context"
	"fmt"
)

// messageTurnDeleter is implemented by session stores that can drop every row
// belonging to one turn. Optional: stores that do not implement it leave the
// durable rows in place; in-memory history is still rolled back.
type messageTurnDeleter interface {
	DeleteMessagesByTurn(ctx context.Context, sessionID string, turn int64) error
}

// DiscardIncompleteTurn removes the session's current (latest) turn when that
// turn never finished — Chat persists the user message before the conversation
// loop runs, so a failed Chat leaves an orphan user row. Callers that retry
// Chat with the same content must discard first, or the next attempt appends a
// duplicate user message and opens a new turn.
//
// A completed turn (user + final assistant with no tool calls) is left alone,
// so a Chat that failed before appendMessage (for example SessionParkedError)
// does not erase prior history. In-turn SQLite databases for the session are
// dropped. Durable rows are deleted when the configured store supports it.
func (a *Agent) DiscardIncompleteTurn(ctx context.Context, sessionID string) error {
	if a == nil || a.memory == nil || sessionID == "" {
		return nil
	}
	session, ok := a.memory.GetSession(sessionID)
	if !ok || session == nil {
		return nil
	}

	turn := sessionCurrentTurn(session)
	if turn <= 0 || !isIncompleteTurn(session, turn) {
		return nil
	}

	session.DiscardTurn(turn)
	if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
		segMem.DiscardTurn(turn)
	}

	a.dropInTurnSQLite(sessionID)

	if store := a.memory.GetStore(); store != nil {
		if d, ok := store.(messageTurnDeleter); ok {
			if err := d.DeleteMessagesByTurn(ctx, sessionID, turn); err != nil {
				return fmt.Errorf("discard turn %d for session %s: %w", turn, sessionID, err)
			}
		}
	}
	return nil
}

// isIncompleteTurn reports whether turn has a user message and no final
// assistant reply (an assistant row with no tool calls). Mid-loop tool-call
// assistants do not count as completion.
func isIncompleteTurn(session *Session, turn int64) bool {
	var hasUser, hasFinalAssistant bool
	for _, m := range sessionMessagesRaw(session) {
		if m.Turn != turn {
			continue
		}
		switch m.Role {
		case "user":
			hasUser = true
		case "assistant":
			if len(m.ToolCalls) == 0 {
				hasFinalAssistant = true
			}
		}
	}
	return hasUser && !hasFinalAssistant
}

// sessionMessagesRaw returns the flat in-memory history, not the compiled LLM
// view. Segmented L1 is preferred when present so turn membership matches
// CurrentTurn.
func sessionMessagesRaw(session *Session) []Message {
	if session == nil {
		return nil
	}
	if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
		return segMem.GetMessages()
	}
	return session.GetMessages()
}
