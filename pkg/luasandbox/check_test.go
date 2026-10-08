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

package luasandbox

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheck(t *testing.T) {
	small := Limits{MaxSourceBytes: 1 << 10}
	cases := []struct {
		name, src string
		wantErr   string // substring; empty means no error
	}{
		{"valid", `local x = args.n or 1 return x * 2`, ""},
		{"valid with tools", `local r = tools.must("q", {}) return r`, ""},
		{"syntax error has a position", "local x =\nreturn (", "report:"},
		{"too large", strings.Repeat("-- comment\n", 200), "the limit is 1024"},
		{"too deep", strings.Repeat("(", 300) + "1" + strings.Repeat(")", 300), "nest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Check("report", tc.src, small)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// Check never runs the script: an infinite loop compiles instantly.
func TestCheckDoesNotExecute(t *testing.T) {
	require.NoError(t, Check("", `while true do end`, Limits{}))
	require.NoError(t, Check("", `error("never raised")`, Limits{}))
}
