package azureopenai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TestClient_ToolNameMapIsPerRequest pins that the sanitized → original tool
// name table is request-local. One client is shared across sessions by the
// server's provider pool; when the table lived on the Client, concurrent
// requests raced on it and could return a tool call under another request's
// name. Each goroutine declares one uniquely named tool, the fake API echoes
// back whatever sanitized name it was sent, and the reply must carry the
// goroutine's own original name. Run with -race.
func TestClient_ToolNameMapIsPerRequest(t *testing.T) {
	const (
		toolChunk   = `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":%q,"arguments":"{}"}}]},"finish_reason":null}]}` + "\n\n"
		finishChunk = `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n"
		completion  = `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":%q,"arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream bool `json:"stream"`
			Tools  []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name := ""
		if len(req.Tools) > 0 {
			name = req.Tools[0].Function.Name
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, toolChunk, name)
			_, _ = fmt.Fprint(w, finishChunk)
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, completion, name)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		Endpoint:     server.URL,
		DeploymentID: "gpt-4o",
		APIKey:       "test-key",
		APIVersion:   "2024-10-21",
	})
	require.NoError(t, err)

	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*2)
	for i := 0; i < goroutines; i++ {
		original := fmt.Sprintf("ns:tool_%d", i)
		tools := []shuttle.Tool{&shuttle.MockTool{
			MockName:        original,
			MockDescription: "race probe",
			MockSchema:      &shuttle.JSONSchema{Type: "object", Properties: map[string]*shuttle.JSONSchema{"q": {Type: "integer"}}},
		}}
		msgs := []llmtypes.Message{{Role: "user", Content: "go"}}
		wg.Add(2)
		go func() {
			defer wg.Done()
			resp, err := client.Chat(context.Background(), msgs, tools)
			errs <- toolNameRoundTripErr("Chat", original, resp, err)
		}()
		go func() {
			defer wg.Done()
			resp, err := client.ChatStream(context.Background(), msgs, tools, nil)
			errs <- toolNameRoundTripErr("ChatStream", original, resp, err)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}

// toolNameRoundTripErr reports whether resp carries exactly one tool call
// named want (the original, un-sanitized name).
func toolNameRoundTripErr(method, want string, resp *llmtypes.LLMResponse, err error) error {
	if err != nil {
		return fmt.Errorf("%s(%s): %w", method, want, err)
	}
	if len(resp.ToolCalls) != 1 {
		return fmt.Errorf("%s(%s): got %d tool calls, want 1", method, want, len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].Name; got != want {
		return fmt.Errorf("%s: tool call named %q, want %q", method, got, want)
	}
	return nil
}
