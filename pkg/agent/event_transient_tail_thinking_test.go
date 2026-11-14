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
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/anthropic"
)

// TestReview589_TailKeepsThinkingPrefix (review round 2, F3): with segmented
// memory the caller tail (WithTransientTail) must be pinned with the turn's
// built-in notes, not appended to a copy on each call and dropped by the next
// one — a dropped tail changes the messages in front of replayed thinking
// blocks, which the provider rejects. The tail is a constant string, so any
// failure comes from dropping it, not from its content changing.
func TestReview589_TailKeepsThinkingPrefix(t *testing.T) {
	for _, withTail := range []bool{false, true} {
		stub := &stubAnthropic{}
		srv := httptest.NewServer(stub)
		cfg := DefaultConfig()
		cfg.PatternConfig = DefaultPatternConfig()
		cfg.PatternConfig.UseLLMClassifier = false
		opts := []Option{WithConfig(cfg)}
		if withTail {
			opts = append(opts, WithTransientTail(func(context.Context) string { return "status: ok" }))
		}
		ag := NewAgent(&mockBackend{}, anthropic.NewClient(anthropic.Config{APIKey: "test", Model: "claude-haiku-5-5", Endpoint: srv.URL}), opts...)
		ag.RegisterTool(namedStubTool{name: "note_fact"})
		_, err := ag.Chat(context.Background(), "s", "first message")
		require.NoError(t, err)
		_, err = ag.Chat(context.Background(), "s", "second message")
		require.NoError(t, err)
		reqs := stub.requests()
		require.Len(t, reqs, 4)
		assert.Zero(t, thinkingPrefixFailures(t, reqs), "withTail=%v", withTail)
		srv.Close()
	}
}
