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

func TestWithSpanContext_CopiesAgentContext(t *testing.T) {
	parent := &agentContext{Context: context.Background(), turnIndex: 4}
	spanCtx, span := observability.NewMockTracer().StartSpan(parent, "child")

	child := withSpanContext(parent, spanCtx)
	assert.NotSame(t, parent, child, "the caller's Context must not be modified")
	assert.Same(t, span, observability.SpanFromContext(child))
	assert.Nil(t, observability.SpanFromContext(parent))
	assert.Equal(t, int64(4), child.(*agentContext).turnIndex)
}
