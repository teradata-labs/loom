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

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// simpleLLM is a minimal LLMProvider returning a fixed response, shared by
// tests that only need a provider stub.
type simpleLLM struct {
	response string
}

func (l *simpleLLM) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{
		Content: l.response,
		Usage: types.Usage{
			InputTokens:  10,
			OutputTokens: 10,
		},
	}, nil
}

func (l *simpleLLM) Name() string {
	return "test-llm"
}

func (l *simpleLLM) Model() string {
	return "test-model"
}
