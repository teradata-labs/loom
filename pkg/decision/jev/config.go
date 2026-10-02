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

package jev

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// Credential sources, in the order FromDecisionConfig consults them.
const (
	// EnvTypeSafeAPIKey is TypeSafe's own key for api.typesafe.ai.
	EnvTypeSafeAPIKey = "TYPESAFE_API_KEY" // #nosec G101 -- env var name, not a credential
	// EnvAIGatewayAPIKey is a Vercel AI Gateway key. When it is the only
	// key present and no base URL is configured, the gateway URL is used.
	EnvAIGatewayAPIKey = "AI_GATEWAY_API_KEY" // #nosec G101 -- env var name, not a credential
	// EnvJevAPIKey is an alias some community tooling uses.
	EnvJevAPIKey = "JEV_API_KEY" // #nosec G101 -- env var name, not a credential
	// EnvBaseURL sets the endpoint when the server configuration does not.
	// It is operator-level like the server configuration: agent and judge
	// configs never choose the endpoint.
	EnvBaseURL = "TYPESAFE_BASE_URL"
)

// VercelGatewayBaseURL is the TypeSafe-compatible base the Vercel AI Gateway
// exposes; the same DefaultPath applies under it.
const VercelGatewayBaseURL = "https://ai-gateway.vercel.sh/typesafe"

// VercelGatewayModel is the only model id the gateway accepts for Jev. It is
// a floating alias: the gateway does not expose pinned releases, and its
// responses echo this id rather than a version. Config validation therefore
// requires allow_alias for it, so a deployment states that it accepted
// floating behaviour.
const VercelGatewayModel = "typesafe-ai/jev"

// gatewayModelPrefix is how gateway model ids are recognised.
const gatewayModelPrefix = "typesafe-ai/"

// ErrNoCredentials is returned when no key is configured anywhere.
var ErrNoCredentials = errors.New("jev: no API key: set " + EnvTypeSafeAPIKey + " (TypeSafe) or " + EnvAIGatewayAPIKey + " (Vercel AI Gateway)")

// ErrGatewayModel is returned when a pinned TypeSafe model id is configured
// against the gateway, which would be rejected there.
var ErrGatewayModel = errors.New("jev: the Vercel AI Gateway serves Jev as \"" + VercelGatewayModel + "\"; set decision.model to that (with allow_alias: true) or leave it empty")

// ErrInsecureEndpoint is returned when the resolved endpoint would send the
// decider credential over plain HTTP, or to a URL that is not a plain
// scheme://host[/path].
var ErrInsecureEndpoint = errors.New("jev: the decider endpoint must be an https URL " +
	"(plain http only to a loopback host, and only with the server-level decision.allow_insecure_http)")

// ServerEndpoint is the operator's choice of decider endpoint for this
// process: looms.yaml decision.base_url / decision.allow_insecure_http for
// the server and workflow runner, the --base-url / --allow-insecure-http
// flags for the loom CLI. Agent and judge configs cannot set it; that is
// what keeps an agent author from choosing where the server's decider key
// is sent.
type ServerEndpoint struct {
	// BaseURL overrides TYPESAFE_BASE_URL and the provider defaults.
	BaseURL string
	// AllowInsecureHTTP permits http:// to a loopback host (a local proxy
	// or a mock decider during development). Non-loopback http is refused
	// even with it set.
	AllowInsecureHTTP bool
}

// Validate checks the configured base URL, when there is one.
func (e ServerEndpoint) Validate() error {
	if strings.TrimSpace(e.BaseURL) == "" {
		return nil
	}
	return checkEndpoint(strings.TrimSpace(e.BaseURL), e.AllowInsecureHTTP)
}

var serverEndpoint struct {
	mu sync.RWMutex
	ep ServerEndpoint
}

// SetServerEndpoint installs the process's server-level endpoint settings.
// It validates them first and leaves the previous settings in place on
// error. looms serve and looms workflow run call it at startup; the loom CLI
// calls it from its flags.
func SetServerEndpoint(ep ServerEndpoint) error {
	ep.BaseURL = strings.TrimSpace(ep.BaseURL)
	if err := ep.Validate(); err != nil {
		return err
	}
	serverEndpoint.mu.Lock()
	defer serverEndpoint.mu.Unlock()
	serverEndpoint.ep = ep
	return nil
}

