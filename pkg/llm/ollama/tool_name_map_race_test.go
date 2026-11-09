package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TestClient_ToolNameMapIsPerRequest pins that the sanitized → original tool
// name table is request-local and that the one-time native-tool probe is safe
// to trigger from concurrent first requests. One client is shared across
// sessions by the server's provider pool; when the table lived on the Client,
// concurrent requests raced on it and could return a tool call under another
// request's name. Each goroutine declares one uniquely named tool, the fake
// Ollama echoes back whatever sanitized name it was sent, and the reply must
// carry the goroutine's own original name. Run with -race.
func TestClient_ToolNameMapIsPerRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"template":"{{ if .Tools }}tools{{ end }}"}`))
		case "/api/chat":
			var req struct {
				Tools []struct {
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
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = fmt.Fprintf(w, `{"model":"llama3.1","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":%q,"arguments":{"q":1}}}]}}`+"\n", name)
			_, _ = fmt.Fprint(w, `{"model":"llama3.1","message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":10,"eval_count":5}`+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Model: "llama3.1"})

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
