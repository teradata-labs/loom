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

package bedrock

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/teradata-labs/loom/pkg/llm"
)

// ThinkingOffWhenSuppressed says "off" exactly when the marked request drops
// the thinking field and the unmarked request sends it.
func TestThinkingOffWhenSuppressedMatchesTheRequest(t *testing.T) {
	t.Parallel()
	marked := llm.ContextSuppressThinking(context.Background())
	tests := []struct {
		name, model, level string
		want               bool
	}{
		{"budget form, thinking on", "anthropic.claude-sonnet-4-5-20250929-v1:0", "medium", true},
		{"adaptive form, thinking on", "anthropic.claude-opus-5-v1:0", "medium", false},
		{"budget form, thinking off", "anthropic.claude-sonnet-4-5-20250929-v1:0", "", false},
		{"budget form, level none", "anthropic.claude-sonnet-4-5-20250929-v1:0", "none", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &SDKClient{modelID: tc.model, thinkingLevel: tc.level, maxTokens: 64000}
			assert.Equal(t, tc.want, c.ThinkingOffWhenSuppressed(), "the report does not match the client's rule")
			zero := anthropicsdk.ThinkingConfigParamUnion{}
			dropped := c.thinkingConfig(context.Background()) != zero && c.thinkingConfig(marked) == zero
			assert.Equal(t, dropped, c.ThinkingOffWhenSuppressed(), "the report does not match what the marked request sends")
		})
	}
}
