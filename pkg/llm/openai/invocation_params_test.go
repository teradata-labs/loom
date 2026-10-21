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
package openai

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

var _ llmtypes.InvocationParameterReporter = (*Client)(nil)

// TestInvocationParameters_UseTheModelsTokenField: the token limit is
// reported under the field the request uses for the model.
func TestInvocationParameters_UseTheModelsTokenField(t *testing.T) {
	ctx := context.Background()
	c := NewClient(Config{APIKey: "k", Model: "gpt-4o", MaxTokens: 1000, Temperature: 0.3})
	assert.Equal(t, map[string]interface{}{"max_completion_tokens": 1000, "temperature": 0.3}, c.InvocationParameters(ctx))

	c = NewClient(Config{APIKey: "k", Model: "gpt-3.5-turbo", MaxTokens: 500, Temperature: 0.3})
	assert.Equal(t, map[string]interface{}{"max_tokens": 500, "temperature": 0.3}, c.InvocationParameters(ctx))
}
