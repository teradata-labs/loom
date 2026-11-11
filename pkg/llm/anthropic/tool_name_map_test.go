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

package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TestChat_ConcurrentCallsKeepTheirToolNames — one client serves concurrent
// calls (an agent's graph memory side calls run beside its main call), so the
// sanitized → original tool name map is per request: each call gets its own
// tool's original name back, and -race sees no shared write.
func TestChat_ConcurrentCallsKeepTheirToolNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		name := ""
		if len(req.Tools) > 0 {
			name = req.Tools[0].Name
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"m1","type":"message","role":"assistant","model":"claude-haiku-5-5",`+
			`"content":[{"type":"tool_use","id":"t1","name":%q,"input":{}}],"stop_reason":"tool_use",`+
			`"usage":{"input_tokens":10,"output_tokens":2}}`, name)
	}))
	defer srv.Close()
	c := NewClient(Config{APIKey: "test", Model: "claude-haiku-5-5", Endpoint: srv.URL, MaxTokens: 64})

	const calls = 16
	var wg sync.WaitGroup
	errs := make(chan string, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprintf("ns:tool%d", i)
			resp, err := c.Chat(context.Background(),
				[]llmtypes.Message{{Role: "user", Content: "go"}},
				[]shuttle.Tool{&mockTool{name: want, description: "d"}})
			if err != nil {
				errs <- fmt.Sprintf("call %d: %v", i, err)
				return
			}
			if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != want {
				errs <- fmt.Sprintf("call %d: got tool calls %+v, want one named %q", i, resp.ToolCalls, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
