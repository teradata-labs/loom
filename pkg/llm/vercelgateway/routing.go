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
	"regexp"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm"
)

// Routing is the typed form of the gateway's providerOptions.gateway request
// object (proto LLMGatewayRouting). Agent and server configuration never
// write raw request JSON; this type is the only way options reach the body.
type Routing struct {
	// Order lists upstream provider slugs to try first, in order.
	Order []string
	// Only restricts serving to these upstream provider slugs.
	Only []string
	// Sort ranks eligible upstreams: "cost", "ttft" or "tps". "" = default.
	Sort string
	// FallbackModels are model ids tried in order when the primary fails.
	FallbackModels []string
	// ZeroDataRetention serves only from zero-data-retention upstreams.
	ZeroDataRetention bool
	// Caching is "" (explicit cache_control only) or "auto".
	Caching string
}

// Sort values the gateway accepts.
const (
	SortCost = "cost"
	SortTTFT = "ttft"
	SortTPS  = "tps"
	// CachingAuto lets the gateway place cache breakpoints itself.
	CachingAuto = "auto"
)

// providerSlugRe matches a gateway upstream slug ("anthropic", "bedrock",
// "azure", and camelCase ones such as "vertexAnthropic", observed live in the
// gateway's own error text on 2026-11-16). The gateway publishes no canonical
// list a client can validate against, so only the shape is checked.
var providerSlugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// RoutingFromProto converts the proto message; nil in, nil out.
func RoutingFromProto(p *loomv1.LLMGatewayRouting) *Routing {
	if p == nil {
		return nil
	}
	return &Routing{
		Order:             append([]string(nil), p.GetOrder()...),
		Only:              append([]string(nil), p.GetOnly()...),
		Sort:              p.GetSort(),
		FallbackModels:    append([]string(nil), p.GetFallbackModels()...),
		ZeroDataRetention: p.GetZeroDataRetention(),
		Caching:           p.GetCaching(),
	}
}

// ToProto converts back to the proto message; nil in, nil out.
func (r *Routing) ToProto() *loomv1.LLMGatewayRouting {
	if r == nil {
		return nil
	}
	return &loomv1.LLMGatewayRouting{
		Order:             append([]string(nil), r.Order...),
		Only:              append([]string(nil), r.Only...),
		Sort:              r.Sort,
		FallbackModels:    append([]string(nil), r.FallbackModels...),
		ZeroDataRetention: r.ZeroDataRetention,
		Caching:           r.Caching,
	}
}

// IsZero reports whether r sets nothing (nil or all fields empty).
func (r *Routing) IsZero() bool {
	return r == nil || (len(r.Order) == 0 && len(r.Only) == 0 && r.Sort == "" &&
		len(r.FallbackModels) == 0 && !r.ZeroDataRetention && r.Caching == "")
}

// Validate checks every field: provider slugs are well-formed and not
// repeated, sort and caching are known values, fallback models are valid
// gateway ids. A nil Routing is valid.
func (r *Routing) Validate() error {
	if r == nil {
		return nil
	}
	if err := validateSlugs("order", r.Order); err != nil {
		return err
	}
	if err := validateSlugs("only", r.Only); err != nil {
		return err
	}
	switch r.Sort {
	case "", SortCost, SortTTFT, SortTPS:
	default:
		return fmt.Errorf("vercel-ai-gateway: routing sort %q must be one of %s, %s, %s", r.Sort, SortCost, SortTTFT, SortTPS)
	}
	switch r.Caching {
	case "", CachingAuto:
	default:
		return fmt.Errorf("vercel-ai-gateway: routing caching %q must be empty or %q", r.Caching, CachingAuto)
	}
	seen := make(map[string]struct{}, len(r.FallbackModels))
	for _, m := range r.FallbackModels {
		if err := ValidateModelID(m); err != nil {
			return fmt.Errorf("vercel-ai-gateway: routing fallback_models: %w", err)
		}
		if _, dup := seen[m]; dup {
			return fmt.Errorf("vercel-ai-gateway: routing fallback_models lists %q twice", m)
		}
		seen[m] = struct{}{}
	}
	return nil
}

func validateSlugs(field string, slugs []string) error {
	seen := make(map[string]struct{}, len(slugs))
	for _, s := range slugs {
		if !providerSlugRe.MatchString(s) {
			return fmt.Errorf("vercel-ai-gateway: routing %s entry %q is not a provider slug (letters, digits, '.', '_' or '-')", field, s)
		}
		if _, dup := seen[s]; dup {
			return fmt.Errorf("vercel-ai-gateway: routing %s lists %q twice", field, s)
		}
		seen[s] = struct{}{}
	}
	return nil
}

// ExtraBody renders r as the request member {"providerOptions": {"gateway":
// {...}}} with the gateway's camelCase keys. Unset fields are omitted, so a
// zero Routing yields nil and the request body is unchanged.
func (r *Routing) ExtraBody() llm.ExtraBody {
	if r.IsZero() {
		return nil
	}
	gw := make(map[string]any, 6)
	if len(r.Order) > 0 {
		gw["order"] = append([]string(nil), r.Order...)
	}
	if len(r.Only) > 0 {
		gw["only"] = append([]string(nil), r.Only...)
	}
	if r.Sort != "" {
		gw["sort"] = r.Sort
	}
	if len(r.FallbackModels) > 0 {
		gw["models"] = append([]string(nil), r.FallbackModels...)
	}
	if r.ZeroDataRetention {
		gw["zeroDataRetention"] = true
	}
	if r.Caching != "" {
		gw["caching"] = r.Caching
	}
	return llm.ExtraBody{"providerOptions": map[string]any{"gateway": gw}}
}
