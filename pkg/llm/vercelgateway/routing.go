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
	"os"
	"regexp"
	"sort"
	"strings"

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

	// BYOK are per-request bring-your-own-key credentials, tried in order
	// per provider. Secret values are read from the environment by
	// ResolveExtraBody; this type only names the variables.
	BYOK []BYOKCredential
	// ProviderTimeoutsMS bounds the wait for a BYOK upstream to start
	// responding, in milliseconds, per provider slug (BYOK only).
	ProviderTimeoutsMS map[string]int
	// SessionAffinity: nil = gateway default (on); false = do not pin.
	SessionAffinity *bool
	// DisallowPromptTraining excludes upstreams that may train on prompts.
	DisallowPromptTraining bool
	// InferenceRegion limits where inference runs; nil = anywhere.
	InferenceRegion *InferenceRegion
	// Headquarters restricts upstreams by ISO 3166-1 alpha-2 country; every
	// entry prefixed "!" excludes instead.
	Headquarters []string
	// User is the end-user id for gateway usage reporting.
	User string
	// Tags are usage-reporting tags.
	Tags []string
}

// BYOKCredential is one bring-your-own-key credential (proto
// GatewayBYOKCredential). Env maps a gateway credential field to the
// environment variable holding it; Values holds non-secret literal fields.
type BYOKCredential struct {
	Provider      string
	Env           map[string]string
	Values        map[string]string
	ModelMappings map[string]string // azure only: gateway slug -> deployment model id
}

// InferenceRegion limits where the gateway runs inference.
type InferenceRegion struct {
	Scope     string // "global" or "zone"
	GeoRegion string // with Scope "zone": "us", "eu", "au", "jp"
}

// Sort values the gateway accepts.
const (
	SortCost = "cost"
	SortTTFT = "ttft"
	SortTPS  = "tps"
	// CachingAuto lets the gateway place cache breakpoints itself.
	CachingAuto = "auto"

	RegionScopeGlobal = "global"
	RegionScopeZone   = "zone"
)

// Documented limits (https://vercel.com/docs/ai-gateway, read 2026-11-19).
const (
	minProviderTimeoutMS = 1000
	maxProviderTimeoutMS = 789000
	maxUserLen           = 256
	maxTags              = 10
	maxTagLen            = 64
)

// byokField describes one credential field the gateway accepts.
type byokField struct {
	secret   bool // must come from the environment
	required bool
}

// byokFields lists, per provider slug, the credential fields the gateway's
// BYOK table documents. A dotted name is a nested object
// ("googleCredentials.privateKey" -> {"googleCredentials":{"privateKey":…}}).
var byokFields = map[string]map[string]byokField{
	"anthropic": {"apiKey": {secret: true, required: true}},
	"openai":    {"apiKey": {secret: true, required: true}},
	"azure":     {"apiKey": {secret: true, required: true}, "resourceName": {required: true}},
	"bedrock": {
		"accessKeyId":     {secret: true, required: true},
		"secretAccessKey": {secret: true, required: true},
		"region":          {},
	},
	"vertex": {
		"project":                       {required: true},
		"location":                      {required: true},
		"googleCredentials.privateKey":  {secret: true, required: true},
		"googleCredentials.clientEmail": {required: true},
	},
}

// providerSlugRe matches a gateway upstream slug ("anthropic", "bedrock",
// "azure", and camelCase ones such as "vertexAnthropic", observed live in the
// gateway's own error text on 2026-11-16). The gateway publishes no canonical
// list a client can validate against, so only the shape is checked.
var providerSlugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// envNameRe matches a portable environment variable name.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// countryRe matches an ISO 3166-1 alpha-2 code, optionally negated.
var countryRe = regexp.MustCompile(`^!?[A-Z]{2}$`)

