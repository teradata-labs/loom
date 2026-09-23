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
	"os"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
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
	// EnvBaseURL overrides the endpoint when the config leaves it empty.
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

// FromDecisionConfig builds a Config from the agent's DecisionConfig plus the
// environment. The config supplies model, timeout and base URL; credentials
// come only from the environment, never from agent YAML. Resolution:
//
//  1. Key: TYPESAFE_API_KEY, else AI_GATEWAY_API_KEY, else JEV_API_KEY.
//  2. Base URL: cfg.base_url, else TYPESAFE_BASE_URL, else the Vercel gateway
//     when the key came from AI_GATEWAY_API_KEY, else TypeSafe direct.
//  3. Model: cfg.model, else DefaultModel. Alias policy is enforced by the
//     config loader, not here.
func FromDecisionConfig(cfg *loomv1.DecisionConfig) (Config, error) {
	return fromDecisionConfig(cfg, os.Getenv)
}

func fromDecisionConfig(cfg *loomv1.DecisionConfig, getenv func(string) string) (Config, error) {
	out := Config{}
	if cfg != nil {
		out.BaseURL = strings.TrimSpace(cfg.BaseUrl)
		out.Model = strings.TrimSpace(cfg.Model)
		if cfg.TimeoutMs > 0 {
			out.Timeout = time.Duration(cfg.TimeoutMs) * time.Millisecond
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

	if out.BaseURL == "" {
		out.BaseURL = strings.TrimSpace(getenv(EnvBaseURL))
	}
	if out.BaseURL == "" && fromGateway {
		out.BaseURL = VercelGatewayBaseURL
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

// IsGatewayURL reports whether a base URL is the Vercel AI Gateway.
func IsGatewayURL(baseURL string) bool {
	return strings.HasPrefix(strings.TrimSpace(baseURL), "https://ai-gateway.vercel.sh/")
}
