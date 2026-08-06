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

package supabaseauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratePKCE(t *testing.T) {
	v, c, err := generatePKCE()
	require.NoError(t, err)
	assert.NotEmpty(t, v)
	// challenge must be the base64url(sha256(verifier)).
	sum := sha256.Sum256([]byte(v))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), c)
	// Two calls produce different verifiers.
	v2, _, _ := generatePKCE()
	assert.NotEqual(t, v, v2)
}

func TestSessionExpiring(t *testing.T) {
	now := time.Now()
	assert.True(t, (&Session{ExpiresAt: now.Add(30 * time.Second).Unix()}).Expiring(60*time.Second))
	assert.False(t, (&Session{ExpiresAt: now.Add(10 * time.Minute).Unix()}).Expiring(60*time.Second))
	assert.False(t, (&Session{ExpiresAt: 0}).Expiring(60*time.Second), "no expiry => not expiring")
}

func TestStore_FileFallback(t *testing.T) {
	st := &Store{FilePath: filepath.Join(t.TempDir(), "auth", "session.json"), UseKeyring: false}

	// Empty store returns (nil, nil).
	got, err := st.Load()
	require.NoError(t, err)
	assert.Nil(t, got)

	sess := &Session{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 123, ProjectRef: "ref", Email: "a@b.c"}
	require.NoError(t, st.Save(sess))

	loaded, err := st.Load()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "at", loaded.AccessToken)
	assert.Equal(t, "a@b.c", loaded.Email)

	// File must be 0600.
	info, err := statFile(st.FilePath)
	require.NoError(t, err)
	assert.Equal(t, "-rw-------", info)

	require.NoError(t, st.Clear())
	cleared, err := st.Load()
	require.NoError(t, err)
	assert.Nil(t, cleared)
}

// mockSupabase returns a test server that handles the /auth/v1/token endpoint
// for both the pkce and refresh_token grants.
func mockSupabase(t *testing.T, wantGrant string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/v1/token" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		assert.Equal(t, wantGrant, r.URL.Query().Get("grant_type"))
		assert.Equal(t, "test-anon", r.Header.Get("apikey"))
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if wantGrant == "pkce" {
			assert.NotEmpty(t, body["auth_code"])
			assert.NotEmpty(t, body["code_verifier"])
		} else {
			assert.NotEmpty(t, body["refresh_token"])
		}
		resp := map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
			"user":          map[string]string{"email": "user@example.com"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestLogin_PKCEFlow(t *testing.T) {
	ts := mockSupabase(t, "pkce")
	defer ts.Close()

	// OpenBrowser simulates the provider round-trip the way real GoTrue behaves:
	// the final redirect to redirect_to carries ONLY the PKCE code — no state
	// echo. The authorize URL must not carry a client state either; GoTrue
	// forwards it verbatim to the provider and then rejects its own callback
	// with bad_oauth_state (live-verified against Supabase).
	openBrowser := func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		redirect := u.Query().Get("redirect_to")
		assert.Equal(t, "S256", u.Query().Get("code_challenge_method"))
		assert.NotEmpty(t, u.Query().Get("code_challenge"))
		assert.False(t, u.Query().Has("state"),
			"authorize URL must not carry a client state (GoTrue rejects it with bad_oauth_state)")
		go func() {
			resp, err := http.Get(redirect + "?code=test-code")
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := Login(ctx, LoginConfig{
		URL:         ts.URL,
		ProjectRef:  "ref",
		AnonKey:     "test-anon",
		Provider:    "github",
		OpenBrowser: openBrowser,
	})
	require.NoError(t, err)
	assert.Equal(t, "new-access", sess.AccessToken)
	assert.Equal(t, "new-refresh", sess.RefreshToken)
	assert.Equal(t, "user@example.com", sess.Email)
	assert.Equal(t, "ref", sess.ProjectRef)
	assert.Greater(t, sess.ExpiresAt, time.Now().Unix(), "expires_in should yield a future expiry")
}

func TestLogin_ProviderErrorRejected(t *testing.T) {
	ts := mockSupabase(t, "pkce")
	defer ts.Close()

	openBrowser := func(authURL string) error {
		u, _ := url.Parse(authURL)
		redirect := u.Query().Get("redirect_to")
		go func() {
			// Provider denial lands on the callback as ?error=... => Login fails.
			resp, err := http.Get(redirect + "?error=access_denied&error_description=user+cancelled")
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Login(ctx, LoginConfig{URL: ts.URL, AnonKey: "test-anon", OpenBrowser: openBrowser})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access_denied")
}

// TestCallbackHandlers_EscapeErrorParam is the regression test for the
// reflected-XSS finding on the loopback OAuth callbacks: the provider-supplied
// `error` query param is rendered into the browser response and must come back
// HTML-escaped. Covers both the user-login handler and the Management-OAuth
// handler, which share the writeBrowserMessage sink.
func TestCallbackHandlers_EscapeErrorParam(t *testing.T) {
	const payload = `<script>alert(1)</script>`

	handlers := map[string]http.Handler{
		"login":      callbackHandler(make(chan string, 1), make(chan error, 1)),
		"management": callbackFunc("want-state", make(chan string, 1), make(chan error, 1)),
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/callback?error="+url.QueryEscape(payload), nil)
			h.ServeHTTP(rec, req)

			body := rec.Body.String()
			assert.NotContains(t, body, payload, "error param must be HTML-escaped (reflected XSS)")
			assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;", "escaped form should still be shown to the user")
		})
	}
}

func TestTokenSource_RefreshesNearExpiry(t *testing.T) {
	ts := mockSupabase(t, "refresh_token")
	defer ts.Close()

	st := &Store{FilePath: filepath.Join(t.TempDir(), "session.json"), UseKeyring: false}
	require.NoError(t, st.Save(&Session{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(-time.Minute).Unix(), // already expired
		URL:          ts.URL,
		AnonKey:      "test-anon",
	}))

	src := NewTokenSource(st)
	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "new-access", tok, "expired token should be refreshed")

	// The refreshed session is persisted.
	loaded, _ := st.Load()
	assert.Equal(t, "new-access", loaded.AccessToken)
	assert.Equal(t, "new-refresh", loaded.RefreshToken)
}

func TestTokenSource_NotLoggedIn(t *testing.T) {
	st := &Store{FilePath: filepath.Join(t.TempDir(), "session.json"), UseKeyring: false}
	_, err := NewTokenSource(st).Token()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not logged in")
}

func statFile(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fi.Mode().String(), nil
}