// RoutingFromProto converts the proto message; nil in, nil out.
func RoutingFromProto(p *loomv1.LLMGatewayRouting) *Routing {
	if p == nil {
		return nil
	}
	r := &Routing{
		Order:                  append([]string(nil), p.GetOrder()...),
		Only:                   append([]string(nil), p.GetOnly()...),
		Sort:                   p.GetSort(),
		FallbackModels:         append([]string(nil), p.GetFallbackModels()...),
		ZeroDataRetention:      p.GetZeroDataRetention(),
		Caching:                p.GetCaching(),
		DisallowPromptTraining: p.GetDisallowPromptTraining(),
		Headquarters:           append([]string(nil), p.GetHeadquarters()...),
		User:                   p.GetUser(),
		Tags:                   append([]string(nil), p.GetTags()...),
	}
	for _, c := range p.GetByok() {
		r.BYOK = append(r.BYOK, BYOKCredential{
			Provider:      c.GetProvider(),
			Env:           copyMap(c.GetEnv()),
			Values:        copyMap(c.GetValues()),
			ModelMappings: copyMap(c.GetModelMappings()),
		})
	}
	if t := p.GetProviderTimeoutsMs(); len(t) > 0 {
		r.ProviderTimeoutsMS = make(map[string]int, len(t))
		for k, v := range t {
			r.ProviderTimeoutsMS[k] = int(v)
		}
	}
	if p.SessionAffinity != nil {
		v := p.GetSessionAffinity()
		r.SessionAffinity = &v
	}
	if ir := p.GetInferenceRegion(); ir != nil {
		r.InferenceRegion = &InferenceRegion{Scope: ir.GetScope(), GeoRegion: ir.GetGeoRegion()}
	}
	return r
}

// ToProto converts back to the proto message; nil in, nil out.
func (r *Routing) ToProto() *loomv1.LLMGatewayRouting {
	if r == nil {
		return nil
	}
	p := &loomv1.LLMGatewayRouting{
		Order:                  append([]string(nil), r.Order...),
		Only:                   append([]string(nil), r.Only...),
		Sort:                   r.Sort,
		FallbackModels:         append([]string(nil), r.FallbackModels...),
		ZeroDataRetention:      r.ZeroDataRetention,
		Caching:                r.Caching,
		DisallowPromptTraining: r.DisallowPromptTraining,
		Headquarters:           append([]string(nil), r.Headquarters...),
		User:                   r.User,
		Tags:                   append([]string(nil), r.Tags...),
	}
	for _, c := range r.BYOK {
		p.Byok = append(p.Byok, &loomv1.GatewayBYOKCredential{
			Provider:      c.Provider,
			Env:           copyMap(c.Env),
			Values:        copyMap(c.Values),
			ModelMappings: copyMap(c.ModelMappings),
		})
	}
	if len(r.ProviderTimeoutsMS) > 0 {
		p.ProviderTimeoutsMs = make(map[string]int32, len(r.ProviderTimeoutsMS))
		for k, v := range r.ProviderTimeoutsMS {
			p.ProviderTimeoutsMs[k] = int32(min(max(v, 0), maxProviderTimeoutMS)) // #nosec G115 -- clamped to the documented range
		}
	}
	if r.SessionAffinity != nil {
		v := *r.SessionAffinity
		p.SessionAffinity = &v
	}
	if r.InferenceRegion != nil {
		p.InferenceRegion = &loomv1.GatewayInferenceRegion{Scope: r.InferenceRegion.Scope, GeoRegion: r.InferenceRegion.GeoRegion}
	}
	return p
}

func copyMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// IsZero reports whether r sets nothing (nil or all fields empty).
func (r *Routing) IsZero() bool {
	return r == nil || (len(r.Order) == 0 && len(r.Only) == 0 && r.Sort == "" &&
		len(r.FallbackModels) == 0 && !r.ZeroDataRetention && r.Caching == "" &&
		len(r.BYOK) == 0 && len(r.ProviderTimeoutsMS) == 0 && r.SessionAffinity == nil &&
		!r.DisallowPromptTraining && r.InferenceRegion == nil && len(r.Headquarters) == 0 &&
		r.User == "" && len(r.Tags) == 0)
}

