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

//go:build fts5

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Graph memory ordering within a turn: a turn's own message must be extracted
// only AFTER recall, so it is never injected back as "past" memory, while
// earlier turns' facts stay recallable.

// turnOrderLLM routes by prompt: extraction, recall query, re-rank, or the
// main chat call (whose requests are recorded).
type turnOrderLLM struct {
	mu           sync.Mutex
	extractJSON  string
	extractGate  chan struct{} // when set, extraction blocks until it is closed
	extractCalls int
	mainScript   []mockLLMResponse
	mainIdx      int
	mainRequests [][]llmtypes.Message
}

func (l *turnOrderLLM) Name() string  { return "turn-order-stub" }
func (l *turnOrderLLM) Model() string { return "stub" }

func promptContains(msgs []llmtypes.Message, s string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, s) {
			return true
		}
	}
	return false
}

func (l *turnOrderLLM) Chat(ctx context.Context, msgs []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	switch {
	case promptContains(msgs, "Extract entities, relationships, and memories"):
		l.mu.Lock()
		l.extractCalls++
		gate := l.extractGate
		l.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-time.After(5 * time.Second):
				return nil, errors.New("extraction gate timeout")
			}
			// A cancelled caller context aborts a real provider call.
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		return &llmtypes.LLMResponse{Content: l.extractJSON}, nil
	case promptContains(msgs, "Distill this message into a concise search query"):
		return &llmtypes.LLMResponse{Content: "dog Max"}, nil
	case promptContains(msgs, "Candidate memories:"):
		return &llmtypes.LLMResponse{Content: "1,2,3,4,5"}, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.mainRequests = append(l.mainRequests, append([]llmtypes.Message(nil), msgs...))
	usage := llmtypes.Usage{InputTokens: 10, OutputTokens: 5}
	if l.mainIdx < len(l.mainScript) {
		r := l.mainScript[l.mainIdx]
		l.mainIdx++
		return &llmtypes.LLMResponse{Content: r.content, ToolCalls: r.toolCalls, Usage: usage}, nil
	}
	return &llmtypes.LLMResponse{Content: "ok", Usage: usage}, nil
}

func (l *turnOrderLLM) extractions() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.extractCalls
}

// memoryBlockIn returns the injected graph-memory block of a main chat request, or "".
func memoryBlockIn(req []llmtypes.Message) string {
	for _, m := range req {
		if strings.HasPrefix(m.Content, graphMemoryContextMarker) {
			return m.Content
		}
	}
	return ""
}

func (l *turnOrderLLM) mainRequest(t *testing.T, i int) []llmtypes.Message {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	require.Greater(t, len(l.mainRequests), i, "main chat call %d never happened", i)
	return l.mainRequests[i]
}

func newTurnOrderLLM(t *testing.T) *turnOrderLLM {
	t.Helper()
	data, err := json.Marshal(ExtractedGraphData{
		Entities: []ExtractedEntity{{Name: "max", EntityType: "concept"}},
		Memories: []ExtractedMemory{{
			Content:    "User adopted a dog named Max",
			Summary:    "Adopted dog Max",
			MemoryType: "fact",
			Salience:   0.8,
			Entities:   []ExtractedEntityRole{{Name: "max", Role: "about"}},
		}},
	})
	require.NoError(t, err)
	return &turnOrderLLM{extractJSON: string(data)}
}

func newTurnOrderAgent(t *testing.T, llm *turnOrderLLM, extra ...Option) (*Agent, memory.GraphMemoryStore) {
	t.Helper()
	store := newTestGraphMemoryStore(t)
	cfg := DefaultConfig()
	cfg.Name = "turn-order-agent"
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	gm := DefaultGraphMemoryConfig()
	gm.Enabled = true
	gm.EnableExtraction = true

	opts := append([]Option{WithConfig(cfg), WithGraphMemoryStore(store, gm)}, extra...)
	return NewAgent(&mockBackend{}, llm, opts...), store
}

