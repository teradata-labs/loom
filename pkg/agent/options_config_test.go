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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithConfigKeepsIdentityFromEarlierOptions: WithName, WithDescription
// and WithSystemPrompt write into the agent's Config; a later WithConfig with
// a fresh Config used to replace it wholesale and silently drop them. The
// registry and the CLI build agents in exactly that order, so every agent
// they built ran without its system prompt.
func TestWithConfigKeepsIdentityFromEarlierOptions(t *testing.T) {
	t.Parallel()
	ag := NewAgent(nil, rerankReplyLLM{reply: "1"},
		WithName("skeptic"),
		WithDescription("argues against"),
		WithSystemPrompt("Argue against the proposition."),
		WithConfig(&Config{MaxTurns: 3}))
	cfg := ag.GetConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, "skeptic", ag.GetName())
	assert.Equal(t, "argues against", cfg.Description)
	assert.Equal(t, "Argue against the proposition.", cfg.SystemPrompt)
	assert.Equal(t, 3, cfg.MaxTurns, "the new config's own fields still apply")

	// The incoming config's own values win when set.
	ag2 := NewAgent(nil, rerankReplyLLM{reply: "1"},
		WithSystemPrompt("old"),
		WithConfig(&Config{SystemPrompt: "new", Name: "n2"}))
	assert.Equal(t, "new", ag2.GetConfig().SystemPrompt)
	assert.Equal(t, "n2", ag2.GetName())

	// And WithSystemPrompt after WithConfig still wins, as before.
	ag3 := NewAgent(nil, rerankReplyLLM{reply: "1"},
		WithConfig(&Config{SystemPrompt: "from config"}),
		WithSystemPrompt("later"))
	assert.Equal(t, "later", ag3.GetConfig().SystemPrompt)
}