// CurrentServerEndpoint returns the settings SetServerEndpoint installed.
func CurrentServerEndpoint() ServerEndpoint {
	serverEndpoint.mu.RLock()
	defer serverEndpoint.mu.RUnlock()
	return serverEndpoint.ep
}

// FromDecisionConfig builds a Config from an agent's or judge's
// DecisionConfig, the server-level endpoint, and the environment. The
// DecisionConfig supplies model, timeout and rate only. Credentials come
// only from the environment and the endpoint only from server-level
// settings, so nothing an agent or judge author writes decides where the
// server's key goes. Resolution:
//
//  1. cfg.base_url must be empty (decision.ErrEndpointNotServerLevel).
//  2. Key: TYPESAFE_API_KEY, else AI_GATEWAY_API_KEY, else JEV_API_KEY.
//  3. Base URL: the server endpoint (SetServerEndpoint), else
//     TYPESAFE_BASE_URL, else the Vercel gateway when the key came from
//     AI_GATEWAY_API_KEY, else TypeSafe direct. Whatever is chosen must be
//     https (ErrInsecureEndpoint), except loopback http under the server's
//     AllowInsecureHTTP.
//  4. Model: cfg.model, else DefaultModel. Alias policy is enforced by
//     decision.ValidateConfig, not here.
func FromDecisionConfig(cfg *loomv1.DecisionConfig) (Config, error) {
	return resolveConfig(cfg, CurrentServerEndpoint(), os.Getenv)
}

// fromDecisionConfig resolves against the process's server endpoint and a
// caller-supplied environment (tests).
func fromDecisionConfig(cfg *loomv1.DecisionConfig, getenv func(string) string) (Config, error) {
	return resolveConfig(cfg, CurrentServerEndpoint(), getenv)
}

func resolveConfig(cfg *loomv1.DecisionConfig, ep ServerEndpoint, getenv func(string) string) (Config, error) {
	out := Config{}
	if cfg != nil {
		if strings.TrimSpace(cfg.BaseUrl) != "" {
			return Config{}, decision.ErrEndpointNotServerLevel
		}
		out.Model = strings.TrimSpace(cfg.Model)
		if cfg.TimeoutMs > 0 {
			out.Timeout = time.Duration(cfg.TimeoutMs) * time.Millisecond
		}
		if cfg.RequestsPerMinute > 0 {
			out.RequestsPerMinute = float64(cfg.RequestsPerMinute)
		}
	}

	fromGateway := false
	switch {
	case getenv(EnvTypeSafeAPIKey) != "":
		out.APIKey = getenv(EnvTypeSafeAPIKey)
	case getenv(EnvAIGatewayAPIKey) != "":
		out.APIKey = getenv(EnvAIGatewayAPIKey)
		fromGateway = true
	case getenv(EnvJevAPIKey) != "":
		out.APIKey = getenv(EnvJevAPIKey)
	default:
		return Config{}, ErrNoCredentials
	}

	out.BaseURL = strings.TrimSpace(ep.BaseURL)
	if out.BaseURL == "" {
		out.BaseURL = strings.TrimSpace(getenv(EnvBaseURL))
	}
	if out.BaseURL == "" && fromGateway {
		out.BaseURL = VercelGatewayBaseURL
	}
	if out.BaseURL != "" {
		if err := checkEndpoint(out.BaseURL, ep.AllowInsecureHTTP); err != nil {
			return Config{}, err
		}
	}
	if IsGatewayURL(out.BaseURL) {
		switch {
		case out.Model == "":
			out.Model = VercelGatewayModel
		case !strings.HasPrefix(out.Model, gatewayModelPrefix):
			return Config{}, ErrGatewayModel
		}
	}
	return out, nil
}

// checkEndpoint requires an absolute https URL with a host and no embedded
// credentials. Plain http passes only to a loopback host and only when
// allowInsecure is set (server-level).
func checkEndpoint(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %q: %v", ErrInsecureEndpoint, raw, err)
	}
	if u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: %q", ErrInsecureEndpoint, raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if allowInsecure && isLoopback(u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrInsecureEndpoint, raw)
}

// isLoopback reports whether host names the loopback interface.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IsGatewayURL reports whether a base URL is the Vercel AI Gateway.
func IsGatewayURL(baseURL string) bool {
	return strings.HasPrefix(strings.TrimSpace(baseURL), "https://ai-gateway.vercel.sh/")
}
