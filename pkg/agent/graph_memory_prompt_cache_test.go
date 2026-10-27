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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Graph memory recall and the Anthropic prompt cache, end to end: a
// conversation with graph memory on, its main calls sent over the wire by the
// real Anthropic client to a stub endpoint, and every captured request
// replayed through a model of Anthropic's prefix cache. Each message's recall
// note differs (the memories grow), so where the note sits decides how much of
// the request the cache can serve.

// cacheRecallLLM answers the graph memory side calls (extraction, recall
// query, re-rank) itself and sends every main chat call through an Anthropic
// client, so the main calls are exactly what the API would receive.
type cacheRecallLLM struct {
	main  *anthropic.Client
	mu    sync.Mutex
	facts int
}

func (l *cacheRecallLLM) Name() string  { return "anthropic" }
func (l *cacheRecallLLM) Model() string { return "claude-haiku-5-5" }

func (l *cacheRecallLLM) Chat(ctx context.Context, msgs []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	switch {
	case promptContains(msgs, "Extract entities, relationships, and memories"):
		l.mu.Lock()
		l.facts++
		n := l.facts
		l.mu.Unlock()
		data, err := json.Marshal(ExtractedGraphData{
			Entities: []ExtractedEntity{{Name: "max", EntityType: "concept"}},
			Memories: []ExtractedMemory{{
				Content:    fmt.Sprintf("Fact %d: the user told the assistant something new about their dog Max", n),
				Summary:    fmt.Sprintf("Max fact %d", n),
				MemoryType: "fact",
				Salience:   0.8,
				Entities:   []ExtractedEntityRole{{Name: "max", Role: "about"}},
			}},
		})
		if err != nil {
			return nil, err
		}
		return &llmtypes.LLMResponse{Content: string(data)}, nil
	case promptContains(msgs, "Distill this message into a concise search query"):
		return &llmtypes.LLMResponse{Content: "dog Max"}, nil
	case promptContains(msgs, "Candidate memories:"):
		return &llmtypes.LLMResponse{Content: "1,2,3,4,5,6,7,8,9,10"}, nil
	}
	return l.main.Chat(ctx, msgs, tools)
}

