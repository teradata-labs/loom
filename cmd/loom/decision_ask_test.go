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
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/decision/sites"
)

func resetAskFlags(t *testing.T) {
	t.Helper()
	resetDecisionFlags(t)
	askKind, askFacts, askOptions, askWhenYes, askWhenNo, askJSON = sites.AskYesNo, "", nil, "", "", false
	prevDecider := askDecider
	t.Cleanup(func() {
		askKind, askFacts, askOptions, askWhenYes, askWhenNo, askJSON = sites.AskYesNo, "", nil, "", "", false
		askDecider = prevDecider
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
	askDecider.decider = "mock"

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
		askDecider.decider = "nope"
		err := runDecisionAsk(decisionAskCmd, []string{"q"})
		require.Error(t, err)
	})
}

// decisionDefaultsChildEnv marks the child process of
// TestDecisionCommandFlagDefaults.
const decisionDefaultsChildEnv = "LOOM_TEST_DECISION_FLAG_DEFAULTS_CHILD"

// Review #410 M2: ask and replay bound one package variable for --decider,
// so init order (by file name) made replay default to ask's "jev". The check
// runs in a fresh process, because other tests in this package set the
// flag variables.
func TestDecisionCommandFlagDefaults(t *testing.T) {
	if os.Getenv(decisionDefaultsChildEnv) == "1" {
		tests := []struct {
			cmd  *cobra.Command
			flag string
			want string
		}{
			{decisionReplayCmd, "decider", "llm"},
			{decisionAskCmd, "decider", "jev"},
			{decisionReplayCmd, "limit", "1000"},
			{decisionReportCmd, "limit", "0"},
		}
		for _, tt := range tests {
			f := tt.cmd.Flags().Lookup(tt.flag)
			require.NotNil(t, f, "%s --%s", tt.cmd.Name(), tt.flag)
			assert.Equal(t, tt.want, f.DefValue, "%s --%s documented default", tt.cmd.Name(), tt.flag)
			assert.Equal(t, tt.want, f.Value.String(), "%s --%s resolves to its documented default with no flag", tt.cmd.Name(), tt.flag)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDecisionCommandFlagDefaults$", "-test.v")
	cmd.Env = append(os.Environ(), decisionDefaultsChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "child process:\n%s", out)
	assert.Contains(t, string(out), "--- PASS: TestDecisionCommandFlagDefaults")
}

// No two decision subcommands share a flag variable unless they also share
// the default: a shared variable takes whichever default init bound last.
func TestDecisionSubcommandFlagsDoNotShareStorage(t *testing.T) {
	type bound struct {
		cmd, def string
	}
	seen := map[uintptr]bound{}
	for _, c := range decisionCmd.Commands() {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Value == nil || reflect.ValueOf(f.Value).Kind() != reflect.Ptr {
				return
			}
			addr := reflect.ValueOf(f.Value).Pointer()
			if prev, ok := seen[addr]; ok && prev.cmd != c.Name() {
				assert.Equal(t, prev.def, f.DefValue,
					"--%s on %s and %s share one variable with different defaults", f.Name, prev.cmd, c.Name())
			}
			seen[addr] = bound{cmd: c.Name(), def: f.DefValue}
		})
	}
}
