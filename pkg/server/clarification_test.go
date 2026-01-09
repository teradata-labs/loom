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
package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/metaagent"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestAnswerClarificationQuestion_Success tests successful answer delivery
func TestAnswerClarificationQuestion_Success(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions:                  make(map[string]*metaagent.Question),
		logger:                            zap.NewNop(),
		clarificationChannelSendTimeoutMs: 100,
	}

	// Create a question with answer channel
	answerChan := make(chan string, 1)
	question := &metaagent.Question{
		ID:         "test-question-1",
		Prompt:     "Test question?",
		AnswerChan: answerChan,
	}

	// Register the question
	server.pendingQuestions[question.ID] = question

	// Send answer via RPC
	req := &loomv1.AnswerClarificationRequest{
		SessionId:  "session-123",
		QuestionId: "test-question-1",
		Answer:     "Yes",
		AgentId:    "agent-1",
	}

	resp, err := server.AnswerClarificationQuestion(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.True(t, resp.Accepted)
	assert.Empty(t, resp.Error)

	// Verify answer was delivered to channel
	select {
	case answer := <-answerChan:
		assert.Equal(t, "Yes", answer)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Answer not received on channel")
	}

	// Verify question was removed from pending map
	server.pendingQuestionsMu.RLock()
	_, exists := server.pendingQuestions[question.ID]
	server.pendingQuestionsMu.RUnlock()
	assert.False(t, exists, "Question should be removed from pending map")
}

// TestAnswerClarificationQuestion_QuestionNotFound tests handling of non-existent question
func TestAnswerClarificationQuestion_QuestionNotFound(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions: make(map[string]*metaagent.Question),
		logger:           zap.NewNop(),
	}

	req := &loomv1.AnswerClarificationRequest{
		SessionId:  "session-123",
		QuestionId: "non-existent-question",
		Answer:     "Yes",
		AgentId:    "agent-1",
	}

	resp, err := server.AnswerClarificationQuestion(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.False(t, resp.Accepted)
	assert.Contains(t, resp.Error, "question not found or already answered")
}

// TestAnswerClarificationQuestion_EmptyQuestionID tests validation of empty question ID
func TestAnswerClarificationQuestion_EmptyQuestionID(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions: make(map[string]*metaagent.Question),
		logger:           zap.NewNop(),
	}

	req := &loomv1.AnswerClarificationRequest{
		SessionId:  "session-123",
		QuestionId: "",
		Answer:     "Yes",
		AgentId:    "agent-1",
	}

	resp, err := server.AnswerClarificationQuestion(context.Background(), req)
	assert.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "question_id is required")
}

// TestAnswerClarificationQuestion_EmptyAnswer tests handling of empty answer
func TestAnswerClarificationQuestion_EmptyAnswer(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions: make(map[string]*metaagent.Question),
		logger:           zap.NewNop(),
	}

	// Create a question
	answerChan := make(chan string, 1)
	question := &metaagent.Question{
		ID:         "test-question-2",
		Prompt:     "Test question?",
		AnswerChan: answerChan,
	}
	server.pendingQuestions[question.ID] = question

	req := &loomv1.AnswerClarificationRequest{
		SessionId:  "session-123",
		QuestionId: "test-question-2",
		Answer:     "",
		AgentId:    "agent-1",
	}

	resp, err := server.AnswerClarificationQuestion(context.Background(), req)
	assert.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "answer cannot be empty")

	// Verify question still in pending map (not removed on validation failure)
	server.pendingQuestionsMu.RLock()
	_, exists := server.pendingQuestions[question.ID]
	server.pendingQuestionsMu.RUnlock()
	assert.True(t, exists, "Question should remain in pending map after validation failure")
}

