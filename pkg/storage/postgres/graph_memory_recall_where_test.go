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

package postgres

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/memory"
)

var placeholderRe = regexp.MustCompile(`\$(\d+)`)

// argFor returns the argument bound to the placeholder that follows the given
// clause prefix (e.g. "m.salience < $"), or fails the test if the clause is
// absent.
func argFor(t *testing.T, where string, args []interface{}, clausePrefix string) interface{} {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(clausePrefix) + `(\d+)`)
	m := re.FindStringSubmatch(where)
	require.NotNil(t, m, "clause %q missing from WHERE: %s", clausePrefix, where)
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)
	require.LessOrEqual(t, n, len(args), "placeholder $%d has no argument", n)
	return args[n-1]
}

// RecallOpts.BelowSalience is the lesson lane's band ceiling (review: Josh-Schoen
// F1). The SQLite store always honored it; the Postgres store ignored it, so the
// re-trial query answered with the best eligible lesson instead of the demoted
// one. These cases pin the clause and the placeholder numbering around it.
func TestRecallWhere_BelowSalience(t *testing.T) {
	tests := []struct {
		name        string
		opts        memory.RecallOpts
		wantCeiling bool
	}{
		{
			name: "no ceiling when BelowSalience is zero",
			opts: memory.RecallOpts{AgentID: "a", MinSalience: 0.3, Limit: 5},
		},
		{
			name:        "ceiling alone",
			opts:        memory.RecallOpts{AgentID: "a", MinSalience: 0.01, BelowSalience: 0.3, Limit: 1},
			wantCeiling: true,
		},
		{
			name: "ceiling with every later optional filter",
			opts: memory.RecallOpts{
				AgentID:       "a",
				MinSalience:   0.01,
				BelowSalience: 0.3,
				MemoryType:    memory.MemoryTypeLesson,
				EntityIDs:     []string{"e1", "e2"},
				Tags:          []string{"lesson", "schema"},
				Limit:         1,
			},
			wantCeiling: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, args, next := recallWhere(tt.opts)

			// Placeholders are $1..$len(args), each used once, in order, and
			// next is the first free one for the caller's query/LIMIT args.
			var seen []int
			for _, m := range placeholderRe.FindAllStringSubmatch(where, -1) {
				n, err := strconv.Atoi(m[1])
				require.NoError(t, err)
				seen = append(seen, n)
			}
			want := make([]int, len(args))
			for i := range want {
				want[i] = i + 1
			}
			assert.Equal(t, want, seen, "placeholders must be numbered 1..n with no gaps: %s", where)
			assert.Equal(t, len(args)+1, next)

			assert.Equal(t, tt.opts.AgentID, argFor(t, where, args, "m.agent_id = $"))
			assert.Equal(t, tt.opts.MinSalience, argFor(t, where, args, "m.salience >= $"))
			if tt.wantCeiling {
				assert.Equal(t, tt.opts.BelowSalience, argFor(t, where, args, "m.salience < $"))
			} else {
				assert.NotContains(t, where, "m.salience < ")
			}
			if tt.opts.MemoryType != "" {
				assert.Equal(t, tt.opts.MemoryType, argFor(t, where, args, "m.memory_type = $"))
			}
		})
	}
}
