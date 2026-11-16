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

package llm

import "net/http"

// ResponseInspector reads gateway-specific facts from a successful response:
// the gateway's own cost for the call and routing metadata such as which
// upstream served it. A gateway-fronting provider supplies one to the inner
// HTTP client, which calls it with the response headers and either the whole
// JSON body (blocking call) or each SSE data payload (stream). It must be
// cheap on payloads that carry nothing it wants, and safe for concurrent use.
//
// It returns the zero ResponseFacts when it finds nothing.
type ResponseInspector func(header http.Header, payload []byte) ResponseFacts

// ResponseFacts is what a ResponseInspector found.
type ResponseFacts struct {
	// CostUSD is the gateway-reported cost of the call; 0 = not reported.
	CostUSD float64
	// Attributes are span/metadata attributes. Keys should carry a namespace
	// prefix (for example "gateway."); values must hold no prompt content and
	// no credential material.
	Attributes map[string]string
}

// Merge folds o into f: a reported cost replaces an earlier one, and o's
// attributes overwrite f's on the same key. A stream reports facts over
// several chunks; the last word wins.
func (f *ResponseFacts) Merge(o ResponseFacts) {
	if o.CostUSD > 0 {
		f.CostUSD = o.CostUSD
	}
	if len(o.Attributes) == 0 {
		return
	}
	if f.Attributes == nil {
		f.Attributes = make(map[string]string, len(o.Attributes))
	}
	for k, v := range o.Attributes {
		f.Attributes[k] = v
	}
}

// Apply copies the attributes into an LLMResponse metadata map, creating the
// map when needed, and returns it.
func (f ResponseFacts) Apply(md map[string]interface{}) map[string]interface{} {
	if len(f.Attributes) == 0 {
		return md
	}
	if md == nil {
		md = make(map[string]interface{}, len(f.Attributes))
	}
	for k, v := range f.Attributes {
		md[k] = v
	}
	return md
}

// MetadataCostUnpriced is the LLMResponse metadata key (and span attribute) a
// client sets to true when it could not price the call: the gateway reported
// no cost and no rate is known for the model. CostUSD is then 0, which means
// "unknown", not "free".
const MetadataCostUnpriced = "llm.cost_unpriced"

// MetadataCostEstimated is the LLMResponse metadata key (and span attribute)
// set to true when the call's CostUSD is an estimate (Usage.CostEstimated):
// no reported cost and no published rate, priced at the median rate of the
// same creator's catalog models. It is set together with MetadataCostUnpriced.
const MetadataCostEstimated = "llm.cost_estimated"
