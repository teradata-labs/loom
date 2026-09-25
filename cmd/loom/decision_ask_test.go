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
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/decision/sites"
)

func resetAskFlags(t *testing.T) {
	t.Helper()
	resetDecisionFlags(t)
	askKind, askFacts, askOptions, askWhenYes, askWhenNo, askJSON = sites.AskYesNo, "", nil, "", "", false
	t.Cleanup(func() {
		askKind, askFacts, askOptions, askWhenYes, askWhenNo, askJSON = sites.AskYesNo, "", nil, "", "", false
	})
}

func TestLoadAskFacts(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "f.json")
	require.NoError(t, os.WriteFile(jsonPath, []byte(`{"exit": 137, "signal": "KILL"}`), 0o600))
	textPath := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(textPath, []byte("plain words"), 0o600))

	got, err := loadAskFacts("@" + jsonPath)
	require.NoError(t, err)
	obj, ok := got.(map[string]any)
	require.True(t, ok, "JSON object files become structured facts")
	assert.Equal(t, float64(137), obj["exit"])

	got, err = loadAskFacts("@" + textPath)
	require.NoError(t, err)
	assert.Equal(t, "plain words", got)

	got, err = loadAskFacts(`{"a": 1}`)
	require.NoError(t, err)
	_, ok = got.(map[string]any)
	assert.True(t, ok, "inline JSON text is parsed")

	got, err = loadAskFacts(`{not json`)
	require.NoError(t, err)
	assert.Equal(t, `{not json`, got, "invalid JSON stays text")

	_, err = loadAskFacts("@" + filepath.Join(dir, "missing"))
	require.Error(t, err)
}

func TestDecisionAskWithMock(t *testing.T) {
	resetAskFlags(t)
	decisionDecider = "mock"

	t.Run("yes_no text", func(t *testing.T) {
		askFacts = "exit status 137"
		var out bytes.Buffer
		decisionAskCmd.SetOut(&out)
		require.NoError(t, runDecisionAsk(decisionAskCmd, []string{"Did it run out of memory?"}))
		assert.Contains(t, out.String(), "answer: true")
		assert.Contains(t, out.String(), "p_yes 0.750")
		assert.Contains(t, out.String(), "model mock")
	})

	t.Run("choice json", func(t *testing.T) {
		askKind, askOptions, askJSON = sites.AskChoice, []string{"transient", "auth", "bad_input"}, true
		askFacts = `{"error": "connection reset"}`
		var out bytes.Buffer
		decisionAskCmd.SetOut(&out)
		require.NoError(t, runDecisionAsk(decisionAskCmd, []string{"What kind of failure?"}))
		var ans sites.AskAnswer
		require.NoError(t, json.Unmarshal(out.Bytes(), &ans))
		assert.Equal(t, sites.AskChoice, ans.Kind)
		assert.Equal(t, "auth", ans.Answer, "the mock favours the first option alphabetically")
		assert.Len(t, ans.Probabilities, 4, "three options + none_of_these")
	})

	t.Run("scale", func(t *testing.T) {
		askKind, askOptions, askJSON = sites.AskScale, []string{"low", "medium", "high"}, false
		askFacts = "all writes failing"
		var out bytes.Buffer
		decisionAskCmd.SetOut(&out)
		require.NoError(t, runDecisionAsk(decisionAskCmd, []string{"How severe?"}))
		assert.Contains(t, out.String(), "answer: medium")
		assert.Contains(t, out.String(), "expected level 1.00")
	})

	t.Run("bad ask is a user error", func(t *testing.T) {
		askKind, askOptions = sites.AskChoice, []string{"only-one"}
		askFacts = "f"
		err := runDecisionAsk(decisionAskCmd, []string{"q"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least 2")
	})

	t.Run("unknown decider", func(t *testing.T) {
		askKind, askOptions, askFacts = sites.AskYesNo, nil, "f"
		decisionDecider = "nope"
		err := runDecisionAsk(decisionAskCmd, []string{"q"})
		require.Error(t, err)
	})
}
