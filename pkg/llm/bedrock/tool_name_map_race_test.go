package bedrock

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TestClient_ToolNameMapIsPerRequest pins that the sanitized → original tool
// name table is request-local on both request paths: the Anthropic Messages
// payload (convertTools / convertResponse) and Converse
// (convertMessagesToConverse / convertToolsToConverse). One client is shared
// across sessions by the server's provider pool; when the table lived on the
// Client, concurrent requests raced on it and could return a tool call under
// another request's name. Each goroutine converts one uniquely named tool and
// must get its own original name back. Run with -race.
func TestClient_ToolNameMapIsPerRequest(t *testing.T) {
	client := &Client{}
	ctx := context.Background()

	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*2)
	for i := 0; i < goroutines; i++ {
		original := fmt.Sprintf("ns:tool_%d", i)
		tools := []shuttle.Tool{&mockTool{
			name:        original,
			description: "race probe",
			schema:      &shuttle.JSONSchema{Type: "object", Properties: map[string]*shuttle.JSONSchema{"q": {Type: "integer"}}},
		}}
		wg.Add(2)
		go func() {
			defer wg.Done()
			nameMap := make(map[string]string)
			apiTools, err := client.convertTools(ctx, tools, nameMap)
			if err != nil {
				errs <- fmt.Errorf("Messages convertTools(%s): %w", original, err)
				return
			}
			if len(apiTools) != 1 {
				errs <- fmt.Errorf("Messages convertTools(%s): got %d tools, want 1", original, len(apiTools))
				return
			}
			// Echo the sanitized name the API would have been sent.
			resp := client.convertResponse(&bedrockResponse{
				StopReason: "tool_use",
				Content: []map[string]interface{}{{
					"type":  "tool_use",
					"id":    "t1",
					"name":  apiTools[0]["name"],
					"input": map[string]interface{}{"q": 1},
				}},
			}, nameMap)
			if len(resp.ToolCalls) != 1 {
				errs <- fmt.Errorf("Messages(%s): got %d tool calls, want 1", original, len(resp.ToolCalls))
				return
			}
			if got := resp.ToolCalls[0].Name; got != original {
				errs <- fmt.Errorf("Messages: tool call named %q, want %q", got, original)
				return
			}
			errs <- nil
		}()
		go func() {
			defer wg.Done()
			nameMap := make(map[string]string)
			history := []llmtypes.Message{
				{Role: "user", Content: "go"},
				{Role: "assistant", ToolCalls: []llmtypes.ToolCall{{ID: "t0", Name: original, Input: map[string]interface{}{}}}},
			}
			_, converseMsgs := client.convertMessagesToConverse(history, nameMap)
			if len(converseMsgs) != 2 {
				errs <- fmt.Errorf("Converse(%s): got %d messages, want 2", original, len(converseMsgs))
				return
			}
			if _, err := client.convertToolsToConverse(ctx, tools, nameMap); err != nil {
				errs <- fmt.Errorf("Converse convertToolsToConverse(%s): %w", original, err)
				return
			}
			if got := llm.ReverseToolName(nameMap, llm.SanitizeToolName(original)); got != original {
				errs <- fmt.Errorf("Converse: %q reverse-mapped to %q", llm.SanitizeToolName(original), got)
				return
			}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}