// Validate checks every field without reading the environment: provider
// slugs are well-formed and not repeated, enumerations hold known values,
// fallback models are valid gateway ids, BYOK credentials name only fields
// the gateway documents for their provider and take secrets only from
// environment variables. A nil Routing is valid.
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
	for i, c := range r.BYOK {
		if err := c.validate(); err != nil {
			return fmt.Errorf("vercel-ai-gateway: routing byok[%d]: %w", i, err)
		}
	}
	for slug, ms := range r.ProviderTimeoutsMS {
		if !providerSlugRe.MatchString(slug) {
			return fmt.Errorf("vercel-ai-gateway: routing provider_timeouts_ms key %q is not a provider slug", slug)
		}
		if ms < minProviderTimeoutMS || ms > maxProviderTimeoutMS {
			return fmt.Errorf("vercel-ai-gateway: routing provider_timeouts_ms[%s] = %d must be between %d and %d", slug, ms, minProviderTimeoutMS, maxProviderTimeoutMS)
		}
	}
	if ir := r.InferenceRegion; ir != nil {
		switch ir.Scope {
		case RegionScopeGlobal:
			if ir.GeoRegion != "" {
				return fmt.Errorf("vercel-ai-gateway: routing inference_region scope %q takes no geo_region", RegionScopeGlobal)
			}
		case RegionScopeZone:
			switch ir.GeoRegion {
			case "us", "eu", "au", "jp":
			default:
				return fmt.Errorf("vercel-ai-gateway: routing inference_region geo_region %q must be us, eu, au or jp", ir.GeoRegion)
			}
		default:
			return fmt.Errorf("vercel-ai-gateway: routing inference_region scope %q must be %q or %q", ir.Scope, RegionScopeGlobal, RegionScopeZone)
		}
	}
	if err := validateHeadquarters(r.Headquarters); err != nil {
		return err
	}
	if len(r.User) > maxUserLen {
		return fmt.Errorf("vercel-ai-gateway: routing user is %d characters; at most %d", len(r.User), maxUserLen)
	}
	if len(r.Tags) > maxTags {
		return fmt.Errorf("vercel-ai-gateway: routing lists %d tags; at most %d", len(r.Tags), maxTags)
	}
	for _, t := range r.Tags {
		if t == "" || len(t) > maxTagLen {
			return fmt.Errorf("vercel-ai-gateway: routing tag %q must be 1 to %d characters", t, maxTagLen)
		}
	}
	return nil
}

// canonicalField returns the gateway's spelling of a credential field,
// matched without regard to case: looms.yaml goes through viper, which
// lowercases map keys ("apiKey" arrives as "apikey").
func canonicalField(fields map[string]byokField, name string) (string, bool) {
	if _, ok := fields[name]; ok {
		return name, true
	}
	for f := range fields {
		if strings.EqualFold(f, name) {
			return f, true
		}
	}
	return name, false
}

// canonical returns c with field names in the gateway's spelling. Unknown
// names are kept as given, for validate to report.
func (c BYOKCredential) canonical() BYOKCredential {
	fields := byokFields[strings.ToLower(c.Provider)]
	out := BYOKCredential{Provider: strings.ToLower(c.Provider), ModelMappings: c.ModelMappings}
	if len(c.Env) > 0 {
		out.Env = make(map[string]string, len(c.Env))
		for k, v := range c.Env {
			f, _ := canonicalField(fields, k)
			out.Env[f] = v
		}
	}
	if len(c.Values) > 0 {
		out.Values = make(map[string]string, len(c.Values))
		for k, v := range c.Values {
			f, _ := canonicalField(fields, k)
			out.Values[f] = v
		}
	}
	return out
}

func (c BYOKCredential) validate() error {
	c = c.canonical()
	fields, ok := byokFields[c.Provider]
	if !ok {
		return fmt.Errorf("provider %q is not one the gateway accepts BYOK credentials for (anthropic, openai, azure, bedrock, vertex)", c.Provider)
	}
	for name, envVar := range c.Env {
		if _, known := fields[name]; !known {
			return fmt.Errorf("%s credential has no field %q", c.Provider, name)
		}
		if !envNameRe.MatchString(envVar) {
			return fmt.Errorf("%s.%s: %q is not an environment variable name", c.Provider, name, envVar)
		}
	}
	for name := range c.Values {
		f, known := fields[name]
		if !known {
			return fmt.Errorf("%s credential has no field %q", c.Provider, name)
		}
		if f.secret {
			return fmt.Errorf("%s.%s is a secret: name the environment variable that holds it under env, not values", c.Provider, name)
		}
		if _, both := c.Env[name]; both {
			return fmt.Errorf("%s.%s is set under both env and values", c.Provider, name)
		}
	}
	for name, f := range fields {
		_, inEnv := c.Env[name]
		_, inValues := c.Values[name]
		if f.required && !inEnv && !inValues {
			return fmt.Errorf("%s credential is missing required field %q", c.Provider, name)
		}
	}
	if len(c.ModelMappings) > 0 && c.Provider != "azure" {
		return fmt.Errorf("model_mappings apply to azure credentials only (got %s)", c.Provider)
	}
	return nil
}

