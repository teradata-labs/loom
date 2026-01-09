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
	"sync"
	"testing"
	"time"
)

func TestNewMemory(t *testing.T) {
	mem := NewMemory()

	if mem == nil {
		t.Fatal("Expected non-nil memory")
	}

	if mem.sessions == nil {
		t.Error("Expected sessions map to be initialized")
	}

	if mem.store != nil {
		t.Error("Expected store to be nil for NewMemory")
	}
}

func TestMemory_GetOrCreateSession(t *testing.T) {
	mem := NewMemory()

	session := mem.GetOrCreateSession("test-session")

	if session == nil {
		t.Fatal("Expected non-nil session")
	}

	if session.ID != "test-session" {
		t.Errorf("Expected ID 'test-session', got %s", session.ID)
	}

	if session.Messages == nil {
		t.Error("Expected messages to be initialized")
	}

	if session.Context == nil {
		t.Error("Expected context to be initialized")
	}
}

func TestMemory_GetOrCreateSession_ExistingSession(t *testing.T) {
	mem := NewMemory()

	session1 := mem.GetOrCreateSession("test-session")
	session1.AddMessage(Message{Role: "user", Content: "test"})

	session2 := mem.GetOrCreateSession("test-session")

	if session1 != session2 {
		t.Error("Expected same session instance")
	}

	if len(session2.Messages) != 1 {
		t.Error("Expected session to retain messages")
	}
}

func TestMemory_GetSession(t *testing.T) {
	mem := NewMemory()

	// Create session
	_ = mem.GetOrCreateSession("test-session")

	// Retrieve it
	session, ok := mem.GetSession("test-session")
	if !ok {
		t.Error("Expected session to exist")
	}

	if session.ID != "test-session" {
		t.Errorf("Expected ID 'test-session', got %s", session.ID)
	}
}

func TestMemory_GetSession_NotFound(t *testing.T) {
	mem := NewMemory()

	_, ok := mem.GetSession("nonexistent")
	if ok {
		t.Error("Expected session to not exist")
	}
}

func TestMemory_DeleteSession(t *testing.T) {
	mem := NewMemory()

	mem.GetOrCreateSession("test-session")

	if _, ok := mem.GetSession("test-session"); !ok {
		t.Fatal("Expected session to exist")
	}

	mem.DeleteSession("test-session")

	if _, ok := mem.GetSession("test-session"); ok {
		t.Error("Expected session to be deleted")
	}
}

func TestMemory_ListSessions(t *testing.T) {
	mem := NewMemory()

	mem.GetOrCreateSession("session1")
	mem.GetOrCreateSession("session2")
	mem.GetOrCreateSession("session3")

	sessions := mem.ListSessions()
	if len(sessions) != 3 {
		t.Errorf("Expected 3 sessions, got %d", len(sessions))
	}
}

func TestMemory_CountSessions(t *testing.T) {
	mem := NewMemory()

	if mem.CountSessions() != 0 {
		t.Error("Expected count to be 0")
	}

	mem.GetOrCreateSession("session1")
	mem.GetOrCreateSession("session2")

	if mem.CountSessions() != 2 {
		t.Errorf("Expected count to be 2, got %d", mem.CountSessions())
	}

	mem.DeleteSession("session1")

	if mem.CountSessions() != 1 {
		t.Errorf("Expected count to be 1, got %d", mem.CountSessions())
	}
}

func TestMemory_ClearAll(t *testing.T) {
	mem := NewMemory()

	mem.GetOrCreateSession("session1")
	mem.GetOrCreateSession("session2")

	if mem.CountSessions() != 2 {
		t.Fatal("Expected 2 sessions before clear")
	}

	mem.ClearAll()

	if mem.CountSessions() != 0 {
		t.Errorf("Expected 0 sessions after clear, got %d", mem.CountSessions())
	}
}

func TestMemory_ConcurrentAccess(t *testing.T) {
	mem := NewMemory()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sessionID := "session"
			session := mem.GetOrCreateSession(sessionID)
			session.AddMessage(Message{Role: "user", Content: "test"})
			_, _ = mem.GetSession(sessionID)
			_ = mem.ListSessions()
			_ = mem.CountSessions()
		}(i)
	}

	wg.Wait()
}

func TestMemory_MultipleSessions_Concurrent(t *testing.T) {
	mem := NewMemory()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sessionID := "session-" + string(rune('A'+id))
			session := mem.GetOrCreateSession(sessionID)
			session.AddMessage(Message{Role: "user", Content: "test"})
		}(i)
	}

	wg.Wait()

	if mem.CountSessions() != 50 {
		t.Errorf("Expected 50 sessions, got %d", mem.CountSessions())
	}
}

func TestSession_AddMessage(t *testing.T) {
	session := &Session{
		ID:        "test",
		Messages:  []Message{},
		Context:   make(map[string]interface{}),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	initialUpdate := session.UpdatedAt

	time.Sleep(10 * time.Millisecond)

	session.AddMessage(Message{
		Role:       "user",
		Content:    "test message",
		TokenCount: 10,
		CostUSD:    0.001,
	})

	if len(session.Messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(session.Messages))
	}

	if session.TotalTokens != 10 {
		t.Errorf("Expected 10 tokens, got %d", session.TotalTokens)
	}

	if session.TotalCostUSD != 0.001 {
		t.Errorf("Expected cost 0.001, got %f", session.TotalCostUSD)
	}

	if !session.UpdatedAt.After(initialUpdate) {
		t.Error("Expected UpdatedAt to be updated")
	}
}

func TestSession_GetMessages(t *testing.T) {
	session := &Session{
		ID:       "test",
		Messages: []Message{},
		Context:  make(map[string]interface{}),
	}

	session.AddMessage(Message{Role: "user", Content: "msg1"})
	session.AddMessage(Message{Role: "assistant", Content: "msg2"})

	messages := session.GetMessages()
	if len(messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(messages))
	}

	if messages[0].Content != "msg1" {
		t.Error("Expected first message to be 'msg1'")
	}

	if messages[1].Content != "msg2" {
		t.Error("Expected second message to be 'msg2'")
	}
}
