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
package shuttle

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// The JSON wire shape: documented snake_case keys
// decode, the historical Go-field casing keeps decoding, and a mis-keyed
// document fails loudly instead of validating as an empty (ungoverned) policy.
func TestParseHooksConfig_WireShapes(t *testing.T) {
	snake := []byte(`{"bindings":[{"kind":"ask","scope":"execute_sql",
		"matcher":{"param_path":"input.parameters.query","op":"regex","value":"(?i)^\\s*grant"}}]}`)
	cfg, err := ParseHooksConfig(snake)
	require.NoError(t, err)
	require.Len(t, cfg.Bindings, 1)
	require.Equal(t, "ask", cfg.Bindings[0].Kind)
	require.Equal(t, "input.parameters.query", cfg.Bindings[0].Matcher.ParamPath,
		"snake_case matcher keys must decode — a dropped param_path silently widens the matcher")

	legacy := []byte(`{"Bindings":[{"Kind":"ask","Scope":"execute_sql"}]}`)
	cfg, err = ParseHooksConfig(legacy)
	require.NoError(t, err)
	require.Len(t, cfg.Bindings, 1, "the historical Go-field casing keeps decoding")

	// "hooks" — the key the YAML surface documents — is an accepted alias of
	// "bindings": a doc-shaped JSON config governs instead of silently decoding
	// to nothing.
	cfg, err = ParseHooksConfig([]byte(`{"hooks":[{"kind":"denylist","scope":"x"}]}`))
	require.NoError(t, err)
	require.Len(t, cfg.Bindings, 1, "the documented YAML list key decodes as an alias")

	_, err = ParseHooksConfig([]byte(`{"rules":[{"kind":"denylist","scope":"x"}]}`))
	require.Error(t, err, "a genuinely unknown key must fail, not decode to zero bindings")

	for _, empty := range []string{"", "{}", "null", `{"bindings":[]}`} {
		cfg, err := ParseHooksConfig([]byte(empty))
		require.NoError(t, err, empty)
		require.Empty(t, cfg.Bindings, empty)
	}
}

func TestHooksConfig_LegacyGoNameCasing_Decodes(t *testing.T) {
	legacy := []byte(`{"Bindings":[{
		"Kind":"gated-allowlist","Scope":"execute_sql",
		"StateKey":"approved_stmts","ReadPattern":"(?i)^\\s*select",
		"StmtParam":"stmt","SourceTool":"render_sql","ResultPath":"statements",
		"Matcher":{"ParamPath":"input.parameters.query","Op":"regex","Value":"(?i)^\\s*grant"}}]}`)
	snake := []byte(`{"bindings":[{
		"kind":"gated-allowlist","scope":"execute_sql",
		"state_key":"approved_stmts","read_pattern":"(?i)^\\s*select",
		"stmt_param":"stmt","source_tool":"render_sql","result_path":"statements",
		"matcher":{"param_path":"input.parameters.query","op":"regex","value":"(?i)^\\s*grant"}}]}`)

	fromLegacy, err := ParseHooksConfig(legacy)
	require.NoError(t, err, "the historical Go-name casing is the only spelling that ever worked on the wire — it must keep decoding")
	fromSnake, err := ParseHooksConfig(snake)
	require.NoError(t, err)
	require.Equal(t, fromSnake, fromLegacy,
		"both spellings must decode to the identical config — six multi-word fields and the matcher included")
	b := fromLegacy.Bindings[0]
	require.Equal(t, "approved_stmts", b.StateKey)
	require.Equal(t, "stmt", b.StmtParam)
	require.Equal(t, "render_sql", b.SourceTool)
	require.Equal(t, "statements", b.ResultPath)
	require.Equal(t, "input.parameters.query", b.Matcher.ParamPath)

	_, err = ParseHooksConfig([]byte(`{"bindings":[{"kind":"ask","scope":"x","statkey":"oops"}]}`))
	require.Error(t, err, "a key that is no spelling of any field must still fail loudly")
}

func TestHooksConfig_PlainUnmarshal_IsStrict(t *testing.T) {
	var cfg HooksConfig
	err := json.Unmarshal([]byte(`{"rules":[{"kind":"ask","scope":"execute_sql"}]}`), &cfg)
	require.Error(t, err,
		"a mis-keyed top-level key must fail even via plain json.Unmarshal")

	// The doc-shaped {"hooks":[…]} decodes as the documented alias and GOVERNS,
	// instead of silently decoding to zero bindings.
	cfg = HooksConfig{}
	require.NoError(t, json.Unmarshal([]byte(`{"hooks":[{"kind":"ask","scope":"execute_sql"}]}`), &cfg))
	require.Len(t, cfg.Bindings, 1)
	require.NoError(t, ValidateHooksConfig(cfg))

	// Binding-level mis-keys are strict via plain Unmarshal too.
	cfg = HooksConfig{}
	err = json.Unmarshal([]byte(`{"bindings":[{"kind":"ask","scope":"x","bogus":"y"}]}`), &cfg)
	require.Error(t, err)
}
