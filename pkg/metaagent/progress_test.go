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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProgressMultiplexer_Emit(t *testing.T) {
	pm := NewProgressMultiplexer()

	var receivedEvents []*ProgressEvent
	var mu sync.Mutex

	listener := ProgressCallback(func(event *ProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		receivedEvents = append(receivedEvents, event)
	})

	pm.AddListener(listener)

	// Emit an event
	pm.Emit(&ProgressEvent{
		Type:      EventSubAgentStarted,
		Timestamp: time.Now(),
		AgentName: "test-agent",
		Message:   "Starting test",
	})

	// Give some time for event delivery (should be immediate but be safe)
	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, receivedEvents, 1)
	assert.Equal(t, EventSubAgentStarted, receivedEvents[0].Type)
	assert.Equal(t, "test-agent", receivedEvents[0].AgentName)
	assert.Equal(t, "Starting test", receivedEvents[0].Message)
}

func TestProgressMultiplexer_MultipleListeners(t *testing.T) {
	pm := NewProgressMultiplexer()

	var events1, events2 []*ProgressEvent
	var mu1, mu2 sync.Mutex

	listener1 := ProgressCallback(func(event *ProgressEvent) {
		mu1.Lock()
		defer mu1.Unlock()
		events1 = append(events1, event)
	})

	listener2 := ProgressCallback(func(event *ProgressEvent) {
		mu2.Lock()
		defer mu2.Unlock()
		events2 = append(events2, event)
	})

	pm.AddListener(listener1)
	pm.AddListener(listener2)

	// Emit event
	pm.Emit(&ProgressEvent{
		Type:    EventWorkflowStarted,
		Message: "Workflow started",
	})

	time.Sleep(10 * time.Millisecond)

	// Both listeners should receive the event
	mu1.Lock()
	assert.Len(t, events1, 1)
	mu1.Unlock()

	mu2.Lock()
	assert.Len(t, events2, 1)
	mu2.Unlock()
}

func TestProgressMultiplexer_EmitSubAgentStarted(t *testing.T) {
	pm := NewProgressMultiplexer()

	var receivedEvent *ProgressEvent
	listener := ProgressCallback(func(event *ProgressEvent) {
		receivedEvent = event
	})

	pm.AddListener(listener)
	pm.EmitSubAgentStarted("analyzer", "Analyzing requirements")

	time.Sleep(10 * time.Millisecond)

	require.NotNil(t, receivedEvent)
	assert.Equal(t, EventSubAgentStarted, receivedEvent.Type)
	assert.Equal(t, "analyzer", receivedEvent.AgentName)
	assert.Equal(t, "Analyzing requirements", receivedEvent.Message)
}

func TestProgressMultiplexer_EmitSubAgentCompleted(t *testing.T) {
	pm := NewProgressMultiplexer()

	var receivedEvent *ProgressEvent
	listener := ProgressCallback(func(event *ProgressEvent) {
		receivedEvent = event
	})

	pm.AddListener(listener)

	details := map[string]interface{}{
		"items_analyzed": 5,
		"duration":       "100ms",
	}

	pm.EmitSubAgentCompleted("analyzer", "Analysis complete", details)

	time.Sleep(10 * time.Millisecond)

	require.NotNil(t, receivedEvent)
	assert.Equal(t, EventSubAgentCompleted, receivedEvent.Type)
	assert.Equal(t, "analyzer", receivedEvent.AgentName)
	assert.Equal(t, "Analysis complete", receivedEvent.Message)
	assert.Equal(t, details, receivedEvent.Details)
}

func TestProgressMultiplexer_EmitSubAgentFailed(t *testing.T) {
	pm := NewProgressMultiplexer()

	var receivedEvent *ProgressEvent
	listener := ProgressCallback(func(event *ProgressEvent) {
		receivedEvent = event
	})

	pm.AddListener(listener)
	pm.EmitSubAgentFailed("analyzer", "Analysis failed", assert.AnError)

	time.Sleep(10 * time.Millisecond)

	require.NotNil(t, receivedEvent)
	assert.Equal(t, EventSubAgentFailed, receivedEvent.Type)
	assert.Equal(t, "analyzer", receivedEvent.AgentName)
	assert.Equal(t, "Analysis failed", receivedEvent.Message)
	assert.Contains(t, receivedEvent.Error, "assert.AnError")
}

func TestProgressMultiplexer_EmitQuestion(t *testing.T) {
	pm := NewProgressMultiplexer()

	var receivedEvent *ProgressEvent
	listener := ProgressCallback(func(event *ProgressEvent) {
		receivedEvent = event
	})

	pm.AddListener(listener)

	question := &Question{
		ID:      "q1",
		Prompt:  "What is your name?",
		Options: []string{"Alice", "Bob"},
		Context: "User identification",
		Timeout: 1 * time.Minute,
	}

	pm.EmitQuestion(question)

	time.Sleep(10 * time.Millisecond)

	require.NotNil(t, receivedEvent)
	assert.Equal(t, EventQuestionAsked, receivedEvent.Type)
	assert.Equal(t, "What is your name?", receivedEvent.Message)
	require.NotNil(t, receivedEvent.Question)
	assert.Equal(t, "q1", receivedEvent.Question.ID)
	assert.Equal(t, "What is your name?", receivedEvent.Question.Prompt)
	assert.Equal(t, []string{"Alice", "Bob"}, receivedEvent.Question.Options)
}

