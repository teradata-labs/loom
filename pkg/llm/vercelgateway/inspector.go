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
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/teradata-labs/loom/pkg/llm"
)

// Span/metadata attribute keys this provider reports.
const (
	AttrEndpointSurface = "gateway.endpoint_surface" // "messages" | "chat" | "responses"
	AttrGenerationID    = "gateway.generation_id"
	AttrFinalProvider   = "gateway.final_provider"
	AttrFinalModel      = "gateway.final_model"
	AttrFallbackUsed    = "gateway.fallback_used" // "true" | "false"
	AttrAttemptCount    = "gateway.attempt_count"
	// AttrCredentialType is the credential the serving attempt used: "byok"
	// or "system" (the gateway's own provider key).
	AttrCredentialType = "gateway.credential_type"
	// AttrSessionSource is where the gateway took the session-affinity key
	// from: "explicit" (a session id loom sent), "fingerprint" (derived by
	// the gateway from the prompt), or another documented source.
	AttrSessionSource = "gateway.session_source"
	// AttrAttempts lists every upstream attempt in order as
	// provider/credential/status ("anthropic/byok/400,anthropic/system/200"),
	// so a BYOK key the upstream rejected is visible even when a fallback
	// served the call.
	AttrAttempts = "gateway.attempts"
	// AttrRetried is set when loom retried the call itself after the first
	// attempt failed before any output: "stall" or "truncated".
	AttrRetried = "gateway.retried"

	SurfaceMessages  = "messages"
	SurfaceChat      = "chat"
	SurfaceResponses = "responses"

	generationIDPrefix = "gen_"
)

// gatewayPayload is the subset of a response body or stream payload the
// inspector reads. Shapes observed live on 2026-11-16
// (docs/research/vercel-ai-gateway-spike.md):
//
//   - Messages: top-level "provider_metadata.gateway" {cost, routing,
//     generationId} on the blocking body and on the stream's message_delta.
//   - Chat Completions: "usage.cost" (a number) and a top-level
//     "generationId"; routing under choices[].message.provider_metadata
//     (blocking) or choices[].delta.provider_metadata (stream).
//
// The completion "id" is the generation id (gen_…) on both surfaces. The
// camelCase providerMetadata spelling from the docs is still accepted.
type gatewayPayload struct {
	ID           string `json:"id"`
	GenerationID string `json:"generationId"`
	Model        string `json:"model"`
	Message      *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	} `json:"message"` // Anthropic message_start event
	Usage *struct {
		Cost json.RawMessage `json:"cost"`
	} `json:"usage"`
	Choices []struct {
		Message *struct {
			ProviderMetadata *providerMetadata `json:"provider_metadata"`
		} `json:"message"`
		Delta *struct {
			ProviderMetadata *providerMetadata `json:"provider_metadata"`
		} `json:"delta"`
	} `json:"choices"`
	ProviderMetadata      *providerMetadata `json:"providerMetadata"`
	ProviderMetadataSnake *providerMetadata `json:"provider_metadata"`
	// Response is the Responses API stream envelope: response.created and
	// response.completed carry the response object (model, id, usage and, on
	// completion, provider_metadata.gateway) under "response".
	Response *gatewayPayload `json:"response"`
}

// metadata returns the gateway provider metadata wherever this payload
// carries it: top level (Messages; docs spelling) or inside the first choice
// (Chat Completions).
func (p *gatewayPayload) metadata() *providerMetadata {
	if p.ProviderMetadataSnake != nil {
		return p.ProviderMetadataSnake
	}
	if p.ProviderMetadata != nil {
		return p.ProviderMetadata
	}
	for _, c := range p.Choices {
		if c.Message != nil && c.Message.ProviderMetadata != nil {
			return c.Message.ProviderMetadata
		}
		if c.Delta != nil && c.Delta.ProviderMetadata != nil {
			return c.Delta.ProviderMetadata
		}
	}
	return nil
}

type providerMetadata struct {
	Gateway *struct {
		Cost         json.RawMessage `json:"cost"`
		GenerationID string          `json:"generationId"`
		Routing      *struct {
			FinalProvider             string `json:"finalProvider"`
			ModelAttemptCount         int    `json:"modelAttemptCount"`
			TotalProviderAttemptCount int    `json:"totalProviderAttemptCount"`
			ClientSessionIDSource     string `json:"clientSessionIdSource"`
			ModelAttempts             []struct {
				ProviderAttempts []struct {
					Provider       string `json:"provider"`
					CredentialType string `json:"credentialType"`
					Success        bool   `json:"success"`
					StatusCode     int    `json:"statusCode"`
				} `json:"providerAttempts"`
			} `json:"modelAttempts"`
		} `json:"routing"`
	} `json:"gateway"`
}

// payload markers: a payload without any of them is skipped without decoding,
// which keeps the per-chunk cost of a stream at a few byte scans.
var payloadMarkers = [][]byte{[]byte(`"gen_`), []byte(`roviderMetadata"`), []byte(`provider_metadata"`), []byte(`"model"`), []byte(`"cost"`)}

