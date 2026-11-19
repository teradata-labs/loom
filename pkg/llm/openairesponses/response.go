// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package openairesponses

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"go.uber.org/zap"
)

// Stop reasons, in the vocabulary pkg/agent reads (the Anthropic names the
// other clients map to).
const (
	stopEndTurn       = "end_turn"
	stopToolUse       = "tool_use"
	stopMaxTokens     = "max_tokens"
	stopRefusal       = "refusal"
	stopContentFilter = "content_filter"
)

// maxResponseBytes caps a blocking response body. Far above any real
// response; it stops a broken upstream from exhausting memory.
const maxResponseBytes = 64 << 20

// readAll reads a response body up to maxResponseBytes.
func readAll(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxResponseBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxResponseBytes)
	}
	return b, nil
}

// checkStatus turns a non-2xx response into an error carrying the HTTP
// status (llm.WrapHTTPStatus) and the body's error message. A context-length
// refusal also wraps llm.ErrContextTooLong so the agent's compaction retry
// fires. The body is consumed and capped at llm.MaxErrorBodyBytes.
func checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, llm.MaxErrorBodyBytes))
	return statusError(resp.StatusCode, body)
}

// statusError builds the error for a non-2xx status and its body.
func statusError(status int, body []byte) error {
	detail := strings.TrimSpace(string(body))
	if e, ok := parseErrorBody(body); ok {
		detail = e.Message
		if t := e.codeOrType(); t != "" {
			detail = fmt.Sprintf("%s (type: %s)", e.Message, t)
		}
	}
	err := fmt.Errorf("responses API error (status %d): %s", status, detail)
	if llm.IsOpenAIContextTooLong(status, body) {
		err = fmt.Errorf("%w: %w", err, llm.ErrContextTooLong)
	}
	return llm.WrapHTTPStatus(status, err)
}

// failureError builds the error for a failed response or a stream error
// event. The status is derived from the error code or type; a
// context_length_exceeded code wraps llm.ErrContextTooLong.
func failureError(prefix string, e apiError) error {
	msg := e.Message
	if msg == "" {
		msg = "no error message"
	}
	err := fmt.Errorf("%s: %s (type: %s)", prefix, msg, e.codeOrType())
	if e.Code == "context_length_exceeded" {
		err = fmt.Errorf("%w: %w", err, llm.ErrContextTooLong)
	}
	return llm.WrapHTTPStatus(failureStatus(e), err)
}

// failureStatus picks a representative HTTP status for an error that
// arrived without one (a failed response or a stream error event): the
// shared table for the type, then for the code, then the Responses API's own
// codes that the shared table does not list. 0 means unknown (no status is
// attached).
func failureStatus(e apiError) int {
	if s := llm.HTTPStatusForErrorType(e.Type); s != 0 {
		return s
	}
	if s := llm.HTTPStatusForErrorType(e.Code); s != 0 {
		return s
	}
	switch e.Code {
	case "rate_limit_exceeded":
		return http.StatusTooManyRequests
	case "context_length_exceeded", "invalid_prompt":
		return http.StatusBadRequest
	}
	return 0
}

// errFailedNoDetail is the error for a failed response without an error
// object.
var errFailedNoDetail = errors.New("responses API: response failed without an error object")

