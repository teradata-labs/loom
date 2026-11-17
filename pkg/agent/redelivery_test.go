// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// healableLLM fails every call until healed, and records the last message of
// each request. Failing until healed keeps the agent's own retries from
// masking the failure.
type healableLLM struct {
	mu     sync.Mutex
	healed bool
	last   []Message
}

func (m *healableLLM) Name() string  { return "healable" }
func (m *healableLLM) Model() string { return "test" }
func (m *healableLLM) Chat(_ context.Context, msgs []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(msgs) > 0 {
		m.last = append(m.last, msgs[len(msgs)-1])
	}
	if !m.healed {
		return nil, errors.New("LLM unavailable")
	}
	return &LLMResponse{Content: "done"}, nil
}

func (m *healableLLM) heal() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healed = true
}

func (m *healableLLM) lastRequestTail() Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last[len(m.last)-1]
}

func userRows(t *testing.T, a *Agent, sessionID string) []Message {
	t.Helper()
	session, ok := a.GetSession(sessionID)
	require.True(t, ok)
	var rows []Message
	for _, m := range rawSessionMessages(session) {
		if m.Role == "user" {
			rows = append(rows, m)
		}
	}
	return rows
}

// failThenRetry runs one Chat that fails, heals the LLM, and runs a second
// Chat with the same text. ctx1 and ctx2 carry each attempt's options.
func failThenRetry(t *testing.T, ctx1, ctx2 context.Context) (*Agent, *healableLLM) {
	t.Helper()
	llm := &healableLLM{}
	a := NewAgent(&mockBackend{}, llm)
	_, err := a.Chat(ctx1, "s1", "do the thing")
	require.Error(t, err)
	require.Len(t, userRows(t, a, "s1"), 1, "the failed turn stores its user row")
	llm.heal()
	_, err = a.Chat(ctx2, "s1", "do the thing")
	require.NoError(t, err)
	return a, llm
}

func TestChat_RedeliveryKeyReusesFailedTurnUserRow(t *testing.T) {
	ctx := WithRedeliveryKey(context.Background(), "queue:m1")
	a, _ := failThenRetry(t, ctx, ctx)
	assert.Len(t, userRows(t, a, "s1"), 1, "a redelivery with the same key must not store the prompt twice")
}

// Without a key Chat keeps its documented behaviour: every call appends.
func TestChat_WithoutRedeliveryKeyAlwaysAppends(t *testing.T) {
	a, _ := failThenRetry(t, context.Background(), context.Background())
	assert.Len(t, userRows(t, a, "s1"), 2, "plain Chat must append even when the text repeats")
}

func TestChat_DifferentRedeliveryKeyAppends(t *testing.T) {
	a, _ := failThenRetry(t,
		WithRedeliveryKey(context.Background(), "queue:m1"),
		WithRedeliveryKey(context.Background(), "queue:m2"))
	assert.Len(t, userRows(t, a, "s1"), 2, "a different delivery is a new message")
}

// A deliberate repeat after a successful turn appends even with the same key:
// the successful turn ended with an assistant row.
func TestChat_RedeliveryKeyAfterSuccessAppends(t *testing.T) {
	llm := &healableLLM{healed: true}
	a := NewAgent(&mockBackend{}, llm)
	ctx := WithRedeliveryKey(context.Background(), "queue:m1")
	_, err := a.Chat(ctx, "s1", "do the thing")
	require.NoError(t, err)
	_, err = a.Chat(ctx, "s1", "do the thing")
	require.NoError(t, err)
	assert.Len(t, userRows(t, a, "s1"), 2)
}

// The user resends the same text with a corrected image after a failed turn.
// The new image must reach the LLM and be stored (#533 review F5).
func TestChatWithContentBlocks_RetryWithSameTextKeepsNewBlocks(t *testing.T) {
	llm := &healableLLM{}
	a := NewAgent(&mockBackend{}, llm)
	imageA := []ContentBlock{{Type: "image", Image: &types.ImageContent{Type: "image", Source: types.ImageSource{Type: "base64", MediaType: "image/png", Data: "QQ=="}}}}
	imageB := []ContentBlock{{Type: "image", Image: &types.ImageContent{Type: "image", Source: types.ImageSource{Type: "base64", MediaType: "image/png", Data: "Qg=="}}}}

	_, err := a.ChatWithContentBlocks(context.Background(), "s1", "what is this?", imageA, nil)
	require.Error(t, err)
	llm.heal()
	_, err = a.ChatWithContentBlocks(context.Background(), "s1", "what is this?", imageB, nil)
	require.NoError(t, err)

	rows := userRows(t, a, "s1")
	require.Len(t, rows, 2)
	assert.Equal(t, "Qg==", imageData(rows[1].ContentBlocks), "the retry's row must carry its own image")
	assert.Equal(t, "Qg==", imageData(llm.lastRequestTail().ContentBlocks), "the retry's image must reach the LLM")
}

func imageData(blocks []ContentBlock) string {
	for _, b := range blocks {
		if b.Type == "image" && b.Image != nil {
			return b.Image.Source.Data
		}
	}
	return ""
}