// newInspector returns the ResponseInspector for a client of requestedModel.
// A served model different from the requested one means the gateway used a
// fallback model.
func newInspector(requestedModel string) llm.ResponseInspector {
	return func(_ http.Header, payload []byte) llm.ResponseFacts {
		return inspectPayload(requestedModel, payload)
	}
}

func inspectPayload(requestedModel string, payload []byte) llm.ResponseFacts {
	var facts llm.ResponseFacts
	if !hasAnyMarker(payload) {
		return facts
	}
	var p gatewayPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return facts
	}
	if p.Response != nil {
		p = *p.Response
	}
	attrs := make(map[string]string, 4)

	id, model := p.ID, p.Model
	if p.Message != nil {
		if id == "" {
			id = p.Message.ID
		}
		if model == "" {
			model = p.Message.Model
		}
	}
	if strings.HasPrefix(p.GenerationID, generationIDPrefix) {
		attrs[AttrGenerationID] = p.GenerationID
	} else if strings.HasPrefix(id, generationIDPrefix) {
		attrs[AttrGenerationID] = id
	}
	if p.Usage != nil {
		if cost, ok := parseCost(p.Usage.Cost); ok {
			facts.CostUSD = cost
		}
	}
	if model != "" {
		attrs[AttrFinalModel] = model
		attrs[AttrFallbackUsed] = strconv.FormatBool(!sameModel(model, requestedModel))
	}

	if pm := p.metadata(); pm != nil && pm.Gateway != nil {
		gw := pm.Gateway
		if gw.GenerationID != "" {
			attrs[AttrGenerationID] = gw.GenerationID
		}
		if cost, ok := parseCost(gw.Cost); ok {
			facts.CostUSD = cost
		}
		if r := gw.Routing; r != nil {
			if r.FinalProvider != "" {
				attrs[AttrFinalProvider] = r.FinalProvider
			}
			if n := r.TotalProviderAttemptCount; n > 0 {
				attrs[AttrAttemptCount] = strconv.Itoa(n)
			} else if n := r.ModelAttemptCount; n > 0 {
				attrs[AttrAttemptCount] = strconv.Itoa(n)
			}
			if r.ModelAttemptCount > 1 {
				attrs[AttrFallbackUsed] = "true"
			}
			if r.ClientSessionIDSource != "" {
				attrs[AttrSessionSource] = r.ClientSessionIDSource
			}
			var attempts []string
			for _, ma := range r.ModelAttempts {
				for _, pa := range ma.ProviderAttempts {
					if pa.Success && pa.CredentialType != "" {
						attrs[AttrCredentialType] = pa.CredentialType
					}
					if pa.Provider != "" {
						attempts = append(attempts, pa.Provider+"/"+pa.CredentialType+"/"+strconv.Itoa(pa.StatusCode))
					}
				}
			}
			if len(attempts) > 0 {
				attrs[AttrAttempts] = strings.Join(attempts, ",")
			}
		}
	}
	if len(attrs) > 0 {
		facts.Attributes = attrs
	}
	return facts
}

func hasAnyMarker(payload []byte) bool {
	for _, m := range payloadMarkers {
		if bytes.Contains(payload, m) {
			return true
		}
	}
	return false
}

// sameModel compares a served model id with the requested one. Upstreams may
// echo the id without the creator prefix ("claude-sonnet-4.5" for
// "anthropic/claude-sonnet-4.5") or as a dated snapshot of it
// ("gpt-5.5-2026-04-23" for "openai/gpt-5.5", seen live on the Responses
// stream 2026-11-19); both are the same model, not a fallback. A different
// version (gpt-5 for gpt-5.5) still differs: gpt-5-5 is not gpt-5.
func sameModel(served, requested string) bool {
	// The Responses API echoes Claude ids with dashes for dots
	// ("claude-sonnet-5-5" for "anthropic/claude-sonnet-5.5", seen live
	// 2026-11-19); dots and dashes are compared as equal.
	served, requested = strings.ReplaceAll(served, ".", "-"), strings.ReplaceAll(requested, ".", "-")
	if strings.EqualFold(served, requested) {
		return true
	}
	bare := requested
	if _, b, ok := strings.Cut(requested, "/"); ok {
		bare = b
	}
	if strings.EqualFold(served, bare) {
		return true
	}
	if _, b, ok := strings.Cut(served, "/"); ok {
		served = b
	}
	return len(served) == len(bare)+len("-2006-01-02") &&
		strings.EqualFold(served[:len(bare)], bare) && datedSuffixRe.MatchString(served[len(bare):])
}

// datedSuffixRe matches the "-YYYY-MM-DD" suffix of a dated model snapshot.
var datedSuffixRe = regexp.MustCompile(`^-\d{4}-\d{2}-\d{2}$`)

// parseCost reads the gateway's cost, documented as a decimal string
// ("0.0045405"); a JSON number is accepted too. Negative, NaN and infinite
// values are rejected so a malformed field never reaches CostUSD.
func parseCost(raw json.RawMessage) (float64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, false
	}
	var s string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, false
		}
	} else {
		s = string(raw)
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}
