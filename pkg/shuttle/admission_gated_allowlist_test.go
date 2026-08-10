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
	"context"
	"github.com/stretchr/testify/assert"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/session"
)

// gated-allowlist without stmt_param is a config error, and an
// empty canonical identity is never a membership key on either side.
func TestGatedAllowlist_EmptyIdentity_NeverMembership(t *testing.T) {
	require.Error(t, ValidateHooksConfig(HooksConfig{Bindings: []HookBinding{{
		Kind: "gated-allowlist", Scope: "sql_execute",
		StateKey: "k", SourceTool: "render",
	}}}), "stmt_param is required")

	// Even with a well-formed binding, a whitespace-only render must not
	// produce an admit-everything "" entry.
	acc := NewApprovedSet()
	ctx := session.WithSessionID(context.Background(), "s1")
	require.NoError(t, acc.Record(ctx, "k", []CallIdentity{""}))
	ok, err := acc.Contains(ctx, "k", "")
	require.NoError(t, err)
	assert.False(t, ok, "the empty identity is never a member")
}

// read classification is per-statement and anchored: a write
// smuggled behind a read never rides the read allowance.
func TestGatedAllowlist_ReadPattern_WholePayloadClassified(t *testing.T) {
	const fixture = `
hooks:
  - kind: gated-allowlist
    scope: sql_execute
    state_key: approved
    source_tool: render
    stmt_param: stmt
    read_pattern: "(?i)\\s*select"
`
	chain, err := BuildChainFromConfig(hooksFromYAML(t, fixture), ChainDeps{})
	require.NoError(t, err)
	tool := countingTool("sql_execute")
	exec := execFor(chain, tool)
	exec.SetApprovedSet(NewApprovedSet())
	ctx := session.WithSessionID(context.Background(), "s1")

	cases := []struct {
		stmt  string
		admit bool
	}{
		{"SELECT 1", true},
		{"SELECT 1; SELECT 2", true},
		{"SELECT 1; DROP TABLE customers", false}, // the smuggle
		{"DELETE FROM t WHERE id IN (SELECT id FROM u)", false},
		{"  select * from orders", true},
		{";", false}, // no statements at all is not read-only
	}
	for _, tc := range cases {
		res, err := exec.Execute(ctx, "sql_execute", map[string]interface{}{"stmt": tc.stmt})
		require.NoError(t, err, tc.stmt)
		assert.Equal(t, tc.admit, res.Success, "stmt %q", tc.stmt)
	}
}
