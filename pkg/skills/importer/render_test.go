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

//go:build fts5

package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/skills"
)

// TestRenderYAML_RoundTripsThroughLoader is the smoke test that
// guarantees what the importer renders is what skills.LoadSkill expects.
// Round-tripping is the whole reason rendering and validation are
// separate phases in the pipeline; if this test fails, server boot
// will fail too.
func TestRenderYAML_RoundTripsThroughLoader(t *testing.T) {
	imp := &Skill{
		Name:         "teradata-sql-fundamentals",
		Description:  "Teradata SQL fundamentals.",
		Author:       "teradata",
		Version:      "1.0",
		Body:         "# Body\n\n## When to Use\n\n- Writing SQL\n",
		LinkedSkills: []string{"teradata-architecture"},
		ResolvedRefs: []string{"teradata-architecture"},
	}
	bs, err := RenderYAML(imp)
	require.NoError(t, err)

	tmp := filepath.Join(t.TempDir(), "out.yaml")
	require.NoError(t, os.WriteFile(tmp, bs, 0o600))
	got, err := skills.LoadSkill(tmp)
	require.NoError(t, err)
	assert.Equal(t, "teradata-sql-fundamentals", got.Name)
	assert.Equal(t, "teradata", got.Domain)
	assert.Equal(t, []string{"teradata-architecture"}, got.SkillRefs)
}

func TestNormalizeCrossSkillLinks(t *testing.T) {
	in := "See [arch](../teradata-architecture/SKILL.md) and [stats](../teradata-statistics/SKILL.md#section)."
	out := normalizeCrossSkillLinks(in)
	assert.Contains(t, out, "[arch](skill:teradata-architecture)")
	assert.Contains(t, out, "[stats](skill:teradata-statistics)")
	assert.NotContains(t, out, "../teradata-")
}

func TestChooseDomain(t *testing.T) {
	cases := []struct {
		name string
		imp  *Skill
		want string
	}{
		{"teradata prefix", &Skill{Name: "teradata-statistics"}, "teradata"},
		{"parent index", &Skill{Name: "teradata-skill-index", IsParentIndex: true}, "meta-agent"},
		{"unknown prefix", &Skill{Name: "postgres-vacuum"}, "general"},
		{"bare name", &Skill{Name: "something"}, "general"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ChooseDomain(tc.imp))
		})
	}
}

func TestDeriveTitle(t *testing.T) {
	assert.Equal(t, "Teradata Sql Fundamentals", DeriveTitle("teradata-sql-fundamentals"))
	assert.Equal(t, "Foo", DeriveTitle("foo"))
	assert.Equal(t, "", DeriveTitle(""))
}
