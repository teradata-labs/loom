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
	"strings"
)

// Wire types for the Responses API. Field shapes follow live captures from
// the Vercel AI Gateway taken on 2026-11-19 (testdata/).

// responsesRequest is the request body. Every JSON member it can write is
// reserved against Config.ExtraBody (see ValidateExtraBody).
type responsesRequest struct {
	Model             string          `json:"model"`
	Instructions      string          `json:"instructions,omitempty"`
	Input             []inputItem     `json:"input"`
	MaxOutputTokens   int             `json:"max_output_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	Tools             []functionTool  `json:"tools,omitempty"`
	ToolChoice        string          `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Reasoning         *reasoningParam `json:"reasoning,omitempty"`
	Include           []string        `json:"include,omitempty"`
	Store             bool            `json:"store"`
	Stream            bool            `json:"stream,omitempty"`
}

// reasoningParam is the request's reasoning object.
type reasoningParam struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// functionTool is a flat Responses function tool.
type functionTool struct {
	Type        string                 `json:"type"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
	Strict      bool                   `json:"strict"`
}

// inputItem is one entry of the request's input array. Only the members of
// its kind are set: a message (Role + Content), a function_call, a
// function_call_output (Output is a pointer so an empty result is still
// sent), or a reasoning item (Summary is a pointer so an empty summary list
// is still sent).
type inputItem struct {
	Type             string                   `json:"type,omitempty"`
	Role             string                   `json:"role,omitempty"`
	Content          []map[string]interface{} `json:"content,omitempty"`
	ID               string                   `json:"id,omitempty"`
	CallID           string                   `json:"call_id,omitempty"`
	Name             string                   `json:"name,omitempty"`
	Arguments        string                   `json:"arguments,omitempty"`
	Output           *string                  `json:"output,omitempty"`
	EncryptedContent string                   `json:"encrypted_content,omitempty"`
	Summary          *[]summaryPart           `json:"summary,omitempty"`
}

// summaryPart is one part of a reasoning item's summary.
type summaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// responseObject is a Responses API response: the blocking body, and the
// "response" member of the stream's lifecycle events.
type responseObject struct {
	ID                string         `json:"id"`
	Status            string         `json:"status"`
	Model             string         `json:"model"`
	Output            []outputItem   `json:"output"`
	Usage             *responseUsage `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error json.RawMessage `json:"error"`
}

// outputItem is one entry of a response's output array.
type outputItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Status           string          `json:"status"`
	Role             string          `json:"role"`
	Content          []outputContent `json:"content"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Arguments        string          `json:"arguments"`
	EncryptedContent string          `json:"encrypted_content"`
	Summary          []summaryPart   `json:"summary"`
}

// outputContent is one content part of a message output item.
type outputContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

// responseUsage is the usage member. input_tokens includes the cached
// tokens (OpenAI convention); cache_write_tokens is a gateway extension and
// is treated the same way.
type responseUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u *responseUsage) cacheRead() int {
	if u == nil || u.InputTokensDetails == nil {
		return 0
	}
	return u.InputTokensDetails.CachedTokens
}

func (u *responseUsage) cacheWrite() int {
	if u == nil || u.InputTokensDetails == nil {
		return 0
	}
	return u.InputTokensDetails.CacheWriteTokens
}

func (u *responseUsage) reasoning() int {
	if u == nil || u.OutputTokensDetails == nil {
		return 0
	}
	return u.OutputTokensDetails.ReasoningTokens
}

// streamEvent is one SSE data payload. Only the members the client reads are
// decoded; Response is set on the lifecycle events.
type streamEvent struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Delta       string          `json:"delta"`
	Item        *outputItem     `json:"item"`
	Response    *responseObject `json:"response"`
	// error event members ({"type":"error","code","message"}); some gateways
	// nest them under "error" instead.
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
	Error   json.RawMessage `json:"error"`
}

// apiError is a decoded error: from an error body, a failed response, or a
// stream error event.
type apiError struct {
	Type    string
	Code    string
	Message string
}

// codeOrType is the most specific classification string the error carries.
func (e apiError) codeOrType() string {
	if e.Code != "" {
		return e.Code
	}
	return e.Type
}

// parseErrorValue decodes an "error" member that is an object
// ({"type","code","message"}) or a plain string. ok is false when raw is
// absent or JSON null.
func parseErrorValue(raw json.RawMessage) (apiError, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return apiError{}, false
	}
	var obj struct {
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return apiError{Type: obj.Type, Code: rawString(obj.Code), Message: obj.Message}, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return apiError{Message: s}, true
	}
	return apiError{Message: trimmed}, true
}

// rawString renders a JSON scalar as text: a string unquoted, a number as
// written, null or absent as "".
func rawString(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return trimmed
}

// parseErrorBody decodes an error response body in either shape the gateway
// was seen to use: {"error":{"type","message"}} or the flat
// {"error":"...","type":"...","statusCode":400}. ok is false when the body
// carries no error member.
func parseErrorBody(body []byte) (apiError, bool) {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Type    string          `json:"type"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return apiError{}, false
	}
	e, ok := parseErrorValue(envelope.Error)
	if !ok {
		if envelope.Message == "" {
			return apiError{}, false
		}
		return apiError{Type: envelope.Type, Message: envelope.Message}, true
	}
	if e.Type == "" {
		e.Type = envelope.Type
	}
	return e, true
}