// TestAnswerClarificationQuestion_ChannelClosed tests handling of closed answer channel
func TestAnswerClarificationQuestion_ChannelClosed(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions: make(map[string]*metaagent.Question),
		logger:           zap.NewNop(),
	}

	// Create a question with closed answer channel
	answerChan := make(chan string, 1)
	close(answerChan) // Close immediately

	question := &metaagent.Question{
		ID:         "test-question-3",
		Prompt:     "Test question?",
		AnswerChan: answerChan,
	}
	server.pendingQuestions[question.ID] = question

	req := &loomv1.AnswerClarificationRequest{
		SessionId:  "session-123",
		QuestionId: "test-question-3",
		Answer:     "Yes",
		AgentId:    "agent-1",
	}

	resp, err := server.AnswerClarificationQuestion(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.False(t, resp.Accepted)
	assert.Contains(t, resp.Error, "answer channel closed or timeout")

	// Verify question was removed from pending map
	server.pendingQuestionsMu.RLock()
	_, exists := server.pendingQuestions[question.ID]
	server.pendingQuestionsMu.RUnlock()
	assert.False(t, exists, "Question should be removed even on channel error")
}

// TestAnswerClarificationQuestion_ContextCancelled tests handling of cancelled context
func TestAnswerClarificationQuestion_ContextCancelled(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions:                  make(map[string]*metaagent.Question),
		logger:                            zap.NewNop(),
		clarificationChannelSendTimeoutMs: 100,
	}

	// Create a question with blocked channel (no buffer, no receiver)
	answerChan := make(chan string)
	question := &metaagent.Question{
		ID:         "test-question-4",
		Prompt:     "Test question?",
		AnswerChan: answerChan,
	}
	server.pendingQuestions[question.ID] = question

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	req := &loomv1.AnswerClarificationRequest{
		SessionId:  "session-123",
		QuestionId: "test-question-4",
		Answer:     "Yes",
		AgentId:    "agent-1",
	}

	resp, err := server.AnswerClarificationQuestion(ctx, req)
	assert.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Canceled, st.Code())
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "context cancelled")

	// Verify question was removed from pending map
	server.pendingQuestionsMu.RLock()
	_, exists := server.pendingQuestions[question.ID]
	server.pendingQuestionsMu.RUnlock()
	assert.False(t, exists, "Question should be removed even on context cancellation")
}

