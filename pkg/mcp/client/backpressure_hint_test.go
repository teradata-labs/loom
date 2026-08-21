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
package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

func toolErr(text string) *ToolResultError {
	return &ToolResultError{Result: &protocol.CallToolResult{
		IsError: true,
		Content: []protocol.Content{{Type: "text", Text: text}},
	}}
}

func TestBackpressureHintParsing(t *testing.T) {
	tests := []struct {
		name string
		text string
		want *BackpressureHint
	}{
		{
			name: "full contract",
			text: `{"code":"session_handle_budget_full","message":"m","retryable":true,"retry_after_s":42,"wait_param":"wait_s","max_wait_s":300}`,
			want: &BackpressureHint{Code: "session_handle_budget_full", RetryAfterS: 42, WaitParam: "wait_s", MaxWaitS: 300},
		},
		{
			name: "retryable without parking",
			text: `{"code":"server_busy","message":"m","retryable":true}`,
			want: &BackpressureHint{Code: "server_busy"},
		},
		{
			name: "task-level failure: no contract",
			text: `{"code":"db_error","message":"[Error 2616] Numeric overflow"}`,
			want: nil,
		},
		{
			name: "explicit retryable false",
			text: `{"code":"server_busy","retryable":false}`,
			want: nil,
		},
		{
			name: "non-JSON error text",
			text: "something went wrong",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolErr(tt.text).Backpressure()
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tt.want, *got)
		})
	}
}

func TestBackpressureNilAndNonTextContent(t *testing.T) {
	assert.Nil(t, (&ToolResultError{}).Backpressure())
	e := &ToolResultError{Result: &protocol.CallToolResult{IsError: true, Content: []protocol.Content{
		{Type: "resource_link", URI: "test://slots"},
	}}}
	assert.Nil(t, e.Backpressure())
}
