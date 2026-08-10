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

import "strings"

// recorder is the sole writer of an approved set: a PostToolHook that captures
// the statements a trusted render/stage tool produced and records their call
// identities under (session, state_key). Because it writes only the output of a
// deterministic tool — which cannot emit a self-GRANT — a write with no prior
// render finds no entry and the gated-allowlist read side hard-denies it. No
// path populates the set from model narration or skill prose.
type recorder struct {
	sourceTool string              // only this tool's completions are observed
	stateKey   string              // approved-set partition the identities are recorded under
	stmtParam  string              // param name the shared Canonicalize keys a statement on
	resultPath string              // dotted path to the statement list within Result.Data
	state      ApprovedSetAccessor // build-time fallback accessor, used when req.State is nil
}

// Observe records the rendered statements when the trusted render tool completes
// successfully. Any other tool, a nil result, or a failed result is a no-op — no
// identities are written, so nothing but a genuine render populates the set.
// Each produced statement is normalized through the SAME Canonicalize the read
// side compares on, so a rendered statement matches its re-emitted
// whitespace-different form at execute time. The accessor is resolved from
// req.State first with the build-time state as fallback — the same order the
// gated read side uses — so the recorder writes the very set that read checks;
// with neither wired it is a no-op. It never gates: a Record error cannot fail a
// tool call that already completed.
func (r *recorder) Observe(req AdmissionRequest, result *Result) {
	if req.ToolName != r.sourceTool || result == nil || !result.Success {
		return
	}
	state := req.State
	if state == nil {
		state = r.state
	}
	if state == nil {
		return
	}

	stmts := extractStatements(result.Data, r.resultPath)
	if len(stmts) == 0 {
		return
	}

	// An empty canonical identity is never recorded: it would otherwise become
	// a wildcard key that admits any call whose statement is absent, non-string
	// or whitespace-only (the read side refuses to look "" up for the same
	// reason — both sides must hold or either alone is bypassable).
	ids := make([]CallIdentity, 0, len(stmts))
	for _, stmt := range stmts {
		if id := Canonicalize(r.sourceTool, map[string]interface{}{r.stmtParam: stmt}, r.stmtParam); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}

	_ = state.Record(req.Ctx, r.stateKey, ids)
}

// extractStatements walks resultPath through the render Result's data to the
// statement list. The path is the one place the design meets the render tool's
// output contract; because it is config-driven, a differently-shaped render tool
// needs no code change. An empty path takes data itself as the list. A segment
// that is absent or not a map yields no statements (record nothing rather than
// guess), so a shape mismatch fails closed instead of writing garbage identities.
func extractStatements(data interface{}, resultPath string) []string {
	node := data
	if strings.TrimSpace(resultPath) != "" {
		for _, seg := range strings.Split(resultPath, ".") {
			m, ok := node.(map[string]interface{})
			if !ok {
				return nil
			}
			node, ok = m[seg]
			if !ok {
				return nil
			}
		}
	}
	return toStringList(node)
}

// toStringList renders the located node as a list of statement strings. A list
// of statements is the expected shape; a lone string is taken as a single-item
// list. Any other shape yields nothing.
func toStringList(node interface{}) []string {
	switch v := node.(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, e := range v {
			out = append(out, valueToString(e))
		}
		return out
	case string:
		return []string{v}
	default:
		return nil
	}
}
