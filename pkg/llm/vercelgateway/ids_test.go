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

package vercelgateway

import (
	"net/url"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeProviderName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"vercel-ai-gateway", ProviderName},
		{"vercel", ProviderName},
		{" Vercel ", ProviderName},
		{"VERCEL-AI-GATEWAY", ProviderName},
		{"anthropic", "anthropic"},
		{"vercel-gateway", "vercel-gateway"},
		{"", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, NormalizeProviderName(tt.in), tt.in)
		assert.Equal(t, tt.want == ProviderName, IsProviderName(tt.in), tt.in)
	}
}

func TestValidateModelID(t *testing.T) {
	tests := []struct {
		id      string
		wantErr string
	}{
		{"anthropic/claude-sonnet-4.5", ""},
		{"openai/gpt-5", ""},
		{"meta/llama-3.1-70b/instruct", ""},
		{"", "model is required"},
		{"gpt-5", "creator/model"},
		{"/gpt-5", "creator/model"},
		{"openai/", "creator/model"},
		{"openai//gpt", "creator/model"},
		{"openai/gpt-5/", "creator/model"},
		{"openai/gpt 5", "whitespace"},
		{"openai/gpt-5\n", "whitespace"},
		{"openai/gpt\x00", "control"},
		{"openai/" + strings.Repeat("x", 300), "longer than"},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			err := ValidateModelID(tt.id)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestIsAnthropicModel(t *testing.T) {
	assert.True(t, IsAnthropicModel("anthropic/claude-sonnet-4.5"))
	assert.True(t, IsAnthropicModel("Anthropic/claude-haiku-4.5"))
	assert.False(t, IsAnthropicModel("openai/gpt-5"))
	assert.False(t, IsAnthropicModel("anthropic.claude-haiku-4-5"), "Bedrock spelling is not a gateway Claude id")
	assert.False(t, IsAnthropicModel("claude-sonnet-4-5"))
	assert.False(t, IsAnthropicModel("vertex/claude-sonnet-4.5"), "only the anthropic creator selects the Messages surface")
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		in, want, wantErr string
	}{
		{"", DefaultEndpoint, ""},
		{"  ", DefaultEndpoint, ""},
		{"https://ai-gateway.vercel.sh", DefaultEndpoint, ""},
		{"https://ai-gateway.vercel.sh/", DefaultEndpoint, ""},
		{"https://ai-gateway.vercel.sh/v1", DefaultEndpoint, ""},
		{"https://ai-gateway.vercel.sh/v1/", DefaultEndpoint, ""},
		{"https://ai-gateway.vercel.sh/v1/chat/completions", DefaultEndpoint, ""},
		{"https://ai-gateway.vercel.sh/v1/messages", DefaultEndpoint, ""},
		{"https://eu.ai-gateway.vercel.sh", "https://eu.ai-gateway.vercel.sh", ""},
		{"https://proxy.example.com/gw/v1", "https://proxy.example.com/gw", ""},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", ""},
		{"http://localhost:3000/v1", "http://localhost:3000", ""},
		{"http://[::1]:9", "http://[::1]:9", ""},
		{"http://ai-gateway.vercel.sh", "", "must use https"},
		{"ftp://ai-gateway.vercel.sh", "", "https URL"},
		{"ai-gateway.vercel.sh", "", "no host"},
		{"https://user:pass@ai-gateway.vercel.sh", "", "credentials"},
		{"https://ai-gateway.vercel.sh?x=1", "", "query"},
		{"https://ai-gateway.vercel.sh#frag", "", "query or fragment"},
		{"https://", "", "no host"},
		{"://bad", "", "invalid endpoint"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := NormalizeEndpoint(tt.in)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// FuzzValidateModelID: an accepted id always has a non-empty creator and
// model and no whitespace, and IsAnthropicModel agrees with its creator.
func FuzzValidateModelID(f *testing.F) {
	for _, s := range []string{"anthropic/claude-sonnet-4.5", "openai/gpt-5", "a/b", "/", "x", "a/b/", "é/ü", "a/ "} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, id string) {
		if ValidateModelID(id) != nil {
			return
		}
		creator, model, ok := strings.Cut(id, "/")
		require.True(t, ok)
		require.NotEmpty(t, creator)
		require.NotEmpty(t, model)
		for _, r := range id {
			require.False(t, unicode.IsSpace(r) || unicode.IsControl(r), "accepted id has whitespace/control: %q", id)
		}
		require.Equal(t, strings.EqualFold(creator, "anthropic"), IsAnthropicModel(id))
	})
}

// FuzzNormalizeEndpoint: an accepted endpoint is an absolute https URL (or
// http on loopback) with a host, no credentials, query or fragment, no
// trailing slash, and normalisation is idempotent.
func FuzzNormalizeEndpoint(f *testing.F) {
	for _, s := range []string{"", DefaultEndpoint, "https://ai-gateway.vercel.sh/v1/messages", "http://localhost:1", "https://u@h", "https://h/%2", "https://h/a/v1/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := NormalizeEndpoint(in)
		if err != nil {
			return
		}
		u, perr := url.Parse(out)
		require.NoError(t, perr)
		require.NotEmpty(t, u.Hostname())
		require.Nil(t, u.User)
		require.Empty(t, u.RawQuery)
		require.Empty(t, u.Fragment)
		require.False(t, strings.HasSuffix(out, "/"), out)
		if u.Scheme == "http" {
			require.True(t, isLoopback(u.Hostname()), out)
		} else {
			require.Equal(t, "https", u.Scheme)
		}
		again, err := NormalizeEndpoint(out)
		require.NoError(t, err)
		require.Equal(t, out, again, "normalisation must be idempotent")
	})
}
