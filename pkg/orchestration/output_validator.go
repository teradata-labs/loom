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

package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xeipuuv/gojsonschema"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// OutputValidator provides universal output validation and retry for any agent execution.
// It composes structural validation (JSON Schema, instant) with semantic validation
// (LLM-based acceptance criteria) and supports three retry session modes:
// CONTINUE (same session), FRESH (new session), and ESCALATE (continue then upgrade LLM).
type OutputValidator struct {
	tracer observability.Tracer
	logger *zap.Logger
}

// NewOutputValidator creates a new output validator.
func NewOutputValidator(tracer observability.Tracer, logger *zap.Logger) *OutputValidator {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OutputValidator{tracer: tracer, logger: logger}
}

// ExecuteFunc is a function that executes an agent and returns its output.
// sessionID controls whether this is the same or a fresh session.
// prompt is the (possibly modified) prompt for this execution.
type ExecuteFunc func(ctx context.Context, sessionID string, prompt string) (*loomv1.AgentResult, error)

// FeedbackFunc appends validation feedback to an existing session.
// Used in CONTINUE mode to add a user message to the same conversation.
type FeedbackFunc func(ctx context.Context, sessionID string, feedback string) (*loomv1.AgentResult, error)

// CoerceFunc attempts to rewrite an output that failed validation into a
// valid form (for example, extracting fenced JSON from mixed text).
// ok reports whether a candidate rewrite was produced.
type CoerceFunc func(output string) (coerced string, ok bool)

// ValidationOutcome is the validator's final verdict on the returned result.
type ValidationOutcome struct {
	// Passed reports whether the final attempt satisfied the policy.
	// Vacuously true when the policy is nil or carries no criteria.
	Passed bool
	// Err is the validation error from the final attempt; nil when Passed.
	Err error
	// Warnings holds one entry per failed attempt.
	Warnings []string
	// CoercionApplied is true when coerce rewrote the output into a form
	// that passed validation; the returned result carries the rewrite.
	CoercionApplied bool
}

// ValidateAndRetry executes an agent, validates the output against the policy,
// and retries with feedback if validation fails. Works across all workflow patterns.
//
// Parameters:
//   - policy: output validation policy (nil = no validation, execute once)
//   - execute: function to execute the agent (called for FRESH sessions)
//   - feedback: function to send feedback in the same session (called for CONTINUE mode)
//   - originalPrompt: the original prompt for fresh retries
//   - workflowID: base workflow ID for session ID generation
//   - coerce: optional free rewrite attempted on a schema failure before the
//     attempt is written off. A rewrite that validates ends the loop, so a
//     fenced-but-valid payload never burns a retry. nil disables coercion.
//
// Returns the agent result (possibly from a retry, possibly coerced) and the
// final verdict on it. The returned ValidationOutcome carries whether that
// result actually satisfies the policy, the validation error when it does not,
// one warning per failed attempt, and whether coercion produced the pass — so
// callers never need to re-validate the output themselves.
//
// A non-nil error means execution or the context failed, not that validation
// failed: an output that fails every attempt is returned with Passed=false and
// a nil error.
func (v *OutputValidator) ValidateAndRetry(
	ctx context.Context,
	policy *loomv1.OutputPolicy,
	execute ExecuteFunc,
	feedback FeedbackFunc,
	originalPrompt string,
	workflowID string,
	coerce CoerceFunc,
) (*loomv1.AgentResult, ValidationOutcome, error) {
	ctx, span := v.tracer.StartSpan(ctx, "output_validator.validate_and_retry")
	defer v.tracer.EndSpan(span)

	// No policy = execute once, no validation. Passing is vacuous.
	if policy == nil {
		result, err := execute(ctx, workflowID, originalPrompt)
		return result, ValidationOutcome{Passed: err == nil}, err
	}

	retryPolicy := policy.RetryPolicy
	maxRetries := 0
	if retryPolicy != nil {
		maxRetries = int(retryPolicy.MaxRetries)
		if maxRetries > 10 {
			maxRetries = 10 // cap
		}
	}

	sessionMode := loomv1.RetrySessionMode_RETRY_SESSION_MODE_FRESH
	if retryPolicy != nil && retryPolicy.SessionMode != loomv1.RetrySessionMode_RETRY_SESSION_MODE_UNSPECIFIED {
		sessionMode = retryPolicy.SessionMode
	}

	var lastResult *loomv1.AgentResult
	var warnings []string
	var lastValidationErr error
	currentSessionID := workflowID

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return lastResult, ValidationOutcome{Warnings: warnings}, ctx.Err()
		}

		var result *loomv1.AgentResult
		var err error

		if attempt == 0 {
			// First attempt: always execute with original prompt.
			result, err = execute(ctx, currentSessionID, originalPrompt)
		} else {
			// Apply cooldown before retry execution (not after).
			if retryPolicy != nil && retryPolicy.CooldownMs > 0 {
				time.Sleep(time.Duration(retryPolicy.CooldownMs) * time.Millisecond)
			}

			lastOutput := ""
			if lastResult != nil {
				lastOutput = lastResult.Output
			}

			// Retry: behavior depends on session mode.
			switch effectiveMode(sessionMode, attempt) {
			case loomv1.RetrySessionMode_RETRY_SESSION_MODE_CONTINUE:
				if feedback != nil {
					result, err = feedback(ctx, currentSessionID, warnings[len(warnings)-1])
				} else {
					// Fallback to fresh if no feedback function provided.
					currentSessionID = fmt.Sprintf("%s-retry%d", workflowID, attempt)
					prompt := buildRetryPromptWithOutput(originalPrompt, warnings[len(warnings)-1], lastOutput, retryPolicy, attempt, maxRetries)
					result, err = execute(ctx, currentSessionID, prompt)
				}
			case loomv1.RetrySessionMode_RETRY_SESSION_MODE_FRESH:
				currentSessionID = fmt.Sprintf("%s-retry%d", workflowID, attempt)
				prompt := buildRetryPromptWithOutput(originalPrompt, warnings[len(warnings)-1], lastOutput, retryPolicy, attempt, maxRetries)
				result, err = execute(ctx, currentSessionID, prompt)
			default:
				// Unknown mode — fall back to FRESH.
				currentSessionID = fmt.Sprintf("%s-retry%d", workflowID, attempt)
				prompt := buildRetryPromptWithOutput(originalPrompt, warnings[len(warnings)-1], lastOutput, retryPolicy, attempt, maxRetries)
				result, err = execute(ctx, currentSessionID, prompt)
			}
		}

		if err != nil {
			return lastResult, ValidationOutcome{Warnings: warnings},
				fmt.Errorf("execution failed (attempt %d): %w", attempt+1, err)
		}
		lastResult = result

		// Validate the output.
		validationErr := v.validate(ctx, policy, result.Output)
		if validationErr == nil {
			// Validation passed.
			return result, ValidationOutcome{Passed: true, Warnings: warnings}, nil
		}

		// A free rewrite that validates costs nothing and ends the loop, so a
		// well-formed payload wrapped in prose or fences never burns a retry.
		if coerce != nil && policy.OutputSchema != "" {
			if coerced, ok := coerce(result.Output); ok && v.validate(ctx, policy, coerced) == nil {
				result.Output = coerced
				v.logger.Debug("output coerced into a valid form without retrying",
					zap.Int("attempt", attempt+1))
				return result, ValidationOutcome{
					Passed:          true,
					Warnings:        warnings,
					CoercionApplied: true,
				}, nil
			}
		}

		// Validation failed.
		lastValidationErr = validationErr
		warning := fmt.Sprintf("attempt %d: %s", attempt+1, validationErr.Error())
		warnings = append(warnings, warning)
		v.logger.Debug("output validation failed",
			zap.Int("attempt", attempt+1),
			zap.Int("max_retries", maxRetries),
			zap.String("error", validationErr.Error()))
	}

	// All retries exhausted — return the last result with the verdict against it.
	return lastResult, ValidationOutcome{Err: lastValidationErr, Warnings: warnings}, nil
}