// stubAnthropic records request bodies and answers each with a fixed reply.
type stubAnthropic struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (s *stubAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"id":"msg_%d","type":"message","role":"assistant","model":"claude-haiku-5-5",`+
		`"content":[{"type":"text","text":%q}],"stop_reason":"end_turn",`+
		`"usage":{"input_tokens":100,"output_tokens":50}}`,
		len(s.bodies), "Max sounds like a good dog. "+strings.Repeat("Here is a longer answer about dogs. ", 20))
}

func (s *stubAnthropic) requests() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.bodies...)
}

// cacheBlock is one cacheable unit of a request, in cache order (tools, then
// system blocks, then message content blocks).
type cacheBlock struct {
	key    string // cumulative hash of every block up to and including this one
	size   int    // bytes of this block, a stand-in for tokens
	marked bool   // carries cache_control
}

// cacheBlocks flattens a captured Messages API body into cache order. The
// cache_control marker is left out of each block's identity, as it is not
// part of the cached content.
func cacheBlocks(t *testing.T, body []byte) []cacheBlock {
	t.Helper()
	var req struct {
		Tools    []map[string]any `json:"tools"`
		System   []map[string]any `json:"system"`
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(body, &req))

	var out []cacheBlock
	h := sha256.New()
	add := func(prefix string, b map[string]any) {
		_, marked := b["cache_control"]
		delete(b, "cache_control")
		enc, err := json.Marshal(b)
		require.NoError(t, err)
		_, _ = io.WriteString(h, prefix)
		_, _ = h.Write(enc)
		out = append(out, cacheBlock{key: fmt.Sprintf("%x", h.Sum(nil)), size: len(enc), marked: marked})
	}
	for _, b := range req.Tools {
		add("tool:", b)
	}
	for _, b := range req.System {
		add("system:", b)
	}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			add(m.Role+":", b)
		}
	}
	return out
}

// promptCache models Anthropic's prefix cache for requests sent within the
// cache lifetime: each cache_control marker writes the prefix ending at its
// block, and a request reads the longest written prefix found at one of its
// markers or up to 20 blocks before one.
type promptCache struct{ written map[string]bool }

const cacheLookbackBlocks = 20

// send returns the bytes of blocks served from the cache and the request's
// total bytes, then records the prefixes this request writes.
func (c *promptCache) send(blocks []cacheBlock) (cached, total int) {
	hit := -1
	for i, b := range blocks {
		total += b.size
		if !b.marked {
			continue
		}
		for j := i; j >= 0 && j >= i-cacheLookbackBlocks; j-- {
			if c.written[blocks[j].key] {
				if j > hit {
					hit = j
				}
				break
			}
		}
	}
	for i := 0; i <= hit; i++ {
		cached += blocks[i].size
	}
	for _, b := range blocks {
		if b.marked {
			c.written[b.key] = true
		}
	}
	return cached, total
}

// TestGraphMemoryRecall_PromptCache sends eight messages in a row, each
// recalling the memories extracted from the ones before it, and measures how
// much of each message's request the prompt cache serves. The recall note
// rides the trailing system reminder, so it changes only the end of the
// request: the system prompt and the conversation before the new message stay
// cached. Run with -v for the per-message numbers.
func TestGraphMemoryRecall_PromptCache(t *testing.T) {
	stub := &stubAnthropic{}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	llm := &cacheRecallLLM{main: anthropic.NewClient(anthropic.Config{
		APIKey:   "test",
		Model:    "claude-haiku-5-5",
		Endpoint: srv.URL,
	})}

	store := newTestGraphMemoryStore(t)
	cfg := DefaultConfig()
	cfg.Name = "cache-agent"
	cfg.SystemPrompt = "Help one household with everyday questions. " +
		strings.Repeat("Answer plainly and keep track of what people say about their pets. ", 60)
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	gm := DefaultGraphMemoryConfig()
	gm.Enabled = true
	gm.EnableExtraction = true
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithGraphMemoryStore(store, gm))

	const messages = 8
	ctx := context.Background()
	for i := 1; i <= messages; i++ {
		_, err := ag.Chat(ctx, "s-cache", fmt.Sprintf("Here is something new about my dog Max, number %d. ", i)+
			strings.Repeat("He likes long walks and chasing the ball in the park. ", 10))
		require.NoError(t, err)
		ag.FlushGraphMemoryExtraction()
	}

	reqs := stub.requests()
	require.Len(t, reqs, messages, "one main call per message")

	cache := &promptCache{written: map[string]bool{}}
	var cachedLater, totalLater, prevReused int
	for i, body := range reqs {
		notes := strings.Count(string(body), graphMemoryContextMarker)
		blocks := cacheBlocks(t, body)
		cached, total := cache.send(blocks)
		t.Logf("message %d: %5d of %5d bytes from cache (%3.0f%%), recall notes in request: %d",
			i+1, cached, total, 100*float64(cached)/float64(total), notes)
		reused := total - blocks[len(blocks)-1].size
		if i == 0 {
			assert.Zero(t, cached, "nothing is cached before the first call")
			prevReused = reused
			continue
		}
		cachedLater += cached
		totalLater += total

		// Every message after the first recalls the earlier ones' facts, in
		// one note: the notes of earlier messages are not carried along.
		assert.Equal(t, 1, notes, "message %d: one recall note per request", i+1)

		var req struct {
			System []map[string]any `json:"system"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		for _, b := range req.System {
			assert.NotContains(t, b["text"], graphMemoryContextMarker, "message %d: recall note in the system field", i+1)
		}
		// Everything the previous request sent before its last block (the
		// trailing system reminder) is read back from the cache.
		assert.GreaterOrEqual(t, cached, prevReused,
			"message %d: the previous request, minus its trailing note, comes from the cache", i+1)
		prevReused = reused
	}
	t.Logf("messages 2-%d: %d of %d bytes from cache (%.0f%%)",
		messages, cachedLater, totalLater, 100*float64(cachedLater)/float64(totalLater))
}
