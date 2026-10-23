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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicyCheck(t *testing.T) {
	pol := Policy{
		Deny:             []string{"shell_execute_sandbox", "agent_*"},
		DenyForShared:    []string{"http_request", "web_*"},
		Reserved:         []string{"run_script"},
		ReservedPrefixes: []string{"script_"},
	}
	tests := []struct {
		name  string
		tool  string
		trust Trust
		code  string
	}{
		{"plain tool", "execute_query", TrustInline, ""},
		{"hard deny", "contact_human", TrustOwn, CodeHardDenied},
		{"hard deny ignores allow", "tool_search", TrustInline, CodeHardDenied},
		{"reserved name", "run_script", TrustInline, CodeHardDenied},
		{"reserved prefix", "script_top_stores", TrustOwn, CodeHardDenied},
		{"deny exact", "shell_execute_sandbox", TrustInline, CodeToolNotVisible},
		{"deny pattern", "agent_management", TrustInline, CodeToolNotVisible},
		{"deny for shared, own script", "http_request", TrustOwn, ""},
		{"deny for shared, inline", "web_search", TrustInline, ""},
		{"deny for shared, shared script", "http_request", TrustShared, CodeToolNotVisible},
		{"deny for shared, published script", "web_browse", TrustPublished, CodeToolNotVisible},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, ok := pol.Check(tt.tool, tt.trust)
			assert.Equal(t, tt.code == "", ok)
			assert.Equal(t, tt.code, code)
		})
	}

	allow := Policy{Allow: []string{"execute_query", "file_*"}}
	_, ok := allow.Check("execute_query", TrustInline)
	assert.True(t, ok)
	_, ok = allow.Check("file_read", TrustInline)
	assert.True(t, ok)
	code, ok := allow.Check("http_request", TrustInline)
	assert.False(t, ok)
	assert.Equal(t, CodeToolNotVisible, code)
	code, _ = allow.Check("contact_human", TrustInline)
	assert.Equal(t, CodeHardDenied, code)
}

func TestPolicyVisible(t *testing.T) {
	pol := Policy{Deny: []string{"b"}, DenyForShared: []string{"c"}}
	advertised := []string{"d", "c", "b", "a", "a", "contact_human"}
	assert.Equal(t, []string{"a", "c", "d"}, pol.Visible(advertised, TrustOwn))
	assert.Equal(t, []string{"a", "d"}, pol.Visible(advertised, TrustShared))
	assert.Empty(t, pol.Visible(nil, TrustOwn))
}

func TestPolicyValidate(t *testing.T) {
	assert.NoError(t, Policy{Allow: []string{"a", "b_*"}}.Validate())
	assert.Error(t, Policy{Deny: []string{"[bad"}}.Validate())
	assert.Error(t, Policy{Allow: []string{" "}}.Validate())
	assert.Error(t, Policy{ReservedPrefixes: []string{""}}.Validate())
}

func TestHardDenyIsACopy(t *testing.T) {
	list := HardDeny()
	require.NotEmpty(t, list)
	list[0] = "mutated"
	assert.NotEqual(t, "mutated", HardDeny()[0])
	assert.Contains(t, HardDeny(), "contact_human")
}

func TestTrustString(t *testing.T) {
	assert.Equal(t, "inline", TrustInline.String())
	assert.Equal(t, "own", TrustOwn.String())
	assert.Equal(t, "shared", TrustShared.String())
	assert.Equal(t, "published", TrustPublished.String())
	assert.Equal(t, "trust(9)", Trust(9).String())
}

func TestLimitsNormalize(t *testing.T) {
	d, c := DefaultLimits(), MaxLimits()
	assert.Equal(t, d, Limits{}.Normalize(), "zero fields take the defaults")
	assert.Equal(t, c, Limits{
		Wall: time.Hour, CPUTicks: 1 << 62, MemoryBytes: 1 << 40, MaxToolCalls: 1 << 30,
		ToolCallTimeout: time.Hour, MaxCallResultBytes: 1 << 40, MaxOutputBytes: 1 << 40,
		MaxResultBytes: 1 << 40, MaxSourceBytes: 1 << 40, MaxSleep: time.Hour,
	}.Normalize(), "fields are capped at the ceilings")
	neg := Limits{Wall: -1, MaxToolCalls: -5}.Normalize()
	assert.Equal(t, d.Wall, neg.Wall)
	assert.Equal(t, d.MaxToolCalls, neg.MaxToolCalls)
}

func TestLimitsClamp(t *testing.T) {
	base := DefaultLimits()
	got := base.Clamp(Limits{Wall: 5 * time.Second, MaxToolCalls: 1000})
	assert.Equal(t, 5*time.Second, got.Wall, "a request may lower a limit")
	assert.Equal(t, base.MaxToolCalls, got.MaxToolCalls, "a request may not raise a limit")
	assert.Equal(t, base.MemoryBytes, got.MemoryBytes, "unset fields keep the base")
	assert.Equal(t, MaxLimits().Wall, Limits{Wall: time.Hour}.Clamp(Limits{}).Wall, "the base is normalized first")
}
