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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// capturingLLM records the messages of the most recent Chat call so tests can
// assert on what the agent actually forwarded to the provider. It returns a
// fixed text response and no tool calls.
type capturingLLM struct {
	mu       sync.Mutex
	response string
	captured []llmtypes.Message
	calls    int
}

func (m *capturingLLM) Chat(_ context.Context, messages []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	// Copy the slice so later mutation by the agent can't race with assertions.
	m.captured = append([]llmtypes.Message(nil), messages...)
	content := m.response
	if content == "" {
		content = "ok"
	}
	return &llmtypes.LLMResponse{
		Content: content,
		Usage:   llmtypes.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.001},
	}, nil
}

func (m *capturingLLM) Name() string  { return "mock-capturing" }
func (m *capturingLLM) Model() string { return "mock-v1" }

func (m *capturingLLM) lastMessages() []llmtypes.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.captured
}

// contentBlocksTestAgent builds an Agent wired to the given LLM with the LLM
// classifier disabled for deterministic behavior.
func contentBlocksTestAgent(llm interface {
	Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error)
	Name() string
	Model() string
}) *Agent {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	return NewAgent(&mockBackend{}, llm, WithConfig(cfg))
}

// sampleBlocks returns a text + image multimodal turn.
func sampleBlocks() []ContentBlock {
	return []ContentBlock{
		{Type: "text", Text: "What is in this image?"},
		{
			Type: "image",
			Image: &types.ImageContent{
				Type: "image",
				Source: types.ImageSource{
					Type:      "base64",
					MediaType: "image/png",
					Data:      "aGVsbG8=", // "hello"
				},
			},
		},
	}
}

// findUserMessage returns the first user-role message whose content ends with
// the given text — the turn carries its arrival timestamp as a prefix, so the
// caller's words are the suffix of the stored content.
func findUserMessage(messages []llmtypes.Message, content string) (llmtypes.Message, bool) {
	for _, msg := range messages {
		if msg.Role == "user" && strings.HasSuffix(msg.Content, content) {
			return msg, true
		}
	}
	return llmtypes.Message{}, false
}

// TestAgent_ChatWithContentBlocks_PropagatesBlocksToLLM is the core guarantee of
// the feature: content blocks attached to the user turn must reach the provider
// request, not just the plain-text content.
func TestAgent_ChatWithContentBlocks_PropagatesBlocksToLLM(t *testing.T) {
	llm := &capturingLLM{response: "I see a picture."}
	ag := contentBlocksTestAgent(llm)

	blocks := sampleBlocks()
	resp, err := ag.ChatWithContentBlocks(context.Background(), "blocks_session", "What is in this image?", blocks, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "I see a picture.", resp.Content)

	userMsg, ok := findUserMessage(llm.lastMessages(), "What is in this image?")
	require.True(t, ok, "user message should be forwarded to the LLM")
	require.Len(t, userMsg.ContentBlocks, 2, "both content blocks must reach the provider")

	assert.Equal(t, "text", userMsg.ContentBlocks[0].Type)
	assert.Equal(t, "What is in this image?", userMsg.ContentBlocks[0].Text)

	imgBlock := userMsg.ContentBlocks[1]
	assert.Equal(t, "image", imgBlock.Type)
	require.NotNil(t, imgBlock.Image)
	assert.Equal(t, "image/png", imgBlock.Image.Source.MediaType)
	assert.Equal(t, "aGVsbG8=", imgBlock.Image.Source.Data)
}

