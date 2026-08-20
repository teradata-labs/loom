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
package agent

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAttachMCPServerInstructions(t *testing.T) {
	tests := []struct {
		name    string
		attach  [][2]string // (server, instructions) pairs, in order
		want    []string    // substrings the supplement must contain
		wantNot []string    // substrings it must not contain
		empty   bool        // supplement must be empty
	}{
		{
			name:   "single server renders header and body",
			attach: [][2]string{{"teradata", "Load syntax before writing native SQL."}},
			want: []string{
				`## Instructions from MCP server "teradata"`,
				"Load syntax before writing native SQL.",
			},
		},
		{
			name: "multiple servers render in sorted order",
			attach: [][2]string{
				{"zeta", "zeta guidance"},
				{"alpha", "alpha guidance"},
			},
			want: []string{"alpha guidance", "zeta guidance"},
		},
		{
			name:   "empty instructions are a no-op",
			attach: [][2]string{{"teradata", "   \n"}},
			empty:  true,
		},
		{
			name:   "empty server name is a no-op",
			attach: [][2]string{{"", "guidance"}},
			empty:  true,
		},
		{
			name: "set-once: second attach for same server ignored",
			attach: [][2]string{
				{"teradata", "original guidance"},
				{"teradata", "replacement guidance"},
			},
			want:    []string{"original guidance"},
			wantNot: []string{"replacement guidance"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Agent{}
			for _, pair := range tt.attach {
				a.attachMCPServerInstructions(pair[0], pair[1])
			}

			got := a.mcpInstructionsPromptSupplement()
			if tt.empty {
				assert.Empty(t, got)
				return
			}
			for _, want := range tt.want {
				assert.Contains(t, got, want)
			}
			for _, not := range tt.wantNot {
				assert.NotContains(t, got, not)
			}
		})
	}
}

func TestMCPInstructionsSupplementSortedAndStable(t *testing.T) {
	a := &Agent{}
	a.attachMCPServerInstructions("zeta", "z-body")
	a.attachMCPServerInstructions("alpha", "a-body")
	a.attachMCPServerInstructions("mid", "m-body")

	got := a.mcpInstructionsPromptSupplement()
	iAlpha := strings.Index(got, `"alpha"`)
	iMid := strings.Index(got, `"mid"`)
	iZeta := strings.Index(got, `"zeta"`)
	assert.True(t, iAlpha >= 0 && iMid > iAlpha && iZeta > iMid,
		"servers must render in sorted order, got: %s", got)

	// Byte-stable across calls for a fixed server set (ROM stability).
	assert.Equal(t, got, a.mcpInstructionsPromptSupplement())
}

func TestAttachMCPServerInstructionsConcurrent(t *testing.T) {
	a := &Agent{}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			a.attachMCPServerInstructions("teradata", "guidance")
		}()
		go func() {
			defer wg.Done()
			_ = a.mcpInstructionsPromptSupplement()
		}()
	}
	wg.Wait()

	assert.Contains(t, a.mcpInstructionsPromptSupplement(), "guidance")
}
