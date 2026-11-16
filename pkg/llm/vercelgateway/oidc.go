// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vercelgateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// EnvOIDCToken is the variable Vercel sets to a deployment's OIDC token.
const EnvOIDCToken = "VERCEL_OIDC_TOKEN"

// maxOIDCTokenBytes bounds a token file read; a JWT is a few KB.
const maxOIDCTokenBytes = 64 << 10

// OIDCConfig authenticates with a Vercel OIDC token instead of an API key.
//
// The token is short-lived and rotated by Vercel; nothing refreshes it for a
// Go client. So the source is read fresh on every request: TokenFile when set
// (a file another process keeps current, such as a mounted secret), otherwise
// the VERCEL_OIDC_TOKEN environment variable. A token whose JWT exp claim is
// in the past is rejected before the request is sent, with an error that
// says so, rather than letting the gateway answer 401.
type OIDCConfig struct {
	// TokenFile is a path re-read per request. Empty = VERCEL_OIDC_TOKEN.
	TokenFile string
}

// oidcSource reads the current token.
type oidcSource struct {
	tokenFile string
	now       func() time.Time
	getenv    func(string) string
	readFile  func(string) ([]byte, error)
}

func newOIDCSource(cfg OIDCConfig) (*oidcSource, error) {
	src := &oidcSource{
		tokenFile: strings.TrimSpace(cfg.TokenFile),
		now:       time.Now,
		getenv:    os.Getenv,
		readFile:  readBounded,
	}
	// Fail at construction when no token is available at all, so a
	// misconfigured server stops at startup rather than on the first turn.
	if _, err := src.token(); err != nil {
		return nil, err
	}
	return src, nil
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-configured token path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, maxOIDCTokenBytes+1)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return nil, err
	}
	if n > maxOIDCTokenBytes {
		return nil, fmt.Errorf("token file is larger than %d bytes", maxOIDCTokenBytes)
	}
	return buf[:n], nil
}

// token returns the current token, or an error naming the source. The token
// itself never appears in an error.
func (s *oidcSource) token() (string, error) {
	var tok string
	if s.tokenFile != "" {
		b, err := s.readFile(s.tokenFile)
		if err != nil {
			return "", fmt.Errorf("vercel-ai-gateway: read OIDC token file: %w", err)
		}
		tok = strings.TrimSpace(string(b))
		if tok == "" {
			return "", fmt.Errorf("vercel-ai-gateway: OIDC token file %s is empty", s.tokenFile)
		}
	} else {
		tok = strings.TrimSpace(s.getenv(EnvOIDCToken))
		if tok == "" {
			return "", fmt.Errorf("vercel-ai-gateway: OIDC is enabled but %s is not set and no token file is configured", EnvOIDCToken)
		}
	}
	if exp, ok := jwtExpiry(tok); ok && !s.now().Before(exp) {
		return "", fmt.Errorf("vercel-ai-gateway: OIDC token expired at %s; refresh it (vercel env pull) or use an API key", exp.UTC().Format(time.RFC3339))
	}
	return tok, nil
}

func (s *oidcSource) authorize(r *http.Request) error {
	tok, err := s.token()
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	// Never send two credentials: the gateway prefers x-api-key, even an
	// invalid one, over a valid bearer token.
	r.Header.Del("x-api-key")
	return nil
}

// jwtExpiry reads the exp claim of a JWT without verifying it (the gateway
// verifies; this only produces a clear local error). ok is false when the
// token is not a JWT or carries no exp.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == "" {
		return time.Time{}, false
	}
	secs, err := claims.Exp.Int64()
	if err != nil || secs <= 0 {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

// authTransport sets the OIDC bearer token on every request.
type authTransport struct {
	base      http.RoundTripper
	authorize func(*http.Request) error
}

// RoundTrip clones the request (a RoundTripper must not modify its input)
// and authorizes the clone.
func (t *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	if err := t.authorize(clone); err != nil {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, err
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}
