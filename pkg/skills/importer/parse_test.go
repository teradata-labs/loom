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

package importer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitFrontmatter(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		raw := []byte("---\nname: foo\ndescription: bar\nmetadata:\n  author: a\n  version: \"1.0\"\n---\n\n# Body\n\nhello\n")
		fm, body, err := splitFrontmatter(raw)
		require.NoError(t, err)
		assert.Equal(t, "foo", fm.Name)
		assert.Equal(t, "bar", fm.Description)
		assert.Equal(t, "a", fm.Metadata.Author)
		assert.True(t, strings.HasPrefix(body, "# Body"))
	})
	t.Run("missing frontmatter", func(t *testing.T) {
		_, _, err := splitFrontmatter([]byte("# just a heading\n"))
		assert.Error(t, err)
	})
}

func TestExtractLinkedSkillNames(t *testing.T) {
	body := `Some text.

See [arch](../teradata-architecture/SKILL.md) and ` + "`teradata-statistics`" + ` for context.
Also [self-ref](../teradata-foo/SKILL.md#section).
`
	got := extractLinkedSkillNames(body, "teradata-foo")
	assert.Equal(t, []string{"teradata-architecture", "teradata-statistics"}, got)
}

func TestParseWhenToUseBullets(t *testing.T) {
	body := `# Title

## When to Use

- First bullet
- Second bullet
* Third bullet

## Next section

- Should not appear
`
	bullets := parseWhenToUseBullets(body)
	assert.Equal(t, []string{"First bullet", "Second bullet", "Third bullet"}, bullets)
}

func TestIsSafeSkillName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"teradata-statistics", true},
		{"teradata-sql-fundamentals", true},
		{"a", true},                  // single letter — regex permits, loader handles
		{"", false},                  // empty
		{"Teradata-Stats", false},    // uppercase rejected
		{"teradata_stats", false},    // underscore rejected
		{"-teradata", false},         // leading hyphen
		{"teradata-", false},         // trailing hyphen
		{"teradata--stats", false},   // double hyphen
		{"../etc/passwd", false},     // path traversal
		{"/abs/path", false},         // absolute path
		{"123-numeric-start", false}, // must start with letter
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.ok, IsSafeSkillName(tc.name))
		})
	}
}

func TestIsSkippedSkill(t *testing.T) {
	assert.True(t, IsSkippedSkill("agent-skill-builder"))
	assert.False(t, IsSkippedSkill("teradata-statistics"))
	assert.False(t, IsSkippedSkill(""))
}