func validateHeadquarters(codes []string) error {
	neg := 0
	for _, c := range codes {
		if !countryRe.MatchString(c) {
			return fmt.Errorf("vercel-ai-gateway: routing headquarters entry %q must be an ISO 3166-1 alpha-2 code such as \"US\" or \"!US\"", c)
		}
		if c[0] == '!' {
			neg++
		}
	}
	if neg != 0 && neg != len(codes) {
		return fmt.Errorf("vercel-ai-gateway: routing headquarters mixes included and excluded (\"!\") countries")
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

// ResolveExtraBody renders r as the request member {"providerOptions":
// {"gateway": {...}}} with the gateway's camelCase keys, reading BYOK secrets
// with getenv (os.Getenv when nil). Unset fields are omitted, so a zero
// Routing yields nil and the request body is unchanged. A BYOK variable that
// is unset or empty is an error naming the variable, never its value.
func (r *Routing) ResolveExtraBody(getenv func(string) string) (llm.ExtraBody, error) {
	if r.IsZero() {
		return nil, nil
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	gw := make(map[string]any, 8)
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
	if len(r.BYOK) > 0 {
		byok := make(map[string][]map[string]any, len(r.BYOK))
		for i, c := range r.BYOK {
			cred, err := c.resolve(getenv)
			if err != nil {
				return nil, fmt.Errorf("vercel-ai-gateway: routing byok[%d]: %w", i, err)
			}
			p := strings.ToLower(c.Provider)
			byok[p] = append(byok[p], cred)
		}
		gw["byok"] = byok
	}
	if len(r.ProviderTimeoutsMS) > 0 {
		t := make(map[string]int, len(r.ProviderTimeoutsMS))
		for k, v := range r.ProviderTimeoutsMS {
			t[k] = v
		}
		gw["providerTimeouts"] = map[string]any{"byok": t}
	}
	if r.SessionAffinity != nil {
		gw["sessionAffinity"] = *r.SessionAffinity
	}
	if r.DisallowPromptTraining {
		gw["disallowPromptTraining"] = true
	}
	if ir := r.InferenceRegion; ir != nil {
		region := map[string]any{"scope": ir.Scope}
		if ir.GeoRegion != "" {
			region["geoRegion"] = ir.GeoRegion
		}
		gw["inferenceRegion"] = region
	}
	if len(r.Headquarters) > 0 {
		gw["headquarters"] = append([]string(nil), r.Headquarters...)
	}
	if r.User != "" {
		gw["user"] = r.User
	}
	if len(r.Tags) > 0 {
		gw["tags"] = append([]string(nil), r.Tags...)
	}
	return llm.ExtraBody{"providerOptions": map[string]any{"gateway": gw}}, nil
}

// resolve builds the gateway credential object, reading secrets from the
// environment. Dotted field names become nested objects.
func (c BYOKCredential) resolve(getenv func(string) string) (map[string]any, error) {
	c = c.canonical()
	out := make(map[string]any, len(c.Env)+len(c.Values)+1)
	for field, envVar := range c.Env {
		v := strings.TrimSpace(getenv(envVar))
		if v == "" {
			return nil, fmt.Errorf("%s.%s: environment variable %s is not set", c.Provider, field, envVar)
		}
		setNested(out, field, v)
	}
	for field, v := range c.Values {
		setNested(out, field, v)
	}
	if len(c.ModelMappings) > 0 {
		slugs := make([]string, 0, len(c.ModelMappings))
		for slug := range c.ModelMappings {
			slugs = append(slugs, slug)
		}
		sort.Strings(slugs)
		mappings := make([]map[string]string, 0, len(slugs))
		for _, slug := range slugs {
			mappings = append(mappings, map[string]string{"gatewayModelSlug": slug, "customModelId": c.ModelMappings[slug]})
		}
		out["modelMappings"] = mappings
	}
	return out, nil
}

func setNested(m map[string]any, dotted, v string) {
	parent, leaf, nested := strings.Cut(dotted, ".")
	if !nested {
		m[dotted] = v
		return
	}
	child, _ := m[parent].(map[string]any)
	if child == nil {
		child = map[string]any{}
		m[parent] = child
	}
	child[leaf] = v
}
