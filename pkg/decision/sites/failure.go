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

// Package sites holds the request builders and reference mappings for each
// call site that uses the decision layer. Keeping them here, not inline at
// the call site, is what lets `loom decision replay` build the exact request
// a live agent builds and compare against the exact reference it would use.
package sites

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/fabric"
)

// SiteFailureKind is the tool-result failure classifier. It feeds the tool
// circuit breaker and the identical-failure refusal in brake-only mode.
const SiteFailureKind = "tool.failure_kind"

// Question ids on the failure-kind request.
const (
	QFailureKind = "kind"
	QRetryHelps  = "retry_same_input_helps"
)

// Failure kinds. They are the Choice option keys and the reference answers.
const (
	KindNotAFailure     = "not_a_failure"
	KindTransient       = "transient"
	KindServerSaturated = "server_saturated"
	KindAuth            = "auth"
	KindBadInput        = "bad_input"
	KindNotFound        = "not_found"
	KindOther           = "other"
)

// ReferenceSourceInferErrorType names the mechanism FailureKindReference
// maps from.
const ReferenceSourceInferErrorType = "fabric.InferErrorType"

// maxErrorTextRunes bounds the error text placed in state. Accuracy falls as
// irrelevant state grows, and error text past this is stack trace or payload.
const maxErrorTextRunes = 2000

// failureKindOptions describes each kind for the decider.
var failureKindOptions = map[string]any{
	KindNotAFailure:     "The tool did what was asked; the text is a normal result or an empty result, not an error.",
	KindTransient:       "A temporary condition (timeout, connection reset, deadlock, lock wait) that an unchanged retry might clear.",
	KindServerSaturated: "The server is refusing load: rate limit, quota or session budget exhausted, overloaded, too many connections. Retrying now makes it worse.",
	KindAuth:            "Credentials or permissions were rejected. Retrying with the same credentials cannot succeed.",
	KindBadInput:        "The input was malformed or invalid for the tool: syntax error, wrong type, missing required argument. Only a changed input can succeed.",
	KindNotFound:        "A referenced object does not exist: table, column, file, resource, id. Only a changed input can succeed.",
	KindOther:           "A failure that fits none of the above.",
}

// FailureKindRequest builds the request for one tool result. State carries
// the tool name, error code, bounded error text, and a digest of the input,
// never the input itself or the result payload: tool results can contain
// customer rows, and the classifier needs none of them.
func FailureKindRequest(tool, errorCode, errorText string, input map[string]any) (*loomv1.DecisionRequest, error) {
	kind, err := decision.Choice(
		"What kind of outcome is this tool result?",
		failureKindOptions,
	)
	if err != nil {
		return nil, err
	}
	// Worded about the failure clearing, not about a retry succeeding: on a
	// successful call "would a retry succeed?" is trivially true and split the
	// first shadow run 786/597, while the reference (no failure, nothing to
	// retry) says false. See docs/research/decision-layer-phase1-report.md §2.
	retry := decision.Noul(
		"Did this call fail in a way that calling the same tool again with the identical input would probably clear?",
		decision.WithCriteria(
			"the call failed for a temporary reason unrelated to the input or credentials, such as a timeout or a dropped connection",
			"the call succeeded, or it failed because of the input, the credentials, a missing object, or a saturated server",
		),
	)
	state := map[string]any{
		"tool":         tool,
		"error_code":   errorCode,
		"error_text":   truncateRunes(errorText, maxErrorTextRunes),
		"input_digest": InputDigest(input),
	}
	return decision.NewRequest(SiteFailureKind, state, map[string]*loomv1.DecisionQuestion{
		QFailureKind: kind,
		QRetryHelps:  retry,
	})
}

// FailureKindReference maps what Loom decides today (the Success flag plus
// fabric.InferErrorType's substring ladder) onto the failure kinds, so a
// shadow row compares like with like. It is the reference, not the truth.
func FailureKindReference(success bool, errorCode, errorText string) map[string]decision.Reference {
	kind := KindOther
	retry := "false"
	switch {
	case success:
		kind = KindNotAFailure
	default:
		switch fabric.InferErrorType(errorCode, errorText) {
		case fabric.ErrorTypeSyntax, fabric.ErrorTypeOverflow, fabric.ErrorTypeConstraint, fabric.ErrorTypeInvalidInput:
			kind = KindBadInput
		case fabric.ErrorTypePermission:
			kind = KindAuth
		case fabric.ErrorTypeColumnNotFound, fabric.ErrorTypeTableNotFound, fabric.ErrorTypeNotFound:
			kind = KindNotFound
		case fabric.ErrorTypeTimeout:
			kind = KindTransient
			retry = "true"
		case fabric.ErrorTypeSaturated:
			kind = KindServerSaturated
		default:
			if looksSaturated(errorCode, errorText) {
				kind = KindServerSaturated
			}
		}
	}
	return map[string]decision.Reference{
		QFailureKind: {Answer: kind, Source: ReferenceSourceInferErrorType},
		QRetryHelps:  {Answer: retry, Source: ReferenceSourceInferErrorType},
	}
}

// saturationMarkers are the substrings today's code would need to recognise
// a saturated server; InferErrorType has none, which is one reason the
// connect storms in the tool-calling assessment were never braked.
var saturationMarkers = []string{
	"budget_full", "rate limit", "rate_limit", "429", "too many", "overloaded",
	"capacity", "backpressure", "quota", "throttl",
}

func looksSaturated(code, text string) bool {
	lower := strings.ToLower(code + " " + text)
	for _, m := range saturationMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// InputDigest is a stable sha256 over the canonical JSON of a tool input.
// It lets two calls be recognised as identical without placing the input,
// which may carry credentials or data, in state. Nil input digests to "".
func InputDigest(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	raw, err := json.Marshal(input) // map keys are sorted by encoding/json
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}
