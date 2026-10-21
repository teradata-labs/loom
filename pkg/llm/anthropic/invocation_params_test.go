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
package anthropic

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

var _ llmtypes.InvocationParameterReporter = (*Client)(nil)

// TestInvocationParameters_MatchTheRequest: the parameters recorded on the
// LLM span are the ones the request sends, including the forced temperature
// when thinking rides.
func TestInvocationParameters_MatchTheRequest(t *testing.T) {
	ctx := context.Background()
	c := NewClient(Config{APIKey: "k", Model: "claude-3-5-sonnet-20241022", MaxTokens: 4096, Temperature: 0.2})
	assert.Equal(t, map[string]interface{}{"max_tokens": 4096, "temperature": 0.2}, c.InvocationParameters(ctx))

	c = NewClient(Config{APIKey: "k", Model: "claude-3-5-sonnet-20241022", MaxTokens: 64000, Temperature: 0.2, ThinkingLevel: "low"})
	assert.Equal(t, map[string]interface{}{"max_tokens": 64000, "temperature": 1.0, "thinking": true}, c.InvocationParameters(ctx))
}