// TestAnswerClarificationQuestion_ConcurrentAnswers tests race conditions with -race detector
func TestAnswerClarificationQuestion_ConcurrentAnswers(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions:                  make(map[string]*metaagent.Question),
		logger:                            zap.NewNop(),
		clarificationChannelSendTimeoutMs: 100, // Set timeout to avoid immediate failure
	}

	// Create multiple questions
	numQuestions := 10
	answerChans := make([]chan string, numQuestions)
	for i := 0; i < numQuestions; i++ {
		answerChan := make(chan string, 1)
		answerChans[i] = answerChan
		question := &metaagent.Question{
			ID:         string(rune('A' + i)),
			Prompt:     "Test question?",
			AnswerChan: answerChan,
		}
		server.pendingQuestions[question.ID] = question
	}

	// Send answers concurrently
	done := make(chan bool, numQuestions)
	for i := 0; i < numQuestions; i++ {
		go func(idx int) {
			defer func() { done <- true }()

			req := &loomv1.AnswerClarificationRequest{
				SessionId:  "session-123",
				QuestionId: string(rune('A' + idx)),
				Answer:     "Concurrent answer",
				AgentId:    "agent-1",
			}

			resp, err := server.AnswerClarificationQuestion(context.Background(), req)
			assert.NoError(t, err)
			assert.True(t, resp.Success)

			// Verify answer delivered
			select {
			case answer := <-answerChans[idx]:
				assert.Equal(t, "Concurrent answer", answer)
			case <-time.After(200 * time.Millisecond):
				t.Errorf("Answer not received for question %d", idx)
			}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numQuestions; i++ {
		<-done
	}

	// Verify all questions removed
	server.pendingQuestionsMu.RLock()
	assert.Empty(t, server.pendingQuestions, "All questions should be removed")
	server.pendingQuestionsMu.RUnlock()
}

// TestAnswerClarificationQuestion_NilAnswerChan tests handling of nil answer channel
func TestAnswerClarificationQuestion_NilAnswerChan(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions: make(map[string]*metaagent.Question),
		logger:           zap.NewNop(),
	}

	// Create a question with nil answer channel
	question := &metaagent.Question{
		ID:         "test-question-5",
		Prompt:     "Test question?",
		AnswerChan: nil, // Nil channel
	}
	server.pendingQuestions[question.ID] = question

	req := &loomv1.AnswerClarificationRequest{
		SessionId:  "session-123",
		QuestionId: "test-question-5",
		Answer:     "Yes",
		AgentId:    "agent-1",
	}

	resp, err := server.AnswerClarificationQuestion(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.False(t, resp.Accepted)
	assert.Contains(t, resp.Error, "question has no answer channel")

	// Verify question was removed from pending map (removed before channel check)
	server.pendingQuestionsMu.RLock()
	_, exists := server.pendingQuestions[question.ID]
	server.pendingQuestionsMu.RUnlock()
	assert.False(t, exists, "Question should be removed")
}

// TestAnswerClarificationQuestion_ChannelTimeout tests timeout on blocked channel
func TestAnswerClarificationQuestion_ChannelTimeout(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions:                  make(map[string]*metaagent.Question),
		logger:                            zap.NewNop(),
		clarificationChannelSendTimeoutMs: 100,
	}

	// Create a question with unbuffered channel (will block)
	answerChan := make(chan string)
	question := &metaagent.Question{
		ID:         "test-question-6",
		Prompt:     "Test question?",
		AnswerChan: answerChan,
	}
	server.pendingQuestions[question.ID] = question

	req := &loomv1.AnswerClarificationRequest{
		SessionId:  "session-123",
		QuestionId: "test-question-6",
		Answer:     "Yes",
		AgentId:    "agent-1",
	}

	// Should timeout after 100ms
	resp, err := server.AnswerClarificationQuestion(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.False(t, resp.Accepted)
	assert.Contains(t, resp.Error, "answer channel closed or timeout")

	// Verify question was removed from pending map
	server.pendingQuestionsMu.RLock()
	_, exists := server.pendingQuestions[question.ID]
	server.pendingQuestionsMu.RUnlock()
	assert.False(t, exists, "Question should be removed after timeout")
}

// TestServerProgressListener_Lifecycle tests question registration and cleanup
func TestServerProgressListener_Lifecycle(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions: make(map[string]*metaagent.Question),
		logger:           zap.NewNop(),
	}

	listener := &serverProgressListener{
		server: server,
		logger: zap.NewNop(),
	}

	// Create a question
	answerChan := make(chan string, 1)
	question := &metaagent.Question{
		ID:         "lifecycle-test-1",
		Prompt:     "Test question?",
		AnswerChan: answerChan,
		Timeout:    5 * time.Minute,
	}

	// Test EventQuestionAsked - should register question
	questionAskedEvent := &metaagent.ProgressEvent{
		Type:     metaagent.EventQuestionAsked,
		Question: question,
	}
	listener.OnProgress(questionAskedEvent)

	server.pendingQuestionsMu.RLock()
	registered, exists := server.pendingQuestions[question.ID]
	server.pendingQuestionsMu.RUnlock()
	assert.True(t, exists, "Question should be registered")
	assert.Equal(t, question.ID, registered.ID)

	// Test EventQuestionAnswered - should cleanup question
	questionAnsweredEvent := &metaagent.ProgressEvent{
		Type: metaagent.EventQuestionAnswered,
		Details: map[string]interface{}{
			"question_id": question.ID,
		},
	}
	listener.OnProgress(questionAnsweredEvent)

	server.pendingQuestionsMu.RLock()
	_, exists = server.pendingQuestions[question.ID]
	server.pendingQuestionsMu.RUnlock()
	assert.False(t, exists, "Question should be cleaned up after answer")
}

