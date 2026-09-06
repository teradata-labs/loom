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
package shuttle

import "context"

// AskGrant resolves Ask decisions from a decision a human (or the session
// override) already made. Installed on the context only around a parked
// batch's re-execution, or around a whole overridden turn. Blanket by design:
// the human approved or rejected the batch as one unit. A grant lifts only an
// Ask — a Deny from any hook still dominates in the chain's combination.
type AskGrant struct {
	Approved bool
	Reason   string // human's note; verbatim deny reason when !Approved
}

// Decide renders the grant as a terminal admission decision for one Ask.
func (g *AskGrant) Decide() Decision {
	if g.Approved {
		return Decision{Kind: Allow}
	}
	reason := g.Reason
	if reason == "" {
		reason = "rejected by user"
	}
	return Decision{Kind: Deny, Reason: reason}
}

type askGrantKey struct{}

// ContextWithAskGrant installs g on ctx. The chain reads it per call, so the
// caller controls the grant's scope by controlling which calls run under the
// derived context.
func ContextWithAskGrant(ctx context.Context, g *AskGrant) context.Context {
	return context.WithValue(ctx, askGrantKey{}, g)
}

// AskGrantFromContext returns the grant installed on ctx, or nil.
func AskGrantFromContext(ctx context.Context) *AskGrant {
	if g, ok := ctx.Value(askGrantKey{}).(*AskGrant); ok {
		return g
	}
	return nil
}

// SummarizeCall renders the deterministic one-line display digest for a tool
// call — the same digest the ask resolver stamps on a held call's request row.
// Exported for the park pre-scan, which builds one grouped request per batch.
func SummarizeCall(toolName string, params map[string]interface{}) string {
	return summarizeCall(toolName, params)
}

// BoundParams caps the JSON-encoded size of params at the resolver's per-call
// bound (8 KB), reporting whether whole pairs were cut. Exported for the park
// pre-scan's per-item descriptor bounding.
func BoundParams(params map[string]interface{}) (map[string]interface{}, bool) {
	return boundParams(params)
}
