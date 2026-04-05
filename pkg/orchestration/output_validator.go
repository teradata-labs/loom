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

// ValidateAndRetry executes an agent, validates the output against the policy,
// and retries with feedback if validation fails. Works across all workflow patterns.
//
// Parameters:
//   - policy: output validation policy (nil = no validation, execute once)
//   - execute: function to execute the agent (called for FRESH sessions)
//   - feedback: function to send feedback in the same session (called for CONTINUE mode)
//   - originalPrompt: the original prompt for fresh retries
//   - workflowID: base workflow ID for session ID generation
//
// Returns the agent result (possibly from a retry) and any validation warnings.
func (v *OutputValidator) ValidateAndRetry(
	ctx context.Context,
	policy *loomv1.OutputPolicy,
	execute ExecuteFunc,
	feedback FeedbackFunc,
	originalPrompt string,
	workflowID string,
) (*loomv1.AgentResult, []string, error) {
	ctx, span := v.tracer.StartSpan(ctx, "output_validator.validate_and_retry")
	defer v.tracer.EndSpan(span)

	// No policy = execute once, no validation.
	if policy == nil {
		result, err := execute(ctx, workflowID, originalPrompt)
		return result, nil, err
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
	currentSessionID := workflowID

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return lastResult, warnings, ctx.Err()
		}

		var result *loomv1.AgentResult
		var err error

		if attempt == 0 {
			// First attempt: always execute with original prompt.
			result, err = execute(ctx, currentSessionID, originalPrompt)
		} else {
			// Retry: behavior depends on session mode.
			switch effectiveMode(sessionMode, attempt) {
			case loomv1.RetrySessionMode_RETRY_SESSION_MODE_CONTINUE:
				if feedback != nil {
					result, err = feedback(ctx, currentSessionID, warnings[len(warnings)-1])
				} else {
					// Fallback to fresh if no feedback function provided.
					currentSessionID = fmt.Sprintf("%s-retry%d", workflowID, attempt)
					prompt := buildRetryPrompt(originalPrompt, warnings[len(warnings)-1], retryPolicy)
					result, err = execute(ctx, currentSessionID, prompt)
				}
			case loomv1.RetrySessionMode_RETRY_SESSION_MODE_FRESH:
				currentSessionID = fmt.Sprintf("%s-retry%d", workflowID, attempt)
				prompt := buildRetryPrompt(originalPrompt, warnings[len(warnings)-1], retryPolicy)
				result, err = execute(ctx, currentSessionID, prompt)
			}

			// Apply cooldown if configured.
			if retryPolicy != nil && retryPolicy.CooldownMs > 0 {
				time.Sleep(time.Duration(retryPolicy.CooldownMs) * time.Millisecond)
			}
		}

		if err != nil {
			return lastResult, warnings, fmt.Errorf("execution failed (attempt %d): %w", attempt+1, err)
		}
		lastResult = result

		// Validate the output.
		validationErr := v.validate(ctx, policy, result.Output)
		if validationErr == nil {
			// Validation passed.
			return result, warnings, nil
		}

		// Validation failed.
		warning := fmt.Sprintf("attempt %d: %s", attempt+1, validationErr.Error())
		warnings = append(warnings, warning)
		v.logger.Debug("output validation failed",
			zap.Int("attempt", attempt+1),
			zap.Int("max_retries", maxRetries),
			zap.String("error", validationErr.Error()))
	}

	// All retries exhausted — return last result with warnings.
	return lastResult, warnings, nil
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
		var jsonErr *json.SyntaxError
		if json.Unmarshal([]byte(output), &jsonErr) != nil {
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
func buildRetryPrompt(originalPrompt, lastWarning string, retryPolicy *loomv1.OutputRetryPolicy) string {
	if retryPolicy != nil && retryPolicy.FeedbackTemplate != "" {
		// Use custom feedback template.
		prompt := retryPolicy.FeedbackTemplate
		prompt = strings.ReplaceAll(prompt, "{{error}}", lastWarning)
		prompt = strings.ReplaceAll(prompt, "{{previous_output}}", "") // not available in fresh mode
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
