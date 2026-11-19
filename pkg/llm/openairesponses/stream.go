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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Stream event types the client acts on. Others (response.created,
// response.in_progress, the *.done text events, summary part events, …) are
// passed to the inspector and otherwise ignored.
const (
	evOutputTextDelta     = "response.output_text.delta"
	evRefusalDelta        = "response.refusal.delta"
	evReasoningSummary    = "response.reasoning_summary_text.delta"
	evOutputItemAdded     = "response.output_item.added"
	evOutputItemDone      = "response.output_item.done"
	evFunctionArgsDelta   = "response.function_call_arguments.delta"
	evResponseCompleted   = "response.completed"
	evResponseIncomplete  = "response.incomplete"
	evResponseFailed      = "response.failed"
	evError               = "error"
	sseDataPrefix         = "data:"
	sseDone               = "[DONE]"
	maxSSELineBytes       = 8 << 20
	initialSSEBufferBytes = 64 << 10
)

// ErrTruncatedStream is returned when the SSE stream ends without
// response.completed, response.incomplete or response.failed. The partial
// output is discarded: it cannot be told apart from a complete answer.
// It is llm.ErrStreamTruncated, so callers can test for any provider's
// truncation with one sentinel.
var ErrTruncatedStream = llm.ErrStreamTruncated

// streamState accumulates what the stream reported before its terminal
// event, as the fallback when the terminal response object has no output.
type streamState struct {
	text      strings.Builder
	refusal   strings.Builder
	items     map[int]outputItem
	argsBytes map[int]int
}

// ChatStream sends the conversation with stream=true. tokenCallback receives
// each output_text delta in order, synchronously. Reasoning summary and
// function-call argument deltas never reach it; they are reported through
// llmtypes.NotifyStreamActivity and NotifyToolInputProgress instead.
func (c *Client) ChatStream(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool,
	tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {
	prepStart := time.Now()
	nameMap := make(map[string]string)
	req, err := c.buildRequest(ctx, messages, tools, nameMap, true)
	if err != nil {
		return nil, err
	}
	body, err := c.marshalRequest(req)
	if err != nil {
		return nil, err
	}

	sendStart := time.Now()
	httpResp, err := c.sendStreamingRequest(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: stream request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if err := checkStatus(httpResp); err != nil {
		return nil, err
	}

	st := streamState{items: make(map[int]outputItem), argsBytes: make(map[int]int)}
	var facts llm.ResponseFacts
	var final *responseObject
	var firstChunkAt time.Time
	chunks := 0

	scanner := bufio.NewScanner(httpResp.Body)
	// A gateway may put a whole response object on one data line.
	scanner.Buffer(make([]byte, 0, initialSSEBufferBytes), maxSSELineBytes)
scan:
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, sseDataPrefix) {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, sseDataPrefix))
		if payload == "" {
			continue
		}
		if payload == sseDone {
			break
		}
		if firstChunkAt.IsZero() {
			firstChunkAt = time.Now()
		}
		chunks++
		if c.inspector != nil {
			facts.Merge(c.inspect(httpResp.Header, []byte(payload)))
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue // malformed payload: skip, as pkg/llm/openai does
		}
		switch ev.Type {
		case evOutputTextDelta:
			if ev.Delta != "" {
				st.text.WriteString(ev.Delta)
				if tokenCallback != nil {
					tokenCallback(ev.Delta)
				}
			}
		case evRefusalDelta:
			st.refusal.WriteString(ev.Delta)
			llmtypes.NotifyStreamActivity(ctx)
		case evReasoningSummary:
			llmtypes.NotifyStreamActivity(ctx)
		case evOutputItemAdded, evOutputItemDone:
			if ev.Item != nil {
				st.items[ev.OutputIndex] = *ev.Item
			}
		case evFunctionArgsDelta:
			st.argsBytes[ev.OutputIndex] += len(ev.Delta)
			item := st.items[ev.OutputIndex]
			llmtypes.NotifyToolInputProgress(ctx, llmtypes.ToolInputProgress{
				Index:      ev.OutputIndex,
				ToolCallID: item.CallID,
				ToolName:   llm.ReverseToolName(nameMap, item.Name),
				Bytes:      st.argsBytes[ev.OutputIndex],
			})
			llmtypes.NotifyStreamActivity(ctx)
		case evResponseCompleted, evResponseIncomplete:
			if ev.Response != nil {
				final = ev.Response
			} else {
				final = &responseObject{Status: strings.TrimPrefix(ev.Type, "response.")}
			}
			break scan
		case evResponseFailed:
			if ev.Response != nil {
				if e, ok := parseErrorValue(ev.Response.Error); ok {
					return nil, failureError("responses API: response failed", e)
				}
			}
			return nil, errFailedNoDetail
		case evError:
			return nil, failureError("responses API: stream error", streamErrorOf(ev))
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("openairesponses: SSE line exceeded %d bytes: %w", maxSSELineBytes, err)
		}
		return nil, fmt.Errorf("openairesponses: read stream: %w", err)
	}
	if final == nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, llm.TruncatedStreamError("stream ended without response.completed, response.incomplete or response.failed")
	}
	if len(final.Output) == 0 {
		final.Output = st.fallbackOutput()
	}
	if final.Model == "" {
		final.Model = c.model
	}

	out, err := c.convertResponse(final, nameMap, facts.CostUSD)
	if err != nil {
		return nil, err
	}
	out.Metadata["streaming"] = true
	out.Metadata["prep_ms"] = sendStart.Sub(prepStart).Milliseconds()
	out.Metadata["sse_chunks"] = chunks
	if !firstChunkAt.IsZero() {
		out.Metadata["ttft_ms"] = firstChunkAt.Sub(sendStart).Milliseconds()
		out.Metadata["gen_ms"] = time.Since(firstChunkAt).Milliseconds()
	}
	out.Metadata = facts.Apply(out.Metadata)
	c.recordTokens(final)
	return out, nil
}

// fallbackOutput rebuilds an output array from the items the stream
// reported, in output_index order. Text deltas stand in for a message item
// that never arrived complete.
func (st *streamState) fallbackOutput() []outputItem {
	out := make([]outputItem, 0, len(st.items)+1)
	hasMessage := false
	for _, idx := range slices.Sorted(maps.Keys(st.items)) {
		item := st.items[idx]
		if item.Type == "message" {
			if len(item.Content) == 0 {
				continue // only output_item.added arrived; the deltas below stand in
			}
			hasMessage = true
		}
		out = append(out, item)
	}
	if !hasMessage && (st.text.Len() > 0 || st.refusal.Len() > 0) {
		msg := outputItem{Type: "message", Role: "assistant"}
		if st.text.Len() > 0 {
			msg.Content = append(msg.Content, outputContent{Type: "output_text", Text: st.text.String()})
		}
		if st.refusal.Len() > 0 {
			msg.Content = append(msg.Content, outputContent{Type: "refusal", Refusal: st.refusal.String()})
		}
		out = append(out, msg)
	}
	return out
}

// streamErrorOf reads an error event in either shape: members at the top
// level ({"type":"error","code","message"}) or nested under "error".
func streamErrorOf(ev streamEvent) apiError {
	if e, ok := parseErrorValue(ev.Error); ok {
		return e
	}
	return apiError{Code: rawString(ev.Code), Message: ev.Message}
}
