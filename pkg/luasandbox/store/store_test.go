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

package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/pkg/luasandbox"
)

func TestToolNameAndValidateName(t *testing.T) {
	assert.Equal(t, "lua_report", ToolName("report"))
	for _, ok := range []string{"abc", "top_stores", "a1_" + strings.Repeat("x", 38)} {
		assert.NoError(t, ValidateName(ok), ok)
	}
	for _, bad := range []string{"", "ab", "1abc", "Abc", "a-bc", "a bc", "../x", "a" + strings.Repeat("x", 41)} {
		assert.ErrorIs(t, ValidateName(bad), ErrInvalid, bad)
	}
}

func TestValidateScript(t *testing.T) {
	good := Script{Name: "abc", Description: "Does a thing.", Source: "return 1"}
	assert.NoError(t, ValidateScript(good, luasandbox.Limits{}))

	props := map[string]any{}
	for i := 0; i < MaxManifestParameters+1; i++ {
		props[fmt.Sprintf("p%d", i)] = map[string]any{"type": "string"}
	}
	cases := map[string]func(s *Script){
		"name":             func(s *Script) { s.Name = "X" },
		"no description":   func(s *Script) { s.Description = " " },
		"long description": func(s *Script) { s.Description = strings.Repeat("d", MaxDescriptionBytes+1) },
		"bad utf8":         func(s *Script) { s.Description = "\xff" },
		"empty source":     func(s *Script) { s.Source = "" },
		"compile error":    func(s *Script) { s.Source = "return (" },
		"source too large": func(s *Script) { s.Source = strings.Repeat("-- x\n", 20000) },
		"params type":      func(s *Script) { s.Manifest = &Manifest{Parameters: map[string]any{"type": "array"}} },
		"params properties": func(s *Script) {
			s.Manifest = &Manifest{Parameters: map[string]any{"type": "object", "properties": "x"}}
		},
		"too many params": func(s *Script) {
			s.Manifest = &Manifest{Parameters: map[string]any{"type": "object", "properties": props}}
		},
		"params too large": func(s *Script) {
			s.Manifest = &Manifest{Parameters: map[string]any{"type": "object", "description": strings.Repeat("x", MaxManifestParametersBytes)}}
		},
		"nested $ref": func(s *Script) {
			s.Manifest = &Manifest{Parameters: map[string]any{"type": "object", "anyOf": []any{map[string]any{"$ref": "#"}}}}
		},
		"too many requires": func(s *Script) { s.Manifest = &Manifest{Requires: make([]string, MaxManifestRequires+1)} },
		"blank require":     func(s *Script) { s.Manifest = &Manifest{Requires: []string{" "}} },
		"long returns":      func(s *Script) { s.Manifest = &Manifest{Returns: strings.Repeat("r", MaxManifestReturnsBytes+1)} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := good
			mutate(&s)
			assert.ErrorIs(t, ValidateScript(s, luasandbox.Limits{MaxSourceBytes: 64 << 10}), ErrInvalid)
		})
	}

	withManifest := good
	withManifest.Manifest = &Manifest{
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
		Requires:   []string{"execute_query"},
		Returns:    "rows",
	}
	assert.NoError(t, ValidateScript(withManifest, luasandbox.Limits{}))
	var nilManifest *Manifest
	assert.NoError(t, nilManifest.Validate())
}
