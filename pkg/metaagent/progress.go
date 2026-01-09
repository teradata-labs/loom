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
package metaagent

import (
	"context"
	"sync"
	"time"
)

// ProgressEventType represents the type of progress event
type ProgressEventType string

const (
	// Weaver/Mender sub-agent events
	EventSubAgentStarted   ProgressEventType = "sub_agent_started"
	EventSubAgentCompleted ProgressEventType = "sub_agent_completed"
	EventSubAgentFailed    ProgressEventType = "sub_agent_failed"

	// Workflow orchestration events
	EventWorkflowStarted   ProgressEventType = "workflow_started"
	EventWorkflowCompleted ProgressEventType = "workflow_completed"
	EventAgentStarted      ProgressEventType = "agent_started"
	EventAgentCompleted    ProgressEventType = "agent_completed"
	EventAgentFailed       ProgressEventType = "agent_failed"

	// Interactive events
	EventQuestionAsked    ProgressEventType = "question_asked"
	EventQuestionAnswered ProgressEventType = "question_answered"

	// Validation events
	EventValidationStarted ProgressEventType = "validation_started"
	EventValidationPassed  ProgressEventType = "validation_passed"
	EventValidationFailed  ProgressEventType = "validation_failed"
)

// ProgressEvent represents a progress update during workflow execution
type ProgressEvent struct {
	Type      ProgressEventType      `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	AgentName string                 `json:"agent_name,omitempty"`
	Message   string                 `json:"message"`
	Details   map[string]interface{} `json:"details,omitempty"`
	Error     string                 `json:"error,omitempty"`

	// For questions
	Question *Question `json:"question,omitempty"`
}

// Question represents an interactive question to the user
type Question struct {
	ID      string        `json:"id"`
	Prompt  string        `json:"prompt"`
	Options []string      `json:"options,omitempty"` // Empty for free-form
	Context string        `json:"context,omitempty"` // Additional context
	Timeout time.Duration `json:"timeout,omitempty"` // Optional timeout for response (0 = use default)

	// Answer channel for synchronous question-answer flow
	// The emitter creates this channel, emits the question, and waits on it for the answer.
	// The UI layer (TUI/console) receives the question event and sends the answer via this channel.
	// Lifecycle: Created by emitter → used by UI → closed by UI after sending answer.
	// Buffer size of 1 prevents blocking if answer arrives before wait.
	AnswerChan chan string `json:"-"`
}

// ProgressListener receives progress events
type ProgressListener interface {
	OnProgress(event *ProgressEvent)
}

// ProgressCallback is a function that handles progress events
type ProgressCallback func(*ProgressEvent)

// OnProgress implements ProgressListener for function callbacks
func (f ProgressCallback) OnProgress(event *ProgressEvent) {
	f(event)
}

// ProgressMultiplexer broadcasts events to multiple listeners.
// Thread-safe for concurrent use by multiple goroutines.
type ProgressMultiplexer struct {
	mu        sync.RWMutex
	listeners []ProgressListener
}

// NewProgressMultiplexer creates a new multiplexer
func NewProgressMultiplexer() *ProgressMultiplexer {
	return &ProgressMultiplexer{
		listeners: []ProgressListener{},
	}
}

// AddListener adds a progress listener.
// Safe for concurrent use.
func (pm *ProgressMultiplexer) AddListener(listener ProgressListener) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.listeners = append(pm.listeners, listener)
}

// RemoveListener removes a progress listener.
// Safe for concurrent use.
func (pm *ProgressMultiplexer) RemoveListener(listener ProgressListener) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for i, l := range pm.listeners {
		if l == listener {
			// Remove by replacing with last element and truncating
			pm.listeners[i] = pm.listeners[len(pm.listeners)-1]
			pm.listeners = pm.listeners[:len(pm.listeners)-1]
			break
		}
	}
}

// Emit broadcasts an event to all listeners.
// Safe for concurrent use - multiple goroutines can emit simultaneously.
func (pm *ProgressMultiplexer) Emit(event *ProgressEvent) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	// Iterate over a snapshot of listeners while holding read lock
	for _, listener := range pm.listeners {
		listener.OnProgress(event)
	}
}

// EmitSubAgentStarted emits a sub-agent started event
func (pm *ProgressMultiplexer) EmitSubAgentStarted(name, message string) {
	pm.Emit(&ProgressEvent{
		Type:      EventSubAgentStarted,
		Timestamp: time.Now(),
		AgentName: name,
		Message:   message,
	})
}

// EmitSubAgentCompleted emits a sub-agent completed event
func (pm *ProgressMultiplexer) EmitSubAgentCompleted(name, message string, details map[string]interface{}) {
	pm.Emit(&ProgressEvent{
		Type:      EventSubAgentCompleted,
		Timestamp: time.Now(),
		AgentName: name,
		Message:   message,
		Details:   details,
	})
}

// EmitSubAgentFailed emits a sub-agent failed event
func (pm *ProgressMultiplexer) EmitSubAgentFailed(name, message string, err error) {
	event := &ProgressEvent{
		Type:      EventSubAgentFailed,
		Timestamp: time.Now(),
		AgentName: name,
		Message:   message,
	}
	if err != nil {
		event.Error = err.Error()
	}
	pm.Emit(event)
}

// EmitQuestion emits a question event and waits for answer
func (pm *ProgressMultiplexer) EmitQuestion(question *Question) {
	pm.Emit(&ProgressEvent{
		Type:      EventQuestionAsked,
		Timestamp: time.Now(),
		Message:   question.Prompt,
		Question:  question,
	})
}

// EmitValidation emits validation events
func (pm *ProgressMultiplexer) EmitValidationStarted() {
	pm.Emit(&ProgressEvent{
		Type:      EventValidationStarted,
		Timestamp: time.Now(),
		Message:   "Running multi-judge validation",
	})
}

func (pm *ProgressMultiplexer) EmitValidationPassed(score float64) {
	pm.Emit(&ProgressEvent{
		Type:      EventValidationPassed,
		Timestamp: time.Now(),
		Message:   "Validation passed",
		Details: map[string]interface{}{
			"score": score,
		},
	})
}

func (pm *ProgressMultiplexer) EmitValidationFailed(score float64, issues int) {
	pm.Emit(&ProgressEvent{
		Type:      EventValidationFailed,
		Timestamp: time.Now(),
		Message:   "Validation failed",
		Details: map[string]interface{}{
			"score":  score,
			"issues": issues,
		},
	})
}

// ProgressContextKey is the context key for progress multiplexer
type progressContextKey struct{}

// WithProgress adds a progress multiplexer to the context
func WithProgress(ctx context.Context, pm *ProgressMultiplexer) context.Context {
	return context.WithValue(ctx, progressContextKey{}, pm)
}

// FromContext retrieves the progress multiplexer from context
func FromContext(ctx context.Context) *ProgressMultiplexer {
	if pm, ok := ctx.Value(progressContextKey{}).(*ProgressMultiplexer); ok {
		return pm
	}
	return nil
}

// EmitProgress is a helper to emit progress from context
func EmitProgress(ctx context.Context, event *ProgressEvent) {
	if pm := FromContext(ctx); pm != nil {
		pm.Emit(event)
	}
}
