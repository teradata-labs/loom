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
package bedrock

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

var (
	_ llmtypes.InvocationParameterReporter = (*SDKClient)(nil)
	_ llmtypes.InvocationParameterReporter = (*Client)(nil)
)

// TestInvocationParameters_MatchTheRequest: the SDK client leaves temperature
// out when thinking rides, as the request does; the legacy client always
// sends both.
func TestInvocationParameters_MatchTheRequest(t *testing.T) {
	ctx := context.Background()
	const older = "us.anthropic.claude-3-5-sonnet-20241022-v2:0"
	c := &SDKClient{modelID: older, maxTokens: 4096, temperature: 0.2}
	assert.Equal(t, map[string]interface{}{"max_tokens": int64(4096), "temperature": 0.2}, c.InvocationParameters(ctx))

	c = &SDKClient{modelID: older, thinkingLevel: "low", maxTokens: 64000, temperature: 0.2}
	assert.Equal(t, map[string]interface{}{"max_tokens": int64(64000), "thinking": true}, c.InvocationParameters(ctx))

	legacy := &Client{modelID: older, maxTokens: 2048, temperature: 0.7}
	assert.Equal(t, map[string]interface{}{"max_tokens": 2048, "temperature": 0.7}, legacy.InvocationParameters(ctx))
}