func TestProgressMultiplexer_EmitValidation(t *testing.T) {
	pm := NewProgressMultiplexer()

	var events []*ProgressEvent
	var mu sync.Mutex

	listener := ProgressCallback(func(event *ProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	})

	pm.AddListener(listener)

	// Test validation flow
	pm.EmitValidationStarted()
	pm.EmitValidationPassed(0.95)

	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, events, 2)

	// First event: validation started
	assert.Equal(t, EventValidationStarted, events[0].Type)
	assert.Equal(t, "Running multi-judge validation", events[0].Message)

	// Second event: validation passed
	assert.Equal(t, EventValidationPassed, events[1].Type)
	assert.Equal(t, "Validation passed", events[1].Message)
	require.NotNil(t, events[1].Details)
	assert.Equal(t, 0.95, events[1].Details["score"])
}

func TestProgressMultiplexer_EmitValidationFailed(t *testing.T) {
	pm := NewProgressMultiplexer()

	var receivedEvent *ProgressEvent
	listener := ProgressCallback(func(event *ProgressEvent) {
		receivedEvent = event
	})

	pm.AddListener(listener)
	pm.EmitValidationFailed(0.45, 3)

	time.Sleep(10 * time.Millisecond)

	require.NotNil(t, receivedEvent)
	assert.Equal(t, EventValidationFailed, receivedEvent.Type)
	assert.Equal(t, "Validation failed", receivedEvent.Message)
	require.NotNil(t, receivedEvent.Details)
	assert.Equal(t, 0.45, receivedEvent.Details["score"])
	assert.Equal(t, 3, receivedEvent.Details["issues"])
}

func TestWithProgress_And_FromContext(t *testing.T) {
	pm := NewProgressMultiplexer()
	ctx := context.Background()

	// Without progress
	assert.Nil(t, FromContext(ctx))

	// With progress
	ctx = WithProgress(ctx, pm)
	retrieved := FromContext(ctx)

	require.NotNil(t, retrieved)
	assert.Equal(t, pm, retrieved)
}

func TestEmitProgress_Helper(t *testing.T) {
	pm := NewProgressMultiplexer()
	ctx := WithProgress(context.Background(), pm)

	var receivedEvent *ProgressEvent
	listener := ProgressCallback(func(event *ProgressEvent) {
		receivedEvent = event
	})

	pm.AddListener(listener)

	// Use helper function
	EmitProgress(ctx, &ProgressEvent{
		Type:    EventAgentStarted,
		Message: "Agent started via helper",
	})

	time.Sleep(10 * time.Millisecond)

	require.NotNil(t, receivedEvent)
	assert.Equal(t, EventAgentStarted, receivedEvent.Type)
	assert.Equal(t, "Agent started via helper", receivedEvent.Message)
}

func TestEmitProgress_NoProgress(t *testing.T) {
	ctx := context.Background()

	// Should not panic when no progress in context
	EmitProgress(ctx, &ProgressEvent{
		Type:    EventAgentStarted,
		Message: "This should be ignored",
	})

	// Test passes if no panic
}

func TestQuestion_AnswerChannel(t *testing.T) {
	q := &Question{
		ID:         "test",
		Prompt:     "Test question?",
		AnswerChan: make(chan string, 1),
	}

	// Send answer
	go func() {
		time.Sleep(10 * time.Millisecond)
		q.AnswerChan <- "test answer"
	}()

	// Receive answer
	select {
	case answer := <-q.AnswerChan:
		assert.Equal(t, "test answer", answer)
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for answer")
	}
}

func TestQuestion_AnswerChannel_Timeout(t *testing.T) {
	q := &Question{
		ID:         "test",
		Prompt:     "Test question?",
		Timeout:    100 * time.Millisecond,
		AnswerChan: make(chan string, 1),
	}

	// Don't send answer, should timeout
	select {
	case <-q.AnswerChan:
		t.Fatal("Should not receive answer")
	case <-time.After(q.Timeout + 50*time.Millisecond):
		// Expected timeout
	}
}

// Test race conditions with concurrent listeners
func TestProgressMultiplexer_ConcurrentListeners(t *testing.T) {
	pm := NewProgressMultiplexer()

	var wg sync.WaitGroup
	numListeners := 10
	eventsPerListener := make([]int, numListeners)
	var eventMutexes []sync.Mutex
	for i := 0; i < numListeners; i++ {
		eventMutexes = append(eventMutexes, sync.Mutex{})
	}

	// Add multiple concurrent listeners
	for i := 0; i < numListeners; i++ {
		idx := i
		listener := ProgressCallback(func(event *ProgressEvent) {
			eventMutexes[idx].Lock()
			defer eventMutexes[idx].Unlock()
			eventsPerListener[idx]++
		})
		pm.AddListener(listener)
	}

	// Emit events concurrently
	numEvents := 100
	wg.Add(numEvents)
	for i := 0; i < numEvents; i++ {
		go func() {
			defer wg.Done()
			pm.Emit(&ProgressEvent{
				Type:    EventAgentStarted,
				Message: "Concurrent event",
			})
		}()
	}

	wg.Wait()
	time.Sleep(50 * time.Millisecond) // Let all events propagate

	// Each listener should receive all events
	for i := range eventsPerListener {
		eventMutexes[i].Lock()
		count := eventsPerListener[i]
		eventMutexes[i].Unlock()
		assert.Equal(t, numEvents, count, "Listener %d did not receive all events", i)
	}
}
