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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultManagementAPI is the base URL of the Supabase Management API.
const DefaultManagementAPI = "https://api.supabase.com"

// ManagementConfig configures the Supabase Management OAuth flow. The developer
// must pre-register an OAuth application in the Supabase dashboard to obtain a
// client id/secret and register the redirect URI.
type ManagementConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string // must exactly match the registered redirect URI
	APIBase      string // defaults to DefaultManagementAPI
	OpenBrowser  func(string) error
	HTTPClient   *http.Client
	Logf         func(string, ...any)
}

// Project is a Supabase project from the Management API.
type Project struct {
	ID     string `json:"id"` // the project ref
	Name   string `json:"name"`
	Region string `json:"region"`
}

// ManagementLogin runs the Management API OAuth authorization-code+PKCE flow and
// returns an access token. The redirect URI's host:port is bound locally to
// capture the code, so it must be a loopback URI registered with the OAuth app.
func ManagementLogin(ctx context.Context, cfg ManagementConfig) (string, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return "", fmt.Errorf("management OAuth client_id and client_secret are required")
	}
	if cfg.RedirectURI == "" {
		return "", fmt.Errorf("redirect URI is required (must match the registered OAuth app)")
	}
	if cfg.OpenBrowser == nil {
		return "", fmt.Errorf("OpenBrowser is required")
	}
	apiBase := cfg.APIBase
	if apiBase == "" {
		apiBase = DefaultManagementAPI
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	redirect, err := url.Parse(cfg.RedirectURI)
	if err != nil {
		return "", fmt.Errorf("parse redirect URI: %w", err)
	}

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return "", err
	}
	state, err := randomToken()
	if err != nil {
		return "", err
	}

	ln, err := net.Listen("tcp", redirect.Host)
	if err != nil {
		return "", fmt.Errorf("bind redirect host %q (is it free and loopback?): %w", redirect.Host, err)
	}
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(redirect.Path, callbackFunc(state, codeCh, errCh))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if redirect.Scheme == "https" {
		// The Supabase Management API rejects plain-HTTP redirect URIs outright
		// ("redirect_uri must use HTTPS"), including loopback, so the local
		// callback must speak TLS. Serve with an ephemeral self-signed cert;
		// the browser shows a one-time certificate warning the user clicks
		// through. The OAuth code is single-use and PKCE-bound, so the
		// untrusted cert does not weaken the flow.
		tlsCfg, certErr := ephemeralLoopbackTLS()
		if certErr != nil {
			return "", fmt.Errorf("generate loopback TLS certificate: %w", certErr)
		}
		srv.TLSConfig = tlsCfg
		go func() { _ = srv.ServeTLS(ln, "", "") }()
	} else {
		go func() { _ = srv.Serve(ln) }()
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	authURL := fmt.Sprintf("%s/v1/oauth/authorize?client_id=%s&redirect_uri=%s&response_type=code&state=%s&code_challenge=%s&code_challenge_method=S256",
		apiBase, url.QueryEscape(cfg.ClientID), url.QueryEscape(cfg.RedirectURI), state, challenge)
	logf("Opening browser to authorize with Supabase...\nIf it does not open, visit:\n%s\n", authURL)
	if err := cfg.OpenBrowser(authURL); err != nil {
		logf("(could not open browser automatically: %v)\n", err)
	}

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", fmt.Errorf("authorization timed out or was cancelled: %w", ctx.Err())
	}

	return exchangeManagementCode(ctx, cfg.HTTPClient, apiBase, cfg, code, verifier)
}

// ephemeralLoopbackTLS builds a TLS config with a freshly generated,
// self-signed ECDSA certificate valid only for loopback (127.0.0.1, ::1,
// localhost) and only for one hour. Public CAs cannot issue for loopback
// addresses, so a self-signed certificate is the only way to satisfy the
// Management API's HTTPS-redirect requirement without routing the OAuth code
// through an external tunnel.
func ephemeralLoopbackTLS() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "loom-cli loopback callback"},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}, nil
}

// callbackFunc is the OAuth redirect handler (shared shape with the user-login flow).
func callbackFunc(wantState string, codeCh chan<- string, errCh chan<- error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			writeBrowserMessage(w, "Authorization failed: "+e)
			errCh <- fmt.Errorf("authorization error: %s: %s", e, q.Get("error_description"))
			return
		}
		if q.Get("state") != wantState {
			writeBrowserMessage(w, "Authorization failed: state mismatch")
			errCh <- fmt.Errorf("state mismatch (possible CSRF)")
			return
		}
		code := q.Get("code")
		if code == "" {
			writeBrowserMessage(w, "Authorization failed: no code")
			errCh <- fmt.Errorf("no authorization code in callback")
			return
		}
		writeBrowserMessage(w, "Authorization complete. You can close this window and return to the terminal.")
		codeCh <- code
	}
}

func exchangeManagementCode(ctx context.Context, hc *http.Client, apiBase string, cfg ManagementConfig, code, verifier string) (string, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURI},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/v1/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Confidential client: HTTP Basic auth with client id/secret.
	basic := base64.StdEncoding.EncodeToString([]byte(cfg.ClientID + ":" + cfg.ClientSecret))
	req.Header.Set("Authorization", "Basic "+basic)

	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("management token endpoint returned %d: %s", resp.StatusCode, string(data))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &tr); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("management token response missing access_token")
	}
	return tr.AccessToken, nil
}

// ListProjects fetches the projects visible to the access token.
func ListProjects(ctx context.Context, hc *http.Client, apiBase, accessToken string) ([]Project, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	if apiBase == "" {
		apiBase = DefaultManagementAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/v1/projects", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("projects endpoint returned %d: %s", resp.StatusCode, string(data))
	}
	var projects []Project
	if err := json.Unmarshal(data, &projects); err != nil {
		return nil, fmt.Errorf("decode projects: %w", err)
	}
	return projects, nil
}
