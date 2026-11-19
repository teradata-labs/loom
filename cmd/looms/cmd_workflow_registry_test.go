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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/pkg/agent"
)

// `looms workflow` builds its agents with the server's llm.max_tokens, as
// `looms serve` does, so an agent with no max_tokens of its own reserves what
// its client sends. The other registry fields pass through unchanged.
func TestWorkflowRegistryConfig_CarriesTheServersMaxTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  *Config
		want int
	}{
		{name: "server max_tokens set", cfg: &Config{LLM: LLMConfig{MaxTokens: 16000}}, want: 16000},
		{name: "server max_tokens unset", cfg: &Config{}, want: 0},
		{name: "no config", cfg: nil, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := workflowRegistryConfig(agent.RegistryConfig{ConfigDir: "/agents", DBPath: "/db"}, tt.cfg)
			assert.Equal(t, tt.want, got.MaxTokens, "the registry does not carry the server's llm.max_tokens")
			assert.Equal(t, "/agents", got.ConfigDir, "ConfigDir changed")
			assert.Equal(t, "/db", got.DBPath, "DBPath changed")
		})
	}
}