// A turn's own message must not be injected back as "past conversations":
// extraction used to finish before recall, so recall always found it.
func TestGraphMemory_TurnDoesNotRecallItsOwnMessage(t *testing.T) {
	llm := newTurnOrderLLM(t)
	ag, store := newTurnOrderAgent(t, llm)

	_, err := ag.Chat(context.Background(), "s-first", "I adopted a dog named Max")
	require.NoError(t, err)

	assert.Empty(t, memoryBlockIn(llm.mainRequest(t, 0)),
		"first message must not receive its own extracted facts as past memory")

	// The fact is still extracted for later turns.
	ag.FlushGraphMemoryExtraction()
	got, err := store.Recall(context.Background(), memory.RecallOpts{AgentID: "turn-order-agent", Query: "Max", Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, got, "the turn's facts must still be extracted")
	assert.Contains(t, got[0].Content, "Max")
}

// Moving extraction after recall must not hide earlier turns' facts.
func TestGraphMemory_EarlierTurnRecalledOnNextTurn(t *testing.T) {
	llm := newTurnOrderLLM(t)
	ag, _ := newTurnOrderAgent(t, llm)
	ctx := context.Background()

	_, err := ag.Chat(ctx, "s-recall", "I adopted a dog named Max")
	require.NoError(t, err)
	_, err = ag.Chat(ctx, "s-recall", "What is my dog's name?")
	require.NoError(t, err)

	block := memoryBlockIn(llm.mainRequest(t, 1))
	require.NotEmpty(t, block, "the next turn must recall the earlier turn's fact")
	assert.Contains(t, block, "Max")
}

// Extraction now runs alongside the LLM call, so the turn can return first.
// A cancelled request context must not abort it and drop the memory.
func TestGraphMemory_ExtractionSurvivesCancelledTurnContext(t *testing.T) {
	llm := newTurnOrderLLM(t)
	llm.extractGate = make(chan struct{})
	ag, store := newTurnOrderAgent(t, llm)

	ctx, cancel := context.WithCancel(context.Background())
	_, err := ag.Chat(ctx, "s-cancel", "I adopted a dog named Max")
	require.NoError(t, err)

	// Turn is over and its context cancelled while extraction is still in flight.
	cancel()
	close(llm.extractGate)
	ag.FlushGraphMemoryExtraction()

	got, err := store.Recall(context.Background(), memory.RecallOpts{AgentID: "turn-order-agent", Query: "Max", Limit: 10})
	require.NoError(t, err)
	assert.NotEmpty(t, got, "a cancelled turn context must not lose the extracted memory")
}

// A resumed turn has no new user message: it re-enters the loop but must not
// extract a second time.
func TestGraphMemory_ResumedTurnDoesNotReExtract(t *testing.T) {
	llm := newTurnOrderLLM(t)
	llm.mainScript = twoCallBatch()

	sessions, err := NewSessionStore(filepath.Join(t.TempDir(), "resume.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessions.Close() })

	humanStore := shuttle.NewInMemoryHumanRequestStore()
	hooks := shuttle.NewChain([]shuttle.Hook{scopedAskHook{tool: "export_csv"}}, nil, nil)
	ag, _ := newTurnOrderAgent(t, llm,
		WithMemory(NewMemoryWithStore(sessions)),
		WithHITLPark(humanStore, 0, NewProgressNotifier()),
		WithAdmissionHooks(hooks),
	)
	ag.RegisterTool(&countingTool{name: "read_table"})
	ag.RegisterTool(&countingTool{name: "export_csv"})
	f := &parkFixture{ag: ag, store: humanStore, sessions: sessions}

	ctx := context.Background()
	_, err = ag.Chat(ctx, "s-resume", "I adopted a dog named Max")
	var parked *TurnParkedError
	require.ErrorAs(t, err, &parked)

	ag.FlushGraphMemoryExtraction()
	require.Equal(t, 1, llm.extractions(), "the new user message is extracted once")

	hr := f.pendingParked(t, "s-resume")
	_, err = ag.ResumeChat(ctx, "s-resume", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
	}, nil)
	require.NoError(t, err)

	ag.FlushGraphMemoryExtraction()
	assert.Equal(t, 1, llm.extractions(), "a resume must not extract the same message again")
}
