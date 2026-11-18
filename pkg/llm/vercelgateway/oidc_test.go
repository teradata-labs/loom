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
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeJWT builds an unsigned token with the given exp (0 = no exp claim).
func fakeJWT(exp int64) string {
	enc := base64.RawURLEncoding
	claims := `{"sub":"test"}`
	if exp != 0 {
		claims = fmt.Sprintf(`{"sub":"test","exp":%d}`, exp)
	}
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString([]byte(claims)) + ".sig"
}

func TestJWTExpiry(t *testing.T) {
	exp, ok := jwtExpiry(fakeJWT(1_900_000_000))
	require.True(t, ok)
	assert.Equal(t, int64(1_900_000_000), exp.Unix())
	_, ok = jwtExpiry(fakeJWT(0))
	assert.False(t, ok)
	_, ok = jwtExpiry("not-a-jwt")
	assert.False(t, ok)
	_, ok = jwtExpiry("a.!!!.c")
	assert.False(t, ok)
}

func TestOIDC_EnvTokenReadPerRequest(t *testing.T) {
	future := time.Now().Add(time.Hour).Unix()
	t.Setenv(EnvOIDCToken, fakeJWT(future))
	g := &gateway{handler: okHandler}
	srv := g.start(t)
	for _, model := range []string{"anthropic/claude-sonnet-4.5", "openai/gpt-5"} {
		t.Run(model, func(t *testing.T) {
			t.Setenv(EnvOIDCToken, fakeJWT(future))
			c, err := NewClient(Config{Endpoint: srv.URL, Model: model, OIDC: &OIDCConfig{}})
			require.NoError(t, err)

			_, err = c.Chat(context.Background(), hi, nil)
			require.NoError(t, err)
			h := g.last(t).Header
			assert.Equal(t, "Bearer "+fakeJWT(future), h.Get("Authorization"))
			assert.Empty(t, h.Get("x-api-key"), "never two credentials")

			// The token rotates; the next request uses the new value.
			rotated := fakeJWT(future + 60)
			t.Setenv(EnvOIDCToken, rotated)
			_, err = c.ChatStream(context.Background(), hi, nil, nil)
			require.NoError(t, err)
			assert.Equal(t, "Bearer "+rotated, g.last(t).Header.Get("Authorization"))

			require.NoError(t, c.HealthCheck(context.Background()) /* okHandler answers chat JSON; decode works as an object */)
			assert.Equal(t, "Bearer "+rotated, g.last(t).Header.Get("Authorization"))
		})
	}
}

func TestOIDC_TokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	future := time.Now().Add(time.Hour).Unix()
	require.NoError(t, os.WriteFile(path, []byte(fakeJWT(future)+"\n"), 0o600))
	g := &gateway{handler: okHandler}
	c, err := NewClient(Config{Endpoint: g.start(t).URL, Model: "openai/gpt-5", OIDC: &OIDCConfig{TokenFile: path}})
	require.NoError(t, err)
	_, err = c.Chat(context.Background(), hi, nil)
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+fakeJWT(future), g.last(t).Header.Get("Authorization"))

	require.NoError(t, os.WriteFile(path, []byte(fakeJWT(future+5)), 0o600))
	_, err = c.Chat(context.Background(), hi, nil)
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+fakeJWT(future+5), g.last(t).Header.Get("Authorization"))
}

func TestOIDC_Errors(t *testing.T) {
	t.Run("no token at construction", func(t *testing.T) {
		t.Setenv(EnvOIDCToken, "")
		_, err := NewClient(Config{Model: "openai/gpt-5", OIDC: &OIDCConfig{}})
		require.ErrorContains(t, err, EnvOIDCToken)
	})
	t.Run("missing file", func(t *testing.T) {
		_, err := NewClient(Config{Model: "openai/gpt-5", OIDC: &OIDCConfig{TokenFile: filepath.Join(t.TempDir(), "absent")}})
		require.ErrorContains(t, err, "token file")
	})
	t.Run("expired token fails before sending", func(t *testing.T) {
		past := time.Now().Add(-time.Minute).Unix()
		future := time.Now().Add(time.Hour).Unix()
		t.Setenv(EnvOIDCToken, fakeJWT(future))
		g := &gateway{handler: okHandler}
		c, err := NewClient(Config{Endpoint: g.start(t).URL, Model: "openai/gpt-5", OIDC: &OIDCConfig{}})
		require.NoError(t, err)
		expired := fakeJWT(past)
		t.Setenv(EnvOIDCToken, expired)
		_, err = c.Chat(context.Background(), hi, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expired")
		assert.NotContains(t, err.Error(), expired, "the token never appears in an error")
		g.mu.Lock()
		assert.Empty(t, g.requests)
		g.mu.Unlock()
	})
	t.Run("oversized file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "big")
		require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("a", maxOIDCTokenBytes+10)), 0o600))
		_, err := NewClient(Config{Model: "openai/gpt-5", OIDC: &OIDCConfig{TokenFile: path}})
		require.ErrorContains(t, err, "larger than")
	})
}

func TestOIDC_APIKeyWins(t *testing.T) {
	t.Setenv(EnvOIDCToken, fakeJWT(time.Now().Add(time.Hour).Unix()))
	g := &gateway{handler: okHandler}
	c, err := NewClient(Config{Endpoint: g.start(t).URL, Model: "openai/gpt-5", APIKey: fakeKey, OIDC: &OIDCConfig{}})
	require.NoError(t, err)
	_, err = c.Chat(context.Background(), hi, nil)
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+fakeKey, g.last(t).Header.Get("Authorization"))
}
