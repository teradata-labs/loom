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
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// refreshLeeway is how far before expiry the token source proactively refreshes.
const refreshLeeway = 60 * time.Second

// LoginConfig configures the PKCE OAuth login flow.
type LoginConfig struct {
	URL         string               // https://<ref>.supabase.co
	ProjectRef  string               // project reference (stored on the session)
	AnonKey     string               // public anon key (sent as apikey)
	Provider    string               // OAuth provider: github, google, ...
	OpenBrowser func(string) error   // defaults to browser.OpenURL
	Logf        func(string, ...any) // optional progress logger
	HTTPClient  *http.Client         // defaults to http.DefaultClient
}

// tokenResponse is the subset of the Supabase /token response we use.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	User         struct {
		Email string `json:"email"`
	} `json:"user"`
}

// generatePKCE returns a PKCE code_verifier and its S256 code_challenge.
func generatePKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Login runs the Supabase Auth PKCE authorization-code flow: it starts a
// loopback callback server, opens the browser to the provider, captures the
// returned code, and exchanges it for tokens. The returned Session is not
// persisted; the caller decides where to store it.
func Login(ctx context.Context, cfg LoginConfig) (*Session, error) {
	if cfg.URL == "" || cfg.AnonKey == "" {
		return nil, fmt.Errorf("supabase url and anon key are required")
	}
	if cfg.Provider == "" {
		cfg.Provider = "github"
	}
	if cfg.OpenBrowser == nil {
		return nil, fmt.Errorf("OpenBrowser is required")
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, fmt.Errorf("generate PKCE: %w", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start callback listener: %w", err)
	}
	redirectURI := fmt.Sprintf("http://%s/callback", ln.Addr().String())

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{Handler: callbackHandler(codeCh, errCh), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	// No client `state` on the authorize URL: GoTrue forwards it verbatim as the
	// provider state, and then its own /auth/v1/callback rejects the flow with
	// bad_oauth_state because it expects its self-signed state JWT there.
	// (Live-verified against Supabase; GoTrue also does not echo any state back
	// to redirect_to.) Cross-flow code injection is instead prevented by PKCE:
	// the token exchange fails unless the code was minted for our
	// code_challenge (RFC 7636) — the protection RFC 8252 prescribes for
	// native/loopback apps.
	authURL := fmt.Sprintf("%s/auth/v1/authorize?provider=%s&redirect_to=%s&code_challenge=%s&code_challenge_method=S256",
		cfg.URL, url.QueryEscape(cfg.Provider), url.QueryEscape(redirectURI), challenge)
	logf("Opening browser to sign in via %s...\nIf it does not open, visit:\n%s\n", cfg.Provider, authURL)
	if err := cfg.OpenBrowser(authURL); err != nil {
		logf("(could not open browser automatically: %v)\n", err)
	}

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, fmt.Errorf("login timed out or was cancelled: %w", ctx.Err())
	}

	tok, err := exchangeCode(ctx, cfg.HTTPClient, cfg.URL, cfg.AnonKey, code, verifier)
	if err != nil {
		return nil, err
	}
	return sessionFromToken(tok, cfg.URL, cfg.ProjectRef, cfg.AnonKey), nil
}

// callbackHandler captures the PKCE code from the OAuth redirect. There is no
// state to validate (see the authorize-URL comment in Login): GoTrue does not
// support a client state roundtrip, so an injected code is instead neutralized
// by the PKCE token exchange, which requires our code_verifier.
func callbackHandler(codeCh chan<- string, errCh chan<- error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			writeBrowserMessage(w, "Login failed: "+e)
			errCh <- fmt.Errorf("authorization error: %s: %s", e, q.Get("error_description"))
			return
		}
		code := q.Get("code")
		if code == "" {
			writeBrowserMessage(w, "Login failed: no authorization code")
			errCh <- fmt.Errorf("no authorization code in callback")
			return
		}
		writeBrowserMessage(w, "Login complete. You can close this window and return to the terminal.")
		codeCh <- code
	})
	return mux
}

// writeBrowserMessage renders a status message as a minimal HTML page on the
// loopback OAuth callback. msg can embed values from the redirect query string
// (e.g. the provider's error code), so it is HTML-escaped to prevent reflected
// XSS — any local process can hit the callback port while login is pending.
func writeBrowserMessage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, "<!doctype html><html><body style=\"font-family:sans-serif;padding:2rem\"><p>%s</p></body></html>", html.EscapeString(msg))
}

// exchangeCode swaps a PKCE auth code for tokens at the Supabase token endpoint.
func exchangeCode(ctx context.Context, hc *http.Client, baseURL, anonKey, code, verifier string) (*tokenResponse, error) {
	return postToken(ctx, hc, baseURL, anonKey, "pkce", map[string]string{
		"auth_code":     code,
		"code_verifier": verifier,
	})
}

// refreshToken exchanges a refresh token for a fresh access token.
func refreshToken(ctx context.Context, hc *http.Client, baseURL, anonKey, refresh string) (*tokenResponse, error) {
	return postToken(ctx, hc, baseURL, anonKey, "refresh_token", map[string]string{
		"refresh_token": refresh,
	})
}

func postToken(ctx context.Context, hc *http.Client, baseURL, anonKey, grantType string, body map[string]string) (*tokenResponse, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/auth/v1/token?grant_type=%s", baseURL, grantType)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", anonKey)

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(data))
	}
	var tr tokenResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}
	return &tr, nil
}

func sessionFromToken(tok *tokenResponse, baseURL, projectRef, anonKey string) *Session {
	expiresAt := tok.ExpiresAt
	if expiresAt == 0 && tok.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix()
	}
	return &Session{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    expiresAt,
		ProjectRef:   projectRef,
		URL:          baseURL,
		AnonKey:      anonKey,
		Email:        tok.User.Email,
	}
}

// TokenSource returns the current access token, refreshing it from the stored
// refresh token when it is near expiry. It is safe for concurrent use.
type TokenSource struct {
	store *Store
	hc    *http.Client
	mu    sync.Mutex
}

// NewTokenSource builds a TokenSource backed by the given session store.
func NewTokenSource(store *Store) *TokenSource {
	return &TokenSource{store: store, hc: http.DefaultClient}
}

// Token returns a valid access token, refreshing if needed. It returns an error
// when no session is stored (the user must run `loom login`).
func (ts *TokenSource) Token() (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	sess, err := ts.store.Load()
	if err != nil {
		return "", err
	}
	if sess == nil || sess.AccessToken == "" {
		return "", fmt.Errorf("not logged in: run 'loom login'")
	}

	if sess.Expiring(refreshLeeway) && sess.RefreshToken != "" {
		tr, err := refreshToken(context.Background(), ts.hc, sess.URL, sess.AnonKey, sess.RefreshToken)
		if err == nil {
			refreshed := sessionFromToken(tr, sess.URL, sess.ProjectRef, sess.AnonKey)
			if refreshed.Email == "" {
				refreshed.Email = sess.Email
			}
			if err := ts.store.Save(refreshed); err == nil {
				sess = refreshed
			}
		}
		// On refresh failure, fall through with the existing token; the server
		// will reject it if it is actually expired.
	}
	return sess.AccessToken, nil
}
