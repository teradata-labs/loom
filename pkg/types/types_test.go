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

package types

import (
	"testing"
	"time"
)

func TestSession_MessageCount(t *testing.T) {
	tests := []struct {
		name     string
		messages []Message
		want     int32
	}{
		{
			name:     "empty session",
			messages: []Message{},
			want:     0,
		},
		{
			name: "single message",
			messages: []Message{
				{Role: "user", Content: "Hello"},
			},
			want: 1,
		},
		{
			name: "multiple messages with different roles",
			messages: []Message{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi there"},
				{Role: "tool", Content: "Tool result"},
			},
			want: 3,
		},
		{
			name: "many messages",
			messages: func() []Message {
				msgs := make([]Message, 100)
				for i := 0; i < 100; i++ {
					msgs[i] = Message{
						Role:    "user",
						Content: "Message",
					}
				}
				return msgs
			}(),
			want: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := &Session{
				ID:        "test_session",
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
				Messages:  tt.messages,
			}

			got := session.MessageCount()
			if got != tt.want {
				t.Errorf("MessageCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSession_MessageCount_ThreadSafe(t *testing.T) {
	session := &Session{
		ID:        "test_session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages:  []Message{},
	}

	// Simulate concurrent reads and writes
	done := make(chan bool)

	// Writer goroutine - adds messages
	go func() {
		for i := 0; i < 100; i++ {
			msg := Message{
				Role:    "user",
				Content: "Test message",
			}
			session.AddMessage(msg)
			time.Sleep(time.Microsecond)
		}
		done <- true
	}()

	// Reader goroutines - read message count
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				_ = session.MessageCount()
				time.Sleep(time.Microsecond)
			}
		}()
	}

	// Wait for writer to complete
	<-done

	// Final count should be 100
	finalCount := session.MessageCount()
	if finalCount != 100 {
		t.Errorf("Final MessageCount() = %d, want 100", finalCount)
	}
}
