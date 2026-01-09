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
// Package message provides message types compatible with Crush's interface.
package message

import (
	"context"
	"fmt"

	"github.com/teradata-labs/loom/internal/pubsub"
)

// Role represents the role of a message sender.
type Role string

const (
	User      Role = "user"
	Assistant Role = "assistant"
	Tool      Role = "tool"
	System    Role = "system"
)

// FinishReason represents the reason a message finished.
type FinishReason string

const (
	FinishReasonEndTurn   FinishReason = "end_turn"
	FinishReasonCanceled  FinishReason = "canceled"
	FinishReasonMaxTokens FinishReason = "max_tokens"
	FinishReasonError     FinishReason = "error"
)

// Message represents a chat message.
type Message struct {
	ID        string
	SessionID string
	Role      Role
	CreatedAt int64
	Provider  string // LLM provider
	Model     string // LLM model
	parts     []ContentPart
	finish    *FinishPart
}

// NewMessage creates a new message.
func NewMessage(id, sessionID string, role Role) Message {
	return Message{
		ID:        id,
		SessionID: sessionID,
		Role:      role,
	}
}

// AddPart adds a content part to the message.
func (m *Message) AddPart(part ContentPart) {
	m.parts = append(m.parts, part)
}

// Parts returns the content parts.
func (m Message) Parts() []ContentPart {
	return m.parts
}

// Content returns the text content.
func (m Message) Content() ContentText {
	var text string
	for _, p := range m.parts {
		if t, ok := p.(ContentText); ok {
			text += t.Text
		}
	}
	return ContentText{Text: text}
}

// ReasoningContent returns thinking content.
func (m Message) ReasoningContent() ReasoningContent {
	for _, p := range m.parts {
		if r, ok := p.(ReasoningContent); ok {
			return r
		}
	}
	return ReasoningContent{}
}

// ToolCalls returns tool calls from the message.
func (m Message) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, p := range m.parts {
		if tc, ok := p.(ToolCall); ok {
			calls = append(calls, tc)
		}
	}
	return calls
}

// ToolResults returns tool results from the message.
func (m Message) ToolResults() []ToolResult {
	var results []ToolResult
	for _, p := range m.parts {
		if tr, ok := p.(ToolResult); ok {
			results = append(results, tr)
		}
	}
	return results
}

// FinishPart returns the finish part if present.
func (m Message) FinishPart() *FinishPart {
	return m.finish
}

// IsThinking returns true if the message is thinking.
func (m Message) IsThinking() bool {
	for _, p := range m.parts {
		if _, ok := p.(ReasoningContent); ok {
			return true
		}
	}
	return false
}

// IsSummaryMessage returns true if this is a summary message.
func (m Message) IsSummaryMessage() bool {
	return false // Not supported in Loom yet
}

// IsFinished returns true if the message is finished.
func (m Message) IsFinished() bool {
	return m.finish != nil
}

// BinaryContent returns any binary content in the message.
func (m Message) BinaryContent() []BinaryContent {
	return nil // Not supported in Loom yet
}

// ThinkingDuration returns the thinking duration.
func (m Message) ThinkingDuration() Duration {
	r := m.ReasoningContent()
	if r.EndedAt > 0 && r.StartedAt > 0 {
		return Duration(r.EndedAt - r.StartedAt)
	}
	return 0
}

// Duration represents a duration in milliseconds.
type Duration int64

// String returns a formatted duration string.
func (d Duration) String() string {
	if d < 1000 {
		return "<1s"
	}
	secs := d / 1000
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	secs = secs % 60
	return fmt.Sprintf("%dm%ds", mins, secs)
}

// BinaryContent represents binary data in a message.
type BinaryContent struct {
	Type     string
	MimeType string
	Data     []byte
	Path     string
}

// ContentPart is a marker interface for content parts.
type ContentPart interface {
	isContentPart()
}

// ContentText represents text content.
type ContentText struct {
	Text string
}

func (ContentText) isContentPart() {}

func (c ContentText) String() string {
	return c.Text
}

// ReasoningContent represents thinking content.
type ReasoningContent struct {
	Thinking   string
	StartedAt  int64
	EndedAt    int64
	FinishedAt int64 // Alias for EndedAt
}

func (ReasoningContent) isContentPart() {}

// ToolCall represents a tool invocation.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
	Input     string // Raw input JSON
	Finished  bool   // Whether the tool call has finished
}

func (ToolCall) isContentPart() {}

// ToolResult represents the result of a tool execution.
type ToolResult struct {
	ToolCallID string
	Content    string
	IsError    bool
	Data       []byte // Binary data result
	MIMEType   string // MIME type of data
	Metadata   string // Tool-specific metadata (JSON)
}

func (ToolResult) isContentPart() {}

// FinishPart represents the finish metadata.
type FinishPart struct {
	Reason  FinishReason
	Message string
	Details string
	Time    int64
}

func (FinishPart) isContentPart() {}

// Attachment represents a file attachment.
type Attachment struct {
	Type     string
	Name     string
	Path     string
	MimeType string
	Data     []byte
	// Aliases for compatibility
	FilePath string
	FileName string
	Content  []byte
}

// Service defines the message service interface.
type Service interface {
	List(ctx context.Context, sessionID string) ([]Message, error)
	Subscribe(ctx context.Context) <-chan pubsub.Event[Message]
}
