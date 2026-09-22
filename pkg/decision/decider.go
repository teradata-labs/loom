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

// Package decision is the typed decision layer.
//
// A decision is a narrow semantic judgment over supplied state: a yes/no
// probability (Noul), a choice among named options (Choice), or a position on
// an ordered scale (Score). Decisions never generate text. They are answered
// by a Decider: a hosted decision model such as TypeSafe Jev, an adapter that
// asks an ordinary LLM for the same typed shape (pkg/decision/llm), or a mock
// (pkg/decision/mock).
//
// This is a sibling of pkg/llm, not part of it. A Decider is not an
// LLMProvider: it has no chat surface and belongs in no model catalog. Call
// sites reach it through a Router, which applies a per-site confidence band
// and reports which path answered so the site can fall back to whatever
// mechanism it used before the decision layer existed.
//
// Wire shapes are defined in proto/loom/v1/decision.proto.
package decision

import (
	"context"
	"errors"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Decider answers typed questions about state.
type Decider interface {
	// Decide evaluates every question in the request against its state and
	// returns one answer per question id. Implementations validate the
	// request (see Validate) and return a *ValidationError for malformed
	// input rather than sending it anywhere.
	Decide(ctx context.Context, req *loomv1.DecisionRequest) (*loomv1.DecisionResponse, error)

	// Name identifies the implementation, for traces and config ("jev",
	// "llm:anthropic", "mock", "off").
	Name() string

	// Model is the pinned model identifier the implementation answers with,
	// or "" when it has none.
	Model() string
}

// Limits mirror the TypeSafe System One API so a request that validates here
// is accepted there. They are also sensible bounds for any decision model.
const (
	// MinChoiceOptions is the smallest useful option set.
	MinChoiceOptions = 2
	// MaxChoiceOptions is the largest option set a Choice accepts.
	MaxChoiceOptions = 255
	// MinScoreLevels is the smallest ordered scale.
	MinScoreLevels = 2
	// MaxScoreLevels is the largest ordered scale a Score accepts.
	MaxScoreLevels = 10
	// MaxRequestTokens bounds state plus all questions.
	MaxRequestTokens = 64_000
	// MaxStateTokens bounds state plus the single longest question.
	MaxStateTokens = 32_000
)

// NoneOption is the option key WithNoneOption adds to a Choice. Every Choice
// over a space where "none of these" is a valid answer should carry it, so an
// out-of-space input has somewhere to put its probability mass other than a
// wrong option.
const NoneOption = "none_of_these"

// Sentinel errors. Implementations wrap these so callers can errors.Is them.
var (
	// ErrDisabled is returned by the Off decider and by a Router whose layer
	// is switched off.
	ErrDisabled = errors.New("decision: layer disabled")
	// ErrValidation wraps every request-shape problem; see ValidationError.
	ErrValidation = errors.New("decision: invalid request")
	// ErrUnauthorized is a rejected credential (HTTP 401).
	ErrUnauthorized = errors.New("decision: unauthorized")
	// ErrRateLimited is a vendor rate limit (HTTP 429).
	ErrRateLimited = errors.New("decision: rate limited")
	// ErrOverloaded is a vendor overload (HTTP 529).
	ErrOverloaded = errors.New("decision: provider overloaded")
	// ErrStateTooLarge is a request that exceeds MaxRequestTokens or
	// MaxStateTokens; it is a ValidationError.
	ErrStateTooLarge = errors.New("decision: state too large")
	// ErrMalformedAnswer is a decider response that does not match the
	// request: a missing question id, an unknown option key, a probability
	// outside [0, 1].
	ErrMalformedAnswer = errors.New("decision: malformed answer")
	// ErrBudgetExhausted is a session that has spent its decision budget.
	ErrBudgetExhausted = errors.New("decision: session budget exhausted")
)

// ValidationError names the request field that failed validation.
type ValidationError struct {
	// Field is a dotted path into the request ("questions.kind.options").
	Field string
	// Msg says what is wrong with it.
	Msg string
	// Cause is an optional more specific sentinel (ErrStateTooLarge).
	Cause error
}

// Error implements error.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("decision: invalid request: %s: %s", e.Field, e.Msg)
}

// Unwrap makes errors.Is(err, ErrValidation) true, and errors.Is(err, Cause)
// when a Cause is set.
func (e *ValidationError) Unwrap() []error {
	if e.Cause != nil {
		return []error{ErrValidation, e.Cause}
	}
	return []error{ErrValidation}
}

func validationErr(field, format string, args ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// sessionKey is the context key for the session id a decision belongs to.
type sessionKey struct{}

// WithSessionID attaches a session id to ctx. The Router uses it to account
// per-session budgets; deciders may use it for tracing. It is never sent to a
// vendor.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionKey{}, sessionID)
}

// SessionIDFromContext returns the session id set by WithSessionID, or "".
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(sessionKey{}).(string); ok {
		return v
	}
	return ""
}
