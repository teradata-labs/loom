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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveSupabaseAuthDefaults(t *testing.T) {
	t.Run("derives jwks and issuer from project ref", func(t *testing.T) {
		c := &Config{}
		c.Server.Auth.Supabase.ProjectRef = "abcdefgh"
		deriveSupabaseAuthDefaults(c)
		assert.Equal(t, "https://abcdefgh.supabase.co/auth/v1/.well-known/jwks.json", c.Server.Auth.Supabase.JWKSURL)
		assert.Equal(t, "https://abcdefgh.supabase.co/auth/v1", c.Server.Auth.Supabase.Issuer)
	})

	t.Run("preserves explicit values", func(t *testing.T) {
		c := &Config{}
		c.Server.Auth.Supabase.ProjectRef = "abcdefgh"
		c.Server.Auth.Supabase.JWKSURL = "https://custom/jwks"
		c.Server.Auth.Supabase.Issuer = "https://custom/iss"
		deriveSupabaseAuthDefaults(c)
		assert.Equal(t, "https://custom/jwks", c.Server.Auth.Supabase.JWKSURL)
		assert.Equal(t, "https://custom/iss", c.Server.Auth.Supabase.Issuer)
	})

	t.Run("no project ref is a no-op", func(t *testing.T) {
		c := &Config{}
		deriveSupabaseAuthDefaults(c)
		assert.Empty(t, c.Server.Auth.Supabase.JWKSURL)
		assert.Empty(t, c.Server.Auth.Supabase.Issuer)
	})
}

func TestIsLoopbackHost(t *testing.T) {
	loopback := []string{"127.0.0.1", "::1", "localhost", "127.0.0.2"}
	for _, h := range loopback {
		assert.True(t, isLoopbackHost(h), "expected %q loopback", h)
	}
	notLoopback := []string{"", "0.0.0.0", "::", "192.168.1.10", "example.com"}
	for _, h := range notLoopback {
		assert.False(t, isLoopbackHost(h), "expected %q non-loopback", h)
	}
}

// authCfg builds a minimal valid auth Config for mutation in tests.
func authCfg() *Config {
	c := &Config{}
	c.Server.Host = "127.0.0.1"
	c.Server.Auth.Enabled = true
	c.Server.Auth.Provider = "supabase"
	c.Server.Auth.Mode = "required"
	c.Server.Auth.Supabase.JWTSecret = "secret"
	c.Server.Auth.Supabase.Audience = "authenticated"
	c.Server.Auth.Supabase.Issuer = "https://x.supabase.co/auth/v1"
	return c
}

func TestValidateAuth(t *testing.T) {
	t.Run("disabled passes", func(t *testing.T) {
		c := &Config{}
		require.NoError(t, c.validateAuth())
	})

	t.Run("valid loopback no-TLS passes", func(t *testing.T) {
		require.NoError(t, authCfg().validateAuth())
	})

	t.Run("unsupported provider", func(t *testing.T) {
		c := authCfg()
		c.Server.Auth.Provider = "auth0"
		assert.Error(t, c.validateAuth())
	})

	t.Run("invalid mode", func(t *testing.T) {
		c := authCfg()
		c.Server.Auth.Mode = "maybe"
		assert.Error(t, c.validateAuth())
	})

	t.Run("no verification source", func(t *testing.T) {
		c := authCfg()
		c.Server.Auth.Supabase.JWTSecret = ""
		c.Server.Auth.Supabase.JWKSURL = ""
		assert.Error(t, c.validateAuth())
	})

	t.Run("jwks-only source passes", func(t *testing.T) {
		c := authCfg()
		c.Server.Auth.Supabase.JWTSecret = ""
		c.Server.Auth.Supabase.JWKSURL = "https://x.supabase.co/auth/v1/.well-known/jwks.json"
		require.NoError(t, c.validateAuth())
	})

	t.Run("missing issuer/audience", func(t *testing.T) {
		c := authCfg()
		c.Server.Auth.Supabase.Issuer = ""
		assert.Error(t, c.validateAuth())
	})

	t.Run("non-loopback host without TLS is rejected", func(t *testing.T) {
		c := authCfg()
		c.Server.Host = "0.0.0.0"
		assert.Error(t, c.validateAuth(), "auth over plaintext on a public interface must be rejected")
	})

	t.Run("non-loopback host with TLS passes", func(t *testing.T) {
		c := authCfg()
		c.Server.Host = "0.0.0.0"
		c.Server.TLS.Enabled = true
		require.NoError(t, c.validateAuth())
	})
}

func TestBuildServerAuthConfig(t *testing.T) {
	c := authCfg()
	c.Server.Auth.Mode = "optional"
	sc := buildServerAuthConfig(c, nil)
	assert.False(t, sc.Required, "optional mode -> Required=false")
	assert.Equal(t, []byte("secret"), sc.HS256Secret)
	assert.Equal(t, "authenticated", sc.Audience)

	c.Server.Auth.Mode = "required"
	sc = buildServerAuthConfig(c, nil)
	assert.True(t, sc.Required)
}