// validate checks an output against all validation criteria in the policy.
// Returns nil if the output passes all checks.
func (v *OutputValidator) validate(ctx context.Context, policy *loomv1.OutputPolicy, output string) error {
	// 1. Structural validation (JSON Schema — instant, free).
	if policy.OutputSchema != "" {
		if err := validateJSONSchema(policy.OutputSchema, output); err != nil {
			return fmt.Errorf("schema validation: %w", err)
		}
	}

	// 2. Semantic validation (acceptance criteria — requires LLM, done by caller).
	// Note: Full LLM-based semantic validation requires the caller to provide
	// a validator agent or judge. For now, acceptance_criteria is stored on the
	// policy and can be evaluated by the TaskManager or a dedicated validator.
	// This keeps the OutputValidator free of LLM dependencies.

	return nil
}

// validateJSONSchema validates output against a JSON Schema string.
func validateJSONSchema(schemaStr, output string) error {
	schemaLoader := gojsonschema.NewStringLoader(schemaStr)
	documentLoader := gojsonschema.NewStringLoader(output)

	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		// If the output isn't valid JSON at all, report that.
		if !json.Valid([]byte(output)) {
			return fmt.Errorf("output is not valid JSON: %s", truncateForError(output, 100))
		}
		return fmt.Errorf("schema validation error: %w", err)
	}

	if !result.Valid() {
		errs := make([]string, 0, len(result.Errors()))
		for _, e := range result.Errors() {
			errs = append(errs, e.String())
		}
		return fmt.Errorf("schema violations: %s", strings.Join(errs, "; "))
	}

	return nil
}

// effectiveMode returns the retry session mode for a given attempt,
// accounting for ESCALATE mode (CONTINUE first, then FRESH).
func effectiveMode(mode loomv1.RetrySessionMode, attempt int) loomv1.RetrySessionMode {
	if mode == loomv1.RetrySessionMode_RETRY_SESSION_MODE_ESCALATE {
		if attempt <= 1 {
			return loomv1.RetrySessionMode_RETRY_SESSION_MODE_CONTINUE
		}
		return loomv1.RetrySessionMode_RETRY_SESSION_MODE_FRESH
	}
	return mode
}

// buildRetryPrompt constructs a retry prompt with validation feedback.
func buildRetryPromptWithOutput(originalPrompt, lastWarning, previousOutput string, retryPolicy *loomv1.OutputRetryPolicy, attempt, maxRetries int) string {
	if retryPolicy != nil && retryPolicy.FeedbackTemplate != "" {
		// Use custom feedback template with all documented variables.
		prompt := retryPolicy.FeedbackTemplate
		prompt = strings.ReplaceAll(prompt, "{{error}}", lastWarning)
		prompt = strings.ReplaceAll(prompt, "{{previous_output}}", previousOutput)
		prompt = strings.ReplaceAll(prompt, "{{attempt}}", fmt.Sprintf("%d", attempt))
		prompt = strings.ReplaceAll(prompt, "{{max_retries}}", fmt.Sprintf("%d", maxRetries))
		return originalPrompt + "\n\n" + prompt
	}

	// Default feedback.
	return fmt.Sprintf("%s\n\nPrevious attempt failed validation: %s\nPlease fix the issues and try again.",
		originalPrompt, lastWarning)
}

func truncateForError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
