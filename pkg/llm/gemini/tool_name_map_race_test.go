package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GenerateContentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name := ""
		if len(req.Tools) > 0 && len(req.Tools[0].FunctionDeclarations) > 0 {
			name = req.Tools[0].FunctionDeclarations[0].Name
		}
		chunk := fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":%q,"args":{"q":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`, name)
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chunk))
	}))
	defer server.Close()

	client := NewClient(Config{APIKey: "test-key", Model: "gemini-3-flash-preview"})
	client.httpClient.Transport = &mockTransport{baseURL: server.URL}

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
