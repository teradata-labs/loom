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
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// HooksConfig is the `tools.hooks` block Viper unmarshals: an ordered list of
// library-policy bindings. It lives in package shuttle (not cmd/looms) so a
// library builder can construct an admission chain without importing main.
// The json tags define the wire shape an out-of-process host (e.g. an agent
// profile's hooks_config) transports. The documented JSON key set is the
// snake_case one on the tags; the historical Go-field-name casing keeps
// decoding through the normalizing UnmarshalJSON below, which folds any
// case/underscore variant of a known key onto its canonical spelling before a
// strict decode — so "StateKey" and "state_key" both land, and a key that is
// no spelling of any field still fails loudly.
type HooksConfig struct {
	Bindings []HookBinding `mapstructure:"hooks" json:"bindings"`
}

// ParseHooksConfig decodes a JSON hooks config and refuses the silent-empty
// trap: a document whose keys miss the schema fails loudly (unknown fields are
// rejected), so a mis-keyed config errors at the save door instead of building
// a chain that governs nothing. A genuinely empty document (empty input, "{}",
// "null", or an explicitly empty bindings list) parses to an empty config.
func ParseHooksConfig(raw []byte) (HooksConfig, error) {
	var cfg HooksConfig
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) || bytes.Equal(trimmed, []byte("null")) {
		return cfg, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return HooksConfig{}, fmt.Errorf("hooks config: %w", err)
	}
	return cfg, nil
}

// normalizeJSONKeys rewrites the keys of one JSON object onto their canonical
// spellings. canon maps a folded key (lowercased, underscores stripped) to the
// canonical json tag, so every historical spelling of a known field — Go field
// name, any casing, with or without underscores — decodes identically, while a
// key that folds onto no known field is refused (the strict-decode contract),
// and two spellings of the same field in one document are refused rather than
// silently letting one win.
func normalizeJSONKeys(data []byte, canon map[string]string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(obj))
	for k, v := range obj {
		folded := strings.ReplaceAll(strings.ToLower(k), "_", "")
		canonical, known := canon[folded]
		if !known {
			return nil, fmt.Errorf("json: unknown field %q", k)
		}
		if _, dup := out[canonical]; dup {
			return nil, fmt.Errorf("json: duplicate field %q", canonical)
		}
		out[canonical] = v
	}
	return out, nil
}

// configKeyCanon folds every accepted spelling of the HooksConfig list key
// onto "bindings": the canonical JSON key, its historical Go-name casing, and
// "hooks" — the key the YAML surface (`tools.hooks`) uses — as an accepted
// alias, so a config written from either document shape decodes identically
// instead of one shape silently decoding to nothing.
var configKeyCanon = map[string]string{
	"bindings": "bindings",
	"hooks":    "bindings",
}

// UnmarshalJSON decodes the config with key normalization at every level, so
// even a decode NOT routed through ParseHooksConfig is strict: a top-level key
// that is no spelling of the list key fails loudly instead of yielding a
// zero-binding config that validates clean and governs nothing.
func (c *HooksConfig) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	obj, err := normalizeJSONKeys(trimmed, configKeyCanon)
	if err != nil {
		return err
	}
	canonical, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	type configAlias HooksConfig // no methods: breaks the UnmarshalJSON recursion
	var alias configAlias
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&alias); err != nil {
		return err
	}
	*c = HooksConfig(alias)
	return nil
}

// bindingKeyCanon folds every accepted spelling of a HookBinding field onto
// its canonical json tag.
var bindingKeyCanon = map[string]string{
	"kind":        "kind",
	"scope":       "scope",
	"matcher":     "matcher",
	"statekey":    "state_key",
	"readpattern": "read_pattern",
	"stmtparam":   "stmt_param",
	"pattern":     "pattern",
	"sourcetool":  "source_tool",
	"resultpath":  "result_path",
	"name":        "name",
}

// matcherKeyCanon folds every accepted spelling of a MatcherSpec field onto
// its canonical json tag.
var matcherKeyCanon = map[string]string{
	"parampath": "param_path",
	"op":        "op",
	"value":     "value",
}

// UnmarshalJSON decodes a binding with key normalization: any case/underscore
// variant of a known field lands on its canonical field, unknown keys fail.
func (b *HookBinding) UnmarshalJSON(data []byte) error {
	obj, err := normalizeJSONKeys(data, bindingKeyCanon)
	if err != nil {
		return err
	}
	canonical, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	type bindingAlias HookBinding // no methods: breaks the UnmarshalJSON recursion
	var alias bindingAlias
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&alias); err != nil {
		return err
	}
	*b = HookBinding(alias)
	return nil
}

// UnmarshalJSON decodes a matcher spec with the same key normalization as
// HookBinding.
func (m *MatcherSpec) UnmarshalJSON(data []byte) error {
	obj, err := normalizeJSONKeys(data, matcherKeyCanon)
	if err != nil {
		return err
	}
	canonical, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	type matcherAlias MatcherSpec
	var alias matcherAlias
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&alias); err != nil {
		return err
	}
	*m = MatcherSpec(alias)
	return nil
}

// HookBinding declares one library policy purely from config: which policy
// (Kind), the tools it governs (Scope), the params it selects on (Matcher), and
// the policy-specific parameters. A binding is turned into a live Hook by
// BuildChainFromConfig; a malformed binding fails serve startup (fail-closed).
type HookBinding struct {
	// Kind selects the policy: "gated-allowlist" | "denylist" | "audit" | "ask" | "custom".
	Kind string `mapstructure:"kind" json:"kind"`
	// Scope is the tool selector: an exact tool name or a "<prefix>*" pattern,
	// with the same match semantics as tools.permissions.
	Scope string `mapstructure:"scope" json:"scope"`
	// Matcher selects calls by their params, including a dispatch tool's
	// carried/nested op addressed by path. An absent matcher selects every call
	// to the scoped tools (a scope-only binding).
	Matcher MatcherSpec `mapstructure:"matcher" json:"matcher"`
	// StateKey names the approved-set partition a gated-allowlist reads.
	StateKey string `mapstructure:"state_key" json:"state_key"`
	// ReadPattern classifies a read-only call (gated-allowlist): anchored to
	// each statement's head, and every statement of the payload must match.
	ReadPattern string `mapstructure:"read_pattern" json:"read_pattern"`
	// StmtParam names the param holding the statement whose identity is checked
	// (gated-allowlist, required); shared with the render/record side's
	// canonicalization.
	StmtParam string `mapstructure:"stmt_param" json:"stmt_param"`
	// Pattern is not a valid field: a denylist selects calls with Matcher.
	// Declared only so a config that sets it fails validation loudly instead of
	// being dropped by the decoder.
	Pattern string `mapstructure:"pattern" json:"pattern"`
	// SourceTool names the render/record tool a gated-allowlist trusts as the
	// source of approved-set entries.
	SourceTool string `mapstructure:"source_tool" json:"source_tool"`
	// ResultPath locates the rendered statements within a render Result.
	ResultPath string `mapstructure:"result_path" json:"result_path"`
	// Name is the registry key of a custom hook.
	Name string `mapstructure:"name" json:"name"`
}
