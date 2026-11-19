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
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const (
	// ProviderName is the provider string used in configuration
	// (llm.provider: vercel-ai-gateway) and returned by Client.Name.
	ProviderName = "vercel-ai-gateway"
	// ProviderAlias is accepted in configuration and normalised to
	// ProviderName at load, so limiter keys, the catalog namespace and span
	// attributes only ever see the canonical name.
	ProviderAlias = "vercel"

	// CatalogProvider is the pkg/llm/catalog namespace gateway model ids are
	// registered and priced under.
	CatalogProvider = ProviderName

	// DefaultEndpoint is the gateway's global host. Regional hosts
	// (eu.ai-gateway.vercel.sh, us.ai-gateway.vercel.sh) go in Config.Endpoint.
	DefaultEndpoint = "https://ai-gateway.vercel.sh"

	// Surface paths, appended to the endpoint.
	MessagesPath        = "/v1/messages"
	ChatCompletionsPath = "/v1/chat/completions"
	ResponsesPath       = "/v1/responses"
	ModelsPath          = "/v1/models"
	CreditsPath         = "/v1/credits"

	anthropicCreator = "anthropic"
	maxModelIDLen    = 256
)

// NormalizeProviderName maps the "vercel" alias (any case, surrounding space)
// to ProviderName and returns every other name unchanged.
func NormalizeProviderName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ProviderName, ProviderAlias:
		return ProviderName
	}
	return name
}

// IsProviderName reports whether name selects this provider (canonical name
// or alias).
func IsProviderName(name string) bool {
	return NormalizeProviderName(name) == ProviderName
}

// ValidateModelID checks a gateway model id: "creator/model", both parts
// non-empty, no whitespace or control characters. The gateway's ids are
// creator-scoped ("anthropic/claude-sonnet-4.5", "openai/gpt-5"); a bare id
// would be rejected by the gateway at request time, so it is rejected here.
func ValidateModelID(id string) error {
	if id == "" {
		return fmt.Errorf("vercel-ai-gateway: model is required (a creator/model id such as anthropic/claude-sonnet-4.5); there is no default, because a default would go stale with the gateway catalog")
	}
	if len(id) > maxModelIDLen {
		return fmt.Errorf("vercel-ai-gateway: model id is longer than %d bytes", maxModelIDLen)
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == unicode.ReplacementChar {
			return fmt.Errorf("vercel-ai-gateway: model id %q contains whitespace or control characters", id)
		}
	}
	creator, model, ok := strings.Cut(id, "/")
	if !ok || creator == "" || model == "" || strings.HasPrefix(model, "/") || strings.HasSuffix(model, "/") {
		return fmt.Errorf("vercel-ai-gateway: model id %q is not of the form creator/model", id)
	}
	return nil
}

// IsAnthropicModel reports whether a gateway model id is a Claude model, which
// this client sends on the Anthropic Messages surface so thinking signatures,
// cache_control and cache-token usage keep their native shape.
func IsAnthropicModel(id string) bool {
	creator, _, ok := strings.Cut(id, "/")
	return ok && strings.EqualFold(creator, anthropicCreator)
}

// knownSurfaceSuffixes are stripped from a configured endpoint so a pasted
// full URL ends up as the bare host the client appends paths to.
var knownSurfaceSuffixes = []string{ChatCompletionsPath, MessagesPath, ModelsPath, CreditsPath, "/v1"}

// NormalizeEndpoint turns a configured endpoint into the bare base URL the
// client appends surface paths to. Empty selects DefaultEndpoint. A trailing
// "/", "/v1" or full surface path is stripped. The URL must be https, except
// for a loopback host (tests, a local proxy): the API key travels in a
// header, and plain http would send it in clear text. Userinfo, query and
// fragment are rejected.
func NormalizeEndpoint(endpoint string) (string, error) {
	e := strings.TrimSpace(endpoint)
	if e == "" {
		return DefaultEndpoint, nil
	}
	if strings.ContainsAny(e, "?#") {
		return "", fmt.Errorf("vercel-ai-gateway: endpoint %s must not have a query or fragment", redactEndpoint(endpoint))
	}
	// Strip trailing slashes and surface paths until stable, so the result is
	// a fixed point (normalising it again changes nothing).
	for {
		before := e
		e = strings.TrimRight(e, "/")
		for _, suffix := range knownSurfaceSuffixes {
			if trimmed, ok := strings.CutSuffix(e, suffix); ok {
				e = trimmed
				break
			}
		}
		if e == before {
			break
		}
	}
	u, err := url.Parse(e)
	if err != nil {
		return "", fmt.Errorf("vercel-ai-gateway: invalid endpoint %s", redactEndpoint(endpoint))
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("vercel-ai-gateway: endpoint %s has no host", redactEndpoint(endpoint))
	}
	if u.User != nil {
		return "", fmt.Errorf("vercel-ai-gateway: endpoint must not carry credentials; use the API key setting")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", fmt.Errorf("vercel-ai-gateway: endpoint %s must not have a query or fragment", redactEndpoint(endpoint))
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", fmt.Errorf("vercel-ai-gateway: endpoint %s must use https (plain http is allowed only for a loopback host)", redactEndpoint(endpoint))
		}
	default:
		return "", fmt.Errorf("vercel-ai-gateway: endpoint %s must be an https URL", redactEndpoint(endpoint))
	}
	return u.String(), nil
}

// redactEndpoint renders a configured endpoint for an error message without
// anything that may carry a secret: userinfo, query and fragment are dropped,
// and an endpoint that does not parse is not echoed at all.
func redactEndpoint(endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "(unparseable)"
	}
	u.User = nil
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	return strconv.Quote(u.String())
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