// TestAgent_ChatWithContentBlocks_PersistsCanonicalText verifies userMessage
// remains the canonical text on the stored turn while ContentBlocks ride along,
// and that the assistant response is appended to history.
func TestAgent_ChatWithContentBlocks_PersistsCanonicalText(t *testing.T) {
	llm := &capturingLLM{response: "done"}
	ag := contentBlocksTestAgent(llm)

	const sessionID = "persist_session"
	_, err := ag.ChatWithContentBlocks(context.Background(), sessionID, "canonical text", sampleBlocks(), nil)
	require.NoError(t, err)

	session, ok := ag.GetSession(sessionID)
	require.True(t, ok, "session should exist after the call")

	messages := session.GetMessages()
	require.GreaterOrEqual(t, len(messages), 2, "expected at least the user turn and assistant reply")

	var userMsg *types.Message
	var sawAssistant bool
	for i := range messages {
		switch messages[i].Role {
		case "user":
			if strings.HasSuffix(messages[i].Content, "canonical text") {
				userMsg = &messages[i]
			}
		case "assistant":
			sawAssistant = true
		}
	}

	require.NotNil(t, userMsg, "stored user turn should keep the canonical text")
	assert.True(t, strings.HasPrefix(userMsg.Content, "["),
		"the turn carries its arrival timestamp as a prefix")
	assert.True(t, strings.HasSuffix(userMsg.Content, "] canonical text"),
		"the caller's words follow the stamp unchanged")
	assert.Len(t, userMsg.ContentBlocks, 2, "content blocks should be stored on the user turn")
	assert.True(t, sawAssistant, "assistant response should be appended to history")
}

// TestAgent_ChatWithContentBlocks_NilProgressCallback confirms a nil callback is
// accepted (the blocks-capable equivalent of plain Chat) and does not panic.
func TestAgent_ChatWithContentBlocks_NilProgressCallback(t *testing.T) {
	llm := &capturingLLM{response: "no callback"}
	ag := contentBlocksTestAgent(llm)

	resp, err := ag.ChatWithContentBlocks(context.Background(), "nil_cb_session", "hello", sampleBlocks(), nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "no callback", resp.Content)
}

// TestAgent_ChatWithContentBlocks_EmptyBlocks confirms nil/empty content blocks
// degrade to an ordinary text turn.
func TestAgent_ChatWithContentBlocks_EmptyBlocks(t *testing.T) {
	llm := &capturingLLM{response: "plain"}
	ag := contentBlocksTestAgent(llm)

	resp, err := ag.ChatWithContentBlocks(context.Background(), "empty_blocks_session", "just text", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "plain", resp.Content)

	userMsg, ok := findUserMessage(llm.lastMessages(), "just text")
	require.True(t, ok)
	assert.Empty(t, userMsg.ContentBlocks, "no blocks supplied means none forwarded")
}

// TestAgent_ChatWithContentBlocks_ImageOnlyPrependsText guards against the
// image-only footgun: providers build the request exclusively from
// ContentBlocks when present, dropping Content — so an image-only block set
// would silently send the image without the question. The agent must prepend
// userMessage as a text block so the model still receives the prompt.
func TestAgent_ChatWithContentBlocks_ImageOnlyPrependsText(t *testing.T) {
	llm := &capturingLLM{response: "a red square on blue"}
	ag := contentBlocksTestAgent(llm)

	imageOnly := []ContentBlock{sampleBlocks()[1]} // just the image block

	resp, err := ag.ChatWithContentBlocks(context.Background(), "img_only_session", "What is in this image?", imageOnly, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)

	userMsg, ok := findUserMessage(llm.lastMessages(), "What is in this image?")
	require.True(t, ok)
	require.Len(t, userMsg.ContentBlocks, 2, "text block must be prepended to the image-only set")
	assert.Equal(t, "text", userMsg.ContentBlocks[0].Type)
	assert.Equal(t, "What is in this image?", userMsg.ContentBlocks[0].Text, "prepended text must be the canonical userMessage")
	assert.Equal(t, "image", userMsg.ContentBlocks[1].Type)
}

// TestAgent_ChatWithContentBlocks_ExistingTextBlockNotDuplicated verifies the
// prepend guard is a no-op when the caller already includes a text block: the
// block set reaches the provider unchanged, with no duplicate prompt text.
func TestAgent_ChatWithContentBlocks_ExistingTextBlockNotDuplicated(t *testing.T) {
	llm := &capturingLLM{response: "ok"}
	ag := contentBlocksTestAgent(llm)

	blocks := sampleBlocks() // already text + image

	resp, err := ag.ChatWithContentBlocks(context.Background(), "has_text_session", "What is in this image?", blocks, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)

	userMsg, ok := findUserMessage(llm.lastMessages(), "What is in this image?")
	require.True(t, ok)
	require.Len(t, userMsg.ContentBlocks, 2, "caller-supplied blocks must pass through unchanged")
	assert.Equal(t, "text", userMsg.ContentBlocks[0].Type)
	assert.Equal(t, "image", userMsg.ContentBlocks[1].Type)
}