// TestServerProgressListener_EventQuestionAnswered_MissingQuestionID tests cleanup with missing question_id
func TestServerProgressListener_EventQuestionAnswered_MissingQuestionID(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions: make(map[string]*metaagent.Question),
		logger:           zap.NewNop(),
	}

	listener := &serverProgressListener{
		server: server,
		logger: zap.NewNop(),
	}

	// Add a question
	server.pendingQuestions["test-q"] = &metaagent.Question{ID: "test-q"}

	// Send EventQuestionAnswered without question_id
	event := &metaagent.ProgressEvent{
		Type:    metaagent.EventQuestionAnswered,
		Details: map[string]interface{}{},
	}
	listener.OnProgress(event)

	// Question should still exist (not cleaned up)
	server.pendingQuestionsMu.RLock()
	_, exists := server.pendingQuestions["test-q"]
	server.pendingQuestionsMu.RUnlock()
	assert.True(t, exists, "Question should not be removed without question_id")
}

// TestServerProgressListener_EventQuestionAnswered_EmptyQuestionID tests cleanup with empty question_id
func TestServerProgressListener_EventQuestionAnswered_EmptyQuestionID(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions: make(map[string]*metaagent.Question),
		logger:           zap.NewNop(),
	}

	listener := &serverProgressListener{
		server: server,
		logger: zap.NewNop(),
	}

	// Add a question
	server.pendingQuestions["test-q"] = &metaagent.Question{ID: "test-q"}

	// Send EventQuestionAnswered with empty question_id
	event := &metaagent.ProgressEvent{
		Type: metaagent.EventQuestionAnswered,
		Details: map[string]interface{}{
			"question_id": "",
		},
	}
	listener.OnProgress(event)

	// Question should still exist (not cleaned up with empty ID)
	server.pendingQuestionsMu.RLock()
	_, exists := server.pendingQuestions["test-q"]
	server.pendingQuestionsMu.RUnlock()
	assert.True(t, exists, "Question should not be removed with empty question_id")
}

// TestShutdown_GracefulCleanup tests graceful shutdown closes all pending questions
func TestShutdown_GracefulCleanup(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions: make(map[string]*metaagent.Question),
		logger:           zap.NewNop(),
	}

	// Create multiple pending questions with answer channels
	numQuestions := 5
	answerChans := make([]chan string, numQuestions)
	for i := 0; i < numQuestions; i++ {
		answerChan := make(chan string, 1)
		answerChans[i] = answerChan
		question := &metaagent.Question{
			ID:         fmt.Sprintf("question-%d", i),
			Prompt:     "Test question?",
			AnswerChan: answerChan,
		}
		server.pendingQuestions[question.ID] = question
	}

	// Verify questions are pending
	server.pendingQuestionsMu.RLock()
	assert.Equal(t, numQuestions, len(server.pendingQuestions))
	server.pendingQuestionsMu.RUnlock()

	// Call Shutdown
	err := server.Shutdown(context.Background())
	require.NoError(t, err)

	// Verify all questions removed
	server.pendingQuestionsMu.RLock()
	assert.Empty(t, server.pendingQuestions, "All questions should be removed after shutdown")
	server.pendingQuestionsMu.RUnlock()

	// Verify all channels were closed (attempts to send will panic, so we check with select)
	for i, ch := range answerChans {
		select {
		case _, ok := <-ch:
			assert.False(t, ok, "Channel %d should be closed", i)
		default:
			t.Errorf("Channel %d was not closed properly", i)
		}
	}
}

// TestShutdown_NilChannels tests shutdown with nil answer channels
func TestShutdown_NilChannels(t *testing.T) {
	server := &MultiAgentServer{
		pendingQuestions: make(map[string]*metaagent.Question),
		logger:           zap.NewNop(),
	}

	// Add questions with nil channels
	server.pendingQuestions["q1"] = &metaagent.Question{
		ID:         "q1",
		AnswerChan: nil,
	}
	server.pendingQuestions["q2"] = &metaagent.Question{
		ID:         "q2",
		AnswerChan: nil,
	}

	// Shutdown should not panic with nil channels
	err := server.Shutdown(context.Background())
	require.NoError(t, err)

	// All questions should be removed
	server.pendingQuestionsMu.RLock()
	assert.Empty(t, server.pendingQuestions)
	server.pendingQuestionsMu.RUnlock()
}
