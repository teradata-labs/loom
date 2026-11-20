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
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A redirect to another host must never carry the gateway credential. Go
// strips Authorization on a cross-host redirect but keeps x-api-key, and the
// OIDC transport used to re-add the bearer token on every hop.
func TestRedirectToAnotherHostIsRefused(t *testing.T) {
	var attackerHits atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 127.0.0.1 vs localhost: a different host for the client.
		target := "http://localhost:" + attacker.URL[len("http://127.0.0.1:"):] + r.URL.Path
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	}))
	defer gw.Close()

	future := time.Now().Add(time.Hour).Unix()
	cases := []struct {
		name  string
		model string
		cfg   func(*Config)
	}{
		{"api key, messages surface", "anthropic/claude-haiku-4.5", func(c *Config) { c.APIKey = fakeKey }},
		{"api key, chat surface", "openai/gpt-5", func(c *Config) { c.APIKey = fakeKey }},
		{"oidc, chat surface", "openai/gpt-5", func(c *Config) { c.OIDC = &OIDCConfig{} }},
		{"oidc, messages surface", "anthropic/claude-haiku-4.5", func(c *Config) { c.OIDC = &OIDCConfig{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvOIDCToken, fakeJWT(future))
			attackerHits.Store(0)
			cfg := Config{Endpoint: gw.URL, Model: tc.model}
			tc.cfg(&cfg)
			c, err := NewClient(cfg)
			require.NoError(t, err)

			_, err = c.Chat(context.Background(), hi, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not the configured gateway host")
			_, err = c.ChatStream(context.Background(), hi, nil, nil)
			require.Error(t, err)
			err = c.HealthCheck(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not the configured gateway host")
			assert.Zero(t, attackerHits.Load(), "no request reaches the other host")
		})
	}
}

func TestHostPinTransport(t *testing.T) {
	ok := &countingRT{}
	pin := &hostPinTransport{scheme: "https", host: "ai-gateway.vercel.sh", base: ok}
	for _, u := range []string{"https://ai-gateway.vercel.sh/v1/messages", "HTTPS://AI-GATEWAY.VERCEL.SH/v1/models"} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		_, err := pin.RoundTrip(req)
		require.NoError(t, err, u)
	}
	for _, u := range []string{"http://ai-gateway.vercel.sh/v1/messages", "https://evil.example/v1/messages", "https://ai-gateway.vercel.sh:8443/x"} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		_, err := pin.RoundTrip(req)
		require.Error(t, err, u)
	}
	assert.Equal(t, int32(2), ok.n.Load())
}

type countingRT struct{ n atomic.Int32 }

func (c *countingRT) RoundTrip(*http.Request) (*http.Response, error) {
	c.n.Add(1)
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
}

func TestEndpointErrorsRedactSecrets(t *testing.T) {
	for _, in := range []string{
		"https://user:s3cr3t@ai-gateway.vercel.sh",
		"https://ai-gateway.vercel.sh?key=s3cr3t",
		"http://gw.example.com/?token=s3cr3t",
		"ftp://x/#s3cr3t",
	} {
		_, err := NormalizeEndpoint(in)
		require.Error(t, err, in)
		assert.NotContains(t, err.Error(), "s3cr3t", in)
	}
}