// TestAgent_ChatWithContentBlocks_SuccessWithProgressCallback exercises the
// success path with a non-nil progress callback: the call succeeds, returns a
// response, and — unlike the error path — never emits a StageFailed event.
func TestAgent_ChatWithContentBlocks_SuccessWithProgressCallback(t *testing.T) {
	llm := &capturingLLM{response: "described"}
	ag := contentBlocksTestAgent(llm)

	var mu sync.Mutex
	var stages []ExecutionStage
	cb := func(ev ProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		stages = append(stages, ev.Stage)
	}

	resp, err := ag.ChatWithContentBlocks(context.Background(), "ok_cb_session", "describe this", sampleBlocks(), cb)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "described", resp.Content)

	mu.Lock()
	defer mu.Unlock()
	assert.NotContains(t, stages, StageFailed, "successful turn must not emit a failure event")
}

// TestAgent_ChatWithContentBlocks_ErrorEmitsFailureEvent covers the error path:
// a failing LLM propagates the error, returns a nil response, and — when a
// progress callback is supplied — emits a StageFailed event.
func TestAgent_ChatWithContentBlocks_ErrorEmitsFailureEvent(t *testing.T) {
	ag := contentBlocksTestAgent(&mockErrorLLM{errorMsg: "boom"})

	var mu sync.Mutex
	var stages []ExecutionStage
	cb := func(ev ProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		stages = append(stages, ev.Stage)
	}

	resp, err := ag.ChatWithContentBlocks(context.Background(), "err_session", "boom please", sampleBlocks(), cb)
	require.Error(t, err)
	assert.Nil(t, resp)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, stages, StageFailed, "failure should emit a StageFailed progress event")
}

// TestAgent_ChatWithContentBlocks_ErrorNilCallback ensures the error path does
// not panic when no progress callback is provided.
func TestAgent_ChatWithContentBlocks_ErrorNilCallback(t *testing.T) {
	ag := contentBlocksTestAgent(&mockErrorLLM{errorMsg: "boom"})

	resp, err := ag.ChatWithContentBlocks(context.Background(), "err_nil_cb_session", "boom please", sampleBlocks(), nil)
	require.Error(t, err)
	assert.Nil(t, resp)
}

// TestContentBlockAlias documents that pkg/agent re-exports the ContentBlock
// type alias so hosts can build multimodal turns without importing pkg/types.
func TestContentBlockAlias(t *testing.T) {
	var block ContentBlock = types.ContentBlock{Type: "text", Text: "hi"}
	var back types.ContentBlock = block
	assert.Equal(t, "hi", back.Text)
}

// TestChat_UserTurnCarriesArrivalTimestamp pins the write-gate rule: time
// enters the session written into the user turn at arrival — "[Mon 2006-01-02
// 15:04 MST] " before the user's words — and nothing renders time dynamically,
// so the stored turn is byte-stable from the moment it is written.
func TestChat_UserTurnCarriesArrivalTimestamp(t *testing.T) {
	llm := &capturingLLM{response: "ok"}
	ag := contentBlocksTestAgent(llm)

	_, err := ag.Chat(context.Background(), "stamp_session", "what ran today?")
	require.NoError(t, err)

	session, ok := ag.GetSession("stamp_session")
	require.True(t, ok)
	var content string
	for _, m := range session.GetMessages() {
		if m.Role == "user" {
			content = m.Content
		}
	}
	require.NotEmpty(t, content)
	require.True(t, strings.HasSuffix(content, "] what ran today?"), "user words follow the stamp: %q", content)
	stamp := strings.TrimPrefix(content[:strings.Index(content, "] ")+1], "[")
	stamp = strings.Trim(stamp, "[]")
	_, err = time.Parse("Mon 2006-01-02 15:04 MST", stamp)
	require.NoError(t, err, "stamp %q parses as the arrival-time format", stamp)
}
