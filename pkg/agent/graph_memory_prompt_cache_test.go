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

// stubAnthropic records request bodies. A request that does not end in a tool
// result is answered with a thinking block and a call to the note_fact tool,
// the call's result with a fixed end_turn reply, so every message is a
// two-call tool turn whose first assistant message carries thinking.
type stubAnthropic struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (s *stubAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	n := len(s.bodies)
	s.mu.Unlock()

	var req struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)
	afterToolResult := false
	if len(req.Messages) > 0 {
		for _, b := range req.Messages[len(req.Messages)-1].Content {
			if b["type"] == "tool_result" {
				afterToolResult = true
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if !afterToolResult {
		_, _ = fmt.Fprintf(w, `{"id":"msg_%d","type":"message","role":"assistant","model":"claude-haiku-5-5",`+
			`"content":[{"type":"thinking","thinking":%q,"signature":"sig-%d"},`+
			`{"type":"tool_use","id":"toolu_%d","name":"note_fact","input":{"fact":"max"}}],`+
			`"stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":50}}`,
			n, fmt.Sprintf("Request %d: note the fact about Max before answering.", n), n, n)
		return
	}
	_, _ = fmt.Fprintf(w, `{"id":"msg_%d","type":"message","role":"assistant","model":"claude-haiku-5-5",`+
		`"content":[{"type":"text","text":%q}],"stop_reason":"end_turn",`+
		`"usage":{"input_tokens":100,"output_tokens":50}}`,
		n, "Max sounds like a good dog. "+strings.Repeat("Here is a longer answer about dogs. ", 20))
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
	noted  bool   // a graph memory recall note
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
		out = append(out, cacheBlock{key: fmt.Sprintf("%x", h.Sum(nil)), size: len(enc), marked: marked,
			noted: strings.Contains(string(enc), graphMemoryContextMarker)})
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

// thinkingPrefixFailures checks the provider's preserved-thinking rule over
// captured request bodies: a thinking block stays valid only while everything
// sent before it — tools, system and every earlier message — is unchanged.
// stubAnthropic signs request n's thinking block "sig-n", so the block's
// prefix is all of request n; every later request that replays it must send
// that same prefix before it (cache_control markers aside). It returns the
// number of replays that fail.
func thinkingPrefixFailures(t *testing.T, reqs [][]byte) int {
	t.Helper()
	written := make(map[string]string) // signature → key of the prefix it was written after
	failures := 0
	for i, body := range reqs {
		var req struct {
			Messages []struct {
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		blocks := cacheBlocks(t, body)
		// Signatures in content order, aligned with blocks past tools and system.
		var sigs []string
		for _, m := range req.Messages {
			for _, b := range m.Content {
				sig, _ := b["signature"].(string)
				if b["type"] != "thinking" {
					sig = ""
				}
				sigs = append(sigs, sig)
			}
		}
		offset := len(blocks) - len(sigs)
		for j, sig := range sigs {
			if sig == "" {
				continue
			}
			want, ok := written[sig]
			if !ok {
				t.Errorf("request %d: replays %s, which no earlier request produced", i+1, sig)
				continue
			}
			got := ""
			if k := offset + j - 1; k >= 0 {
				got = blocks[k].key
			}
			if got != want {
				failures++
				t.Logf("request %d: %s follows a prefix that changed since it was written", i+1, sig)
			}
		}
		if len(blocks) > 0 {
			written[fmt.Sprintf("sig-%d", i+1)] = blocks[len(blocks)-1].key
		}
	}
	return failures
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
// much of each request the prompt cache serves. Each message is a tool turn
// whose first response carries a thinking block. The recall note is a
// <system-reminder> pinned after the message for the rest of its turn, so the
// request only grows within a turn and never carries an earlier turn's note:
// the system prompt and the conversation up to the previous message stay
// cached. Every replayed thinking block follows the prefix it was written
// after (thinkingPrefixFailures); a settled turn that had a recall note drops
// its blocks, since the note is gone. Run with -v for the per-request numbers.
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
	ag.RegisterTool(namedStubTool{name: "note_fact"})

	const messages = 8
	ctx := context.Background()
	for i := 1; i <= messages; i++ {
		_, err := ag.Chat(ctx, "s-cache", fmt.Sprintf("Here is something new about my dog Max, number %d. ", i)+
			strings.Repeat("He likes long walks and chasing the ball in the park. ", 10))
		require.NoError(t, err)
		ag.FlushGraphMemoryExtraction()
	}

	reqs := stub.requests()
	require.Len(t, reqs, 2*messages, "two main calls per message: the tool call and the answer")

	cache := &promptCache{written: map[string]bool{}}
	var cachedLater, totalLater, prevReused int
	for i, body := range reqs {
		notes := strings.Count(string(body), graphMemoryContextMarker)
		blocks := cacheBlocks(t, body)
		cached, total := cache.send(blocks)
		thinking := strings.Count(string(body), `"type":"thinking"`)
		t.Logf("request %2d: %5d of %5d bytes from cache (%3.0f%%), recall notes: %d, thinking blocks: %d",
			i+1, cached, total, 100*float64(cached)/float64(total), notes, thinking)
		// The first message recalled nothing, so its thinking block is
		// replayed while its prefix holds. Later settled messages had a recall
		// note before their block, gone once they settled, so theirs are left
		// out. This message's own block rides its second call.
		wantThinking := i % 2
		if i >= 2 {
			wantThinking++
		}
		assert.Equal(t, wantThinking, thinking, "request %d: settled and in-turn thinking blocks replayed", i+1)
		// What the next request can read back: everything before this
		// request's recall note (the note goes when the turn settles).
		reused := 0
		for _, b := range blocks {
			if b.noted {
				break
			}
			reused += b.size
		}
		if i == 0 {
			assert.Zero(t, cached, "nothing is cached before the first call")
			prevReused = reused
			continue
		}
		cachedLater += cached
		totalLater += total

		// Every message after the first recalls the earlier ones' facts, in
		// one note on both of its calls: the notes of earlier messages are not
		// carried along. The first message has nothing to recall.
		wantNotes := 1
		if i < 2 {
			wantNotes = 0
		}
		assert.Equal(t, wantNotes, notes, "request %d: recall notes in the request", i+1)

		var req struct {
			System []map[string]any `json:"system"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		for _, b := range req.System {
			assert.NotContains(t, b["text"], graphMemoryContextMarker, "request %d: recall note in the system field", i+1)
		}
		// Everything the previous request sent before its recall note is
		// read back from the cache.
		assert.GreaterOrEqual(t, cached, prevReused,
			"request %d: the previous request, up to its recall note, comes from the cache", i+1)
		prevReused = reused
	}
	t.Logf("requests 2-%d: %d of %d bytes from cache (%.0f%%)",
		len(reqs), cachedLater, totalLater, 100*float64(cachedLater)/float64(totalLater))
	assert.Zero(t, thinkingPrefixFailures(t, reqs), "every replayed thinking block follows the prefix it was written after")
}
