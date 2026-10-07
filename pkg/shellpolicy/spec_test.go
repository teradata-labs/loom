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

package shellpolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuild(t *testing.T) {
	pols, err := Build([]Spec{
		{Name: "dev", Allow: []string{"make", "go"}},
		{Name: "ci", Extends: "dev", Allow: []string{"npm"}, Never: []string{"make"}},
		{Name: "lax", Extends: "ci", Allow: []string{"make"}},
	})
	require.NoError(t, err)
	assert.Contains(t, pols, "readonly")
	assert.Contains(t, pols["dev"].Programs, "make")
	assert.NotContains(t, pols["ci"].Programs, "make")
	assert.Contains(t, pols["ci"].Programs, "npm")
	assert.NotContains(t, pols["lax"].Programs, "make", "a parent's never cannot be re-allowed")
	assert.True(t, pols["lax"].Never["make"])

	for name, specs := range map[string][]Spec{
		"no name":         {{Allow: []string{"x"}}},
		"readonly":        {{Name: "readonly"}},
		"duplicate":       {{Name: "a"}, {Name: "a"}},
		"unknown extends": {{Name: "a", Extends: "b"}},
		"loop":            {{Name: "a", Extends: "b"}, {Name: "b", Extends: "a"}},
		"bad program":     {{Name: "a", Allow: []string{"/bin/x"}}},
	} {
		_, err := Build(specs)
		assert.Error(t, err, name)
	}
}

// A policy rebuilt from its flat form behaves exactly like the original.
func TestFlatSpecRoundTrip(t *testing.T) {
	pols, err := Build([]Spec{
		{Name: "dev", Allow: []string{"make"}},
		{Name: "ci", Extends: "dev", Allow: []string{"npm"}, Never: []string{"git"}},
	})
	require.NoError(t, err)
	for _, name := range []string{"readonly", "dev", "ci"} {
		orig := pols[name]
		rebuilt := FromSpec(orig.FlatSpec())
		assert.Equal(t, orig.Name, rebuilt.Name, name)
		assert.Equal(t, orig.ProgramNames(), rebuilt.ProgramNames(), name)
		assert.Equal(t, orig.Never, rebuilt.Never, name)
		assert.Equal(t, orig.Builtins, rebuilt.Builtins, name)
	}
	assert.Equal(t, Spec{Name: "readonly", Allow: []string{}, Never: []string{}}, Readonly().FlatSpec())
}
