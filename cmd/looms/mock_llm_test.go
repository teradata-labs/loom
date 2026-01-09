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
//go:build hawk

package main

import (
	"context"
	"fmt"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// mockLLMProvider implements agent.LLMProvider for testing
type mockLLMProvider struct {
	response    string
	shouldError bool
	errorMsg    string
	toolsToUse  []string
}

func (m *mockLLMProvider) Name() string {
	return "mock-llm"
}

func (m *mockLLMProvider) Model() string {
	return "mock-model-v1"
}

func (m *mockLLMProvider) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	if m.shouldError {
		return nil, fmt.Errorf("%s", m.errorMsg)
	}

	return &llmtypes.LLMResponse{
		Content: m.response,
		Usage: llmtypes.Usage{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
			CostUSD:      0.001,
		},
		StopReason: "end_turn",
	}, nil
}
