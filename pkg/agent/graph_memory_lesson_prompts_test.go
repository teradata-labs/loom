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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/prompts"
)

// F4: the mining prompt and the injection headings come from the prompt
// registry when it holds the key, and fall back to the built-in text
// otherwise.
func TestLessonPromptsUseRegistryWithFallback(t *testing.T) {
	ctx := context.Background()
	pairs := []lessonPair{{Tool: "x", FailingIn: "{a}", ErrorText: "boom", SucceedsIn: "{b}", ChangedFragments: []string{"f1"}}}

	// No registry: the built-in text, identical to buildLessonMiningPrompt.
	bare := &Agent{}
	assert.Equal(t, buildLessonMiningPrompt(pairs, "the task"), bare.lessonMiningPrompt(ctx, pairs, "the task"))
	assert.Equal(t, lessonPromptFallbacks[lessonPromptHeadingStart], bare.lessonPrompt(ctx, lessonPromptHeadingStart))

	// A registry without the keys: still the built-in text.
	empty := &Agent{prompts: &keyedPrompts{values: map[string]string{}}}
	assert.Equal(t, buildLessonMiningPrompt(pairs, ""), empty.lessonMiningPrompt(ctx, pairs, ""))

	// A registry with the keys: its text is used.
	reg := &keyedPrompts{values: map[string]string{
		lessonPromptMiningPreamble:     "REGISTRY PREAMBLE",
		lessonPromptMiningTaskHeading:  "REGISTRY TASK:",
		lessonPromptMiningInstructions: "REGISTRY INSTRUCTIONS",
		lessonPromptHeadingStart:       "REGISTRY START HEADING",
	}}
	tuned := &Agent{prompts: reg}
	p := tuned.lessonMiningPrompt(ctx, pairs, "the task")
	assert.True(t, strings.HasPrefix(p, "REGISTRY PREAMBLE\n\nREGISTRY TASK:\nthe task\n\n1. tool: x\n"), p)
	assert.True(t, strings.HasSuffix(p, "REGISTRY INSTRUCTIONS\n"), p)
	assert.NotContains(t, p, "Return ONLY a JSON object")
	assert.Equal(t, "REGISTRY START HEADING", tuned.lessonPrompt(ctx, lessonPromptHeadingStart))

	// A registry entry with an unrendered placeholder is never shown.
	reg.values[lessonPromptHeadingStart] = "HEADING {{.missing}}"
	assert.Equal(t, lessonPromptFallbacks[lessonPromptHeadingStart], tuned.lessonPrompt(ctx, lessonPromptHeadingStart))
}

// F4: the shipped YAML registers every lesson key, with text identical to the
// built-in fallback, so moving the text did not change behaviour.
func TestLessonPromptsShippedYAMLMatchesFallback(t *testing.T) {
	reg := prompts.NewFileRegistry("../../prompts")
	require.NoError(t, reg.Reload(context.Background()))
	ctx := context.Background()
	require.NotEmpty(t, lessonPromptFallbacks)
	for key, fallback := range lessonPromptFallbacks {
		got, err := reg.Get(ctx, key, nil)
		require.NoError(t, err, key)
		assert.Equal(t, fallback, got, key)
	}
	// And the compaction prompt in the same directory still loads.
	_, err := reg.Get(ctx, compactionPromptKey, nil)
	require.NoError(t, err)
}