// convertResponse maps a response object to an LLMResponse. reportedCost is
// the gateway's cost for the call (0 = not reported). A "failed" status
// returns an error.
func (c *Client) convertResponse(resp *responseObject, nameMap map[string]string, reportedCost float64) (*llmtypes.LLMResponse, error) {
	if resp.Status == "failed" {
		if e, ok := parseErrorValue(resp.Error); ok {
			return nil, failureError("responses API: response failed", e)
		}
		return nil, errFailedNoDetail
	}
	out := &llmtypes.LLMResponse{}
	var text, thinking strings.Builder
	refused := false
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					text.WriteString(part.Text)
				case "refusal":
					refused = true
					text.WriteString(part.Refusal)
				}
			}
		case "function_call":
			if tc, ok := toolCall(item, nameMap); ok {
				out.ToolCalls = append(out.ToolCalls, tc)
			}
		case "reasoning":
			out.ThinkingBlocks = append(out.ThinkingBlocks, reasoningBlocks(item)...)
			for _, p := range item.Summary {
				if thinking.Len() > 0 && p.Text != "" {
					thinking.WriteString("\n\n")
				}
				thinking.WriteString(p.Text)
			}
		}
	}
	out.Content = text.String()
	out.Thinking = thinking.String()
	out.StopReason = stopReason(resp, refused, len(out.ToolCalls) > 0)
	out.Usage = c.usage(resp.Usage, reportedCost)

	md := map[string]interface{}{
		"model":         resp.Model,
		"finish_reason": resp.Status,
		"response_id":   resp.ID,
	}
	if resp.IncompleteDetails != nil && resp.IncompleteDetails.Reason != "" {
		md["incomplete_reason"] = resp.IncompleteDetails.Reason
	}
	out.Metadata = c.finishCost(&out.Usage, reportedCost, md)

	zap.L().Debug("openairesponses usage",
		zap.String("model", c.model),
		zap.Int("input_tokens", out.Usage.InputTokens),
		zap.Int("cache_read", out.Usage.CacheReadInputTokens),
		zap.Int("cache_write", out.Usage.CacheCreationInputTokens),
		zap.Int("output_tokens", out.Usage.OutputTokens))
	return out, nil
}

// toolCall maps a function_call item. Items with no call_id or name are
// dropped (strict upstreams reject them on replay). Arguments that are not a
// JSON object are kept under "_raw", as pkg/llm/openai does.
func toolCall(item outputItem, nameMap map[string]string) (llmtypes.ToolCall, bool) {
	if item.CallID == "" || item.Name == "" {
		zap.L().Warn("openairesponses: dropping incomplete function_call",
			zap.String("call_id", item.CallID), zap.String("tool_name", item.Name))
		return llmtypes.ToolCall{}, false
	}
	input := map[string]interface{}{}
	if strings.TrimSpace(item.Arguments) != "" {
		if err := json.Unmarshal([]byte(item.Arguments), &input); err != nil || input == nil {
			input = map[string]interface{}{"_raw": item.Arguments}
		}
	}
	return llmtypes.ToolCall{
		ID:    item.CallID,
		Name:  llm.ReverseToolName(nameMap, item.Name),
		Input: input,
	}, true
}

// stopReason derives the loom stop reason. An incomplete response reports
// why it stopped; otherwise a refusal, then tool calls, decide.
func stopReason(resp *responseObject, refused, hasToolCalls bool) string {
	if resp.Status == "incomplete" {
		reason := ""
		if resp.IncompleteDetails != nil {
			reason = resp.IncompleteDetails.Reason
		}
		switch reason {
		case "max_output_tokens":
			return stopMaxTokens
		case "content_filter":
			return stopContentFilter
		case "":
			return stopMaxTokens
		default:
			return reason
		}
	}
	switch {
	case refused:
		return stopRefusal
	case hasToolCalls:
		return stopToolUse
	default:
		return stopEndTurn
	}
}

// usage maps the Responses usage to loom's disjoint buckets.
func (c *Client) usage(u *responseUsage, reportedCost float64) llmtypes.Usage {
	if u == nil {
		return llmtypes.Usage{CostUSD: reportedCost}
	}
	read, write := u.cacheRead(), u.cacheWrite()
	uncached := max(0, u.InputTokens-read-write)
	out := llmtypes.Usage{
		InputTokens:              uncached,
		OutputTokens:             u.OutputTokens,
		TotalTokens:              uncached + u.OutputTokens,
		CacheReadInputTokens:     read,
		CacheCreationInputTokens: write,
		ThinkingTokens:           u.reasoning(),
		RateLimitTokens:          meteredTokens(u),
	}
	if reportedCost > 0 {
		out.CostUSD = reportedCost
	} else {
		out.CostUSD, _ = c.estimateCost(u.InputTokens, u.OutputTokens, read, write)
	}
	return out
}

// meteredTokens is the raw, cache-inclusive total that OpenAI-style TPM
// limits count: total_tokens, or input+output when it is absent.
func meteredTokens(u *responseUsage) int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens
}
