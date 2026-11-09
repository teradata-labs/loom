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
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

func TestSetToolSpanInput_TruncatedArgumentsAreNotJSON(t *testing.T) {
	span := &observability.Span{Attributes: map[string]interface{}{}}
	long := strings.Repeat("x", observability.DefaultMaxContentLength*2)
	setToolSpanInput(span, ToolCall{Name: "file_write", Input: map[string]interface{}{"content": long}})

	in, _ := span.Attributes["input.value"].(string)
	assert.Equal(t, observability.DefaultMaxContentLength+1, len([]rune(in)), "bounded, plus the ellipsis")
	_, hasMime := span.Attributes["input.mime_type"]
	assert.False(t, hasMime, "truncated arguments are not valid JSON")
	_, hasID := span.Attributes["gen_ai.tool.call.id"]
	assert.False(t, hasID, "no call ID, no attribute")
}

func TestToolOutputPreview(t *testing.T) {
	tests := []struct {
		name   string
		result *shuttle.Result
		err    error
		want   string
	}{
		{name: "execution error", err: errors.New("circuit breaker open"), want: "error: circuit breaker open"},
		{name: "no result", want: ""},
		{name: "tool error", result: &shuttle.Result{Error: &shuttle.Error{Code: "not_found", Message: "no such file"}}, want: "error: not_found: no such file"},
		{name: "string data", result: &shuttle.Result{Success: true, Data: "hello"}, want: "hello"},
		{name: "structured data", result: &shuttle.Result{Success: true, Data: map[string]interface{}{"rows": 2}}, want: `{"rows":2}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toolOutputPreview(tt.result, tt.err))
		})
	}
}

func TestSetToolSpanInput_RedactsBeforeTruncation(t *testing.T) {
	const secret = "hunter2boundarysecret987654"
	// Prefix is {"a_padding":" (14) + 160 + ","password":" (14) = 188 runes,
	// so "password":"<secret>" crosses rune 200 with 12 runes of secret before
	// the truncation boundary. Truncation without prior redaction would cut off
	// the closing quote and leak secret[:12].
	padding := strings.Repeat("a", 160)
	span := &observability.Span{Attributes: map[string]interface{}{}}
	setToolSpanInput(span, ToolCall{
		Name: "db_connect",
		Input: map[string]interface{}{
			"a_padding": padding,
			"password":  secret,
		},
	})

	in, _ := span.Attributes["input.value"].(string)
	assert.NotContains(t, in, secret[:6], "no prefix of a boundary-crossing secret may survive in input.value")
	assert.Contains(t, in, observability.RedactedValue)

	out := toolOutputPreview(&shuttle.Result{
		Success: true,
		Data:    `{"a_padding":"` + padding + `","password":"` + secret + `"}`,
	}, nil)
	assert.NotContains(t, out, secret[:6], "no prefix of a boundary-crossing secret may survive in output.value")
	assert.Contains(t, out, observability.RedactedValue)
}

func TestWithSpanContext_CopiesAgentContext(t *testing.T) {
	parent := &agentContext{Context: context.Background(), turnIndex: 4}
	spanCtx, span := observability.NewMockTracer().StartSpan(parent, "child")

	child := withSpanContext(parent, spanCtx)
	assert.NotSame(t, parent, child, "the caller's Context must not be modified")
	assert.Same(t, span, observability.SpanFromContext(child))
	assert.Nil(t, observability.SpanFromContext(parent))
	assert.Equal(t, int64(4), child.(*agentContext).turnIndex)
}

func TestToolSpanPreviews_SkippedWhenSpanIsNotRecorded(t *testing.T) {
	_, span := observability.NewNoOpTracer().StartSpan(context.Background(), "agent.tool_execution")
	setToolSpanInput(span, ToolCall{Name: "query", Input: map[string]interface{}{"sql": "select 1"}})

	assert.Equal(t, "query", span.Attributes["tool_name"], "cheap identity attributes are still set")
	_, hasInput := span.Attributes["input.value"]
	assert.False(t, hasInput, "no preview is built for a NoOp span")
}

// largeToolResult is about 1 MB of JSON rows, the shape of a SQL tool result.
func largeToolResult() *shuttle.Result {
	rows := make([]map[string]interface{}, 12_000)
	for i := range rows {
		rows[i] = map[string]interface{}{"customer_id": i, "name": "Customer", "region": "EMEA", "note": "order shipped via ground"}
	}
	return &shuttle.Result{Success: true, Data: rows}
}

// BenchmarkToolOutputPreview_1MBJSON measures the tool span's output preview
// for a 1 MB result. "string" is a result whose data is already JSON text;
// "structured" also pays for json.Marshal of the rows, which the preview needs
// to render them and which redaction does not add to.
func BenchmarkToolOutputPreview_1MBJSON(b *testing.B) {
	structured := largeToolResult()
	text, err := json.Marshal(structured.Data)
	if err != nil {
		b.Fatal(err)
	}
	for _, bc := range []struct {
		name   string
		result *shuttle.Result
	}{
		{"string", &shuttle.Result{Success: true, Data: string(text)}},
		{"structured", structured},
	} {
		b.Run(bc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = toolOutputPreview(bc.result, nil)
			}
		})
	}
}
