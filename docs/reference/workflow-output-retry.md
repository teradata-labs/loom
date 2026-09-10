# Workflow Output Retry Reference

**Version**: v1.3.0

Output retry adds automatic retry with informative feedback when agent output doesn't match expected formats. Supported by conditional, pipeline, and swarm workflow patterns.

## Table of Contents

- [Quick Reference](#quick-reference)
- [OutputRetryPolicy](#outputretrypolicy)
- [Conditional Pattern Retry](#conditional-pattern-retry)
- [Pipeline Pattern Retry](#pipeline-pattern-retry)
- [Swarm Pattern Retry](#swarm-pattern-retry)
- [Output Coercion](#output-coercion)
- [Configuration Reference](#configuration-reference)
- [Loader tolerances and errors](#loader-tolerances-and-errors)
- [Builder API](#builder-api)
- [Behavior Details](#behavior-details)
- [See Also](#see-also)

## Quick Reference

| Pattern | Trigger | Retry Includes | Fallback |
|---------|---------|---------------|----------|
| Conditional | Classifier output doesn't match any branch key | Valid branch keys listed | Default branch (after retries exhaust) |
| Pipeline (schema) | Output doesn't conform to JSON Schema | Full JSON Schema + specific violations | Graceful degradation (continue with unvalidated output) |
| Pipeline (LLM) | `validation_prompt` check fails | Validation criteria | Graceful degradation |
| Swarm vote | Agent doesn't use VOTE:/CONFIDENCE:/REASONING: format | Format template + example | Default "abstain" vote |
| Swarm judge | Judge picks an option not in the vote distribution | List of valid options | Error returned |

## OutputRetryPolicy

Defined in `proto/loom/v1/collaboration.proto`. Shared across all pattern types.

```protobuf
message OutputRetryPolicy {
  int32 max_retries = 1;
  bool include_valid_values = 2;
  RetrySessionMode session_mode = 3;
  string feedback_template = 4;
  int32 cooldown_ms = 5;
}

enum RetrySessionMode {
  RETRY_SESSION_MODE_UNSPECIFIED = 0;  // treated as FRESH
  RETRY_SESSION_MODE_CONTINUE = 1;
  RETRY_SESSION_MODE_FRESH = 2;
  RETRY_SESSION_MODE_ESCALATE = 3;
}
```

Fields 3-5 have been on the proto and honored by the validator; **v1.3.0 is the first release in which the workflow YAML loader parses them**, so a `retry_policy:` block written in YAML can now set all five.

### Fields

#### max_retries

**Type**: `int32`
**Default**: `0` (no retries, preserves backwards compatibility)
**Range**: `0` - `10` (values above 10 are silently capped)
**Required**: No

Number of retry attempts when output validation fails. Each retry uses a fresh session ID to avoid anchoring on previous bad output. Set to 0 or omit to disable retries.

#### include_valid_values

**Type**: `bool`
**Default**: `true` (YAML parser sets this; proto3 default is `false`)
**Required**: No

Whether to include valid output values in the retry prompt:
- **Conditional**: Always includes branch keys regardless of this setting (keys are essential for the retry to work).
- **Pipeline**: Controls whether the JSON Schema is included in the retry prompt.
- **Swarm**: Controls whether the VOTE:/CONFIDENCE:/REASONING: format template and example are included.

**Note**: When constructing `OutputRetryPolicy` directly in Go (not via YAML or builders), set `IncludeValidValues: true` explicitly. Proto3 defaults `bool` to `false`, but the YAML parser and builder methods default to `true`.

#### session_mode

**Type**: `RetrySessionMode` (YAML: string)
**Default**: `fresh` (proto `UNSPECIFIED` is treated as `FRESH`)
**Accepted**: `fresh`, `continue`, `escalate` — hyphens and any case are accepted, as are the full enum names such as `RETRY_SESSION_MODE_FRESH`
**Required**: No

How the agent's session is handled on each retry:

| Value | Behavior |
|-------|----------|
| `fresh` | New session per retry (`{workflowID}-retry{N}`). The agent sees the original prompt plus the failure feedback, with no memory of its bad output. |
| `continue` | Same session. The failure feedback is appended as a new user message, so the agent sees its previous attempt and what went wrong. Falls back to `fresh` when the caller supplied no feedback function. |
| `escalate` | `continue` on the first retry, then `fresh` for every retry after it. **No model change**: `escalate` only changes session handling, and no LLM upgrade is implemented for it. To escalate to a stronger model, use a `leveling:` ladder — see [Capability Leveling](workflow-leveling.md). |

#### feedback_template

**Type**: `string`
**Default**: empty (a built-in template is used)
**Required**: No

Custom feedback appended to the original prompt on each retry. The built-in default is `Previous attempt failed validation: <reason>\nPlease fix the issues and try again.`

Substituted variables:

| Variable | Value |
|----------|-------|
| `{{error}}` | The validation failure that triggered this retry |
| `{{previous_output}}` | The output of the previous attempt |
| `{{attempt}}` | The retry number (1 for the first retry) |
| `{{max_retries}}` | The effective retry bound after clamping to `[0, 10]` |

The template is appended to the original prompt, not substituted for it: the retry prompt is always `originalPrompt + "\n\n" + rendered template`.

#### cooldown_ms

**Type**: `int32`
**Default**: `0` (no cooldown)
**Range**: `>= 0`, whole numbers only
**Required**: No

Milliseconds to wait before each retry execution — applied before the call, not after. A negative or fractional value is a load error.

## Conditional Pattern Retry

When the classifier agent's output doesn't match any branch key, the conditional executor tries three strategies in order:

1. **Standard matching** (always): exact match, case-insensitive match, substring match
2. **Output coercion** (always, no LLM call): strips markdown, common prefixes, punctuation, then does word-boundary matching
3. **Retry with feedback** (only if `retry_policy` configured): sends a new prompt listing valid branch keys
4. **Default branch** (if configured): used after all retries exhaust

### YAML Example

```yaml
spec:
  type: conditional
  condition_agent_id: classifier
  condition_prompt: "Classify this as: bug, feature, or question"
  branches:
    bug:
      type: pipeline
      initial_prompt: "Fix this bug"
      stages:
        - agent_id: debugger
          prompt_template: "Debug: {{previous}}"
    feature:
      type: pipeline
      initial_prompt: "Build feature"
      stages:
        - agent_id: developer
          prompt_template: "Develop: {{previous}}"
  default_branch:
    type: fork-join
    prompt: "Handle unknown request"
    agent_ids: [fallback-agent]
    merge_strategy: first
  retry_policy:
    max_retries: 2
```

### Retry Prompt

On retry, the classifier receives:

```
Your previous response was: "I think this is probably a bug based on the error trace"

This output could not be matched to any valid workflow branch.

REASON: The condition evaluator must respond with exactly one of the allowed
branch values. Your response did not match any of them (even after
case-insensitive and substring matching).

VALID VALUES (respond with exactly one of these, nothing else):
- bug
- feature
- question

RULES:
1. Respond with ONLY one of the valid values above.
2. No explanation, no formatting, no punctuation, no quotes.
3. Just the single word/phrase from the list.

This is retry 1 of 2.
```

## Pipeline Pattern Retry

Pipeline stages support two types of validation, checked in order:

1. **JSON Schema validation** (`output_schema`): Instant, free, deterministic. Uses `gojsonschema`. Checked first.
2. **LLM validation** (`validation_prompt`): Asks an LLM if the output meets criteria. Checked second (only if schema passes or is not configured).

When validation fails and `retry_policy` is configured, the stage is retried with a prompt that explains the failure and shows the expected format.

### Output Normalization

When `output_schema` validation succeeds on JSON extracted from mixed text (e.g., `"Here is the data: {"result": "ok"} Done."`), the stage output is normalized to just the extracted JSON (`{"result": "ok"}`). This ensures downstream stages receive clean structured data.

### Graceful Degradation

When all retries are exhausted, the pipeline **continues** with the unvalidated output rather than failing. A warning is recorded in `WorkflowResult.Metadata["validation_warnings"]`. This is different from the behavior when no `retry_policy` is configured — in that case, validation failure is fatal.

⚠️ **With a `leveling:` block enabled on the stage, the stage never hard-fails on validation** — even with no `retry_policy` at all. Leveling always continues with the best output it obtained and records a `validation_warnings` entry instead. See [Capability Leveling → Failure Semantics](workflow-leveling.md#failure-semantics).

### YAML Example

```yaml
spec:
  type: pipeline
  initial_prompt: "Extract customer data"
  stages:
    - agent_id: extractor
      prompt_template: "Extract structured data from: {{previous}}"
      output_schema: '{"type":"object","required":["customers"],"properties":{"customers":{"type":"array","items":{"type":"object"}}}}'
      retry_policy:
        max_retries: 2
    - agent_id: formatter
      prompt_template: "Format as table: {{previous}}"
      validation_prompt: "Does the output contain a properly formatted markdown table? Answer yes or no."
      retry_policy:
        max_retries: 1
    - agent_id: reviewer
      prompt_template: "Review: {{previous}}"
```

### Retry Prompt (Schema Failure)

```
⚠️ OUTPUT VALIDATION FAILED (retry 1 of 2)

YOUR PREVIOUS OUTPUT:
---
Here is some analysis of the customers...
---

WHY IT FAILED:
JSON Schema validation failed: no valid JSON found in output

REQUIRED JSON SCHEMA:
{"type":"object","required":["customers"],"properties":{"customers":{"type":"array","items":{"type":"object"}}}}

WHAT TO DO:
1. Your output MUST be valid JSON conforming to the schema above.
2. Output ONLY the JSON object — no markdown, no explanation, no code fences.
3. Ensure all required fields are present and have the correct types.

ORIGINAL TASK:
Extract structured data from: ...
```

## Swarm Pattern Retry

Swarm voting expects agents to respond in `VOTE: / CONFIDENCE: / REASONING:` format. When parsing fails (agent outputs prose instead of the format), the vote defaults to "abstain" with 0.5 confidence. With `retry_policy`, the agent is retried with a prompt showing the expected format.

Judge retry works similarly: when the judge's decision doesn't match any option in the vote distribution, coercion is attempted first (case-insensitive + word-boundary matching), then retry with a prompt listing valid options.

### YAML Example

```yaml
spec:
  type: swarm
  question: "Which database should we use for the new service?"
  agent_ids: [dba-expert, backend-dev, architect]
  strategy: majority
  confidence_threshold: 0.6
  judge_agent_id: tech-lead
  retry_policy:
    max_retries: 2
```

### Vote Retry Prompt

```
Your previous response could not be parsed as a valid vote.

YOUR PREVIOUS OUTPUT:
---
I think PostgreSQL would be the best choice because of its strong ecosystem...
---

WHY IT FAILED:
Your response did not contain the required VOTE: / CONFIDENCE: / REASONING: fields
in the expected format. The vote was recorded as "abstain" with 0.5 confidence,
which was not your intent.

REQUIRED FORMAT (respond exactly like this):

VOTE: <your single clear answer>
CONFIDENCE: <a number between 0.0 and 1.0>
REASONING: <your explanation for the vote>

EXAMPLE:
VOTE: PostgreSQL
CONFIDENCE: 0.85
REASONING: PostgreSQL best addresses the core issue because...

QUESTION BEING VOTED ON:
Which database should we use for the new service?

This is retry 1 of 2. Please respond in the exact format above.
```

## Output Coercion

Before retrying (which costs an LLM call), the conditional executor applies lightweight text coercion to the classifier output. This is instant and free.

**Strategies applied in order:**
1. Strip markdown formatting (`**`, `` ` ``, ```` ``` ````)
2. Strip common prefixes ("The answer is:", "I classify this as:", "Based on my analysis,", etc.)
3. Strip trailing punctuation (`.`, `,`, `!`, `?`, `;`, `:`)
4. Word-boundary matching (`\b{key}\b` regex)

**Ambiguity handling**: If multiple branch keys match as standalone words, coercion returns "no match" (ambiguous) and falls through to retry.

**JSON extraction**: For pipeline schema validation, JSON is extracted from mixed text before validation. Handles: plain JSON, markdown code fences (````json ... ````), and JSON embedded in prose.

## Configuration Reference

### YAML Fields

#### Conditional Pattern

```yaml
retry_policy:              # Optional
  max_retries: 2           # int, 0-10, default 0
  include_valid_values: true  # bool, default true (no effect for conditionals)
  session_mode: fresh      # optional; fresh | continue | escalate
  feedback_template: "..." # optional; {{error}}, {{previous_output}}, {{attempt}}, {{max_retries}}
  cooldown_ms: 250         # optional; >= 0
```

#### Pipeline Stage

```yaml
stages:
  - agent_id: my-agent
    prompt_template: "..."
    validation_prompt: "..."    # Optional: LLM-based validation
    output_schema: '...'        # Optional: JSON Schema string
    retry_policy:               # Optional
      max_retries: 2            # int, 0-10, default 0
      include_valid_values: true   # bool, default true
      session_mode: continue    # optional; fresh | continue | escalate
      feedback_template: "..."  # optional
      cooldown_ms: 250          # optional; >= 0
```

#### Swarm Pattern

```yaml
retry_policy:              # Optional
  max_retries: 2           # int, 0-10, default 0
  include_valid_values: true  # bool, default true
  session_mode: fresh      # optional; fresh | continue | escalate
  feedback_template: "..." # optional
  cooldown_ms: 250         # optional; >= 0
```

### Loader tolerances and errors

The workflow-YAML loader is deliberately lenient about three shapes that loaded before it gained strict scalar helpers, because rejecting them would break configs that already run. Each keeps its old behavior and now emits a `zap` warning naming the YAML path, so no malformed key is dropped without a trace.

| YAML | Resolves to | Loader behavior |
|------|------------|-----------------|
| `max_retries: 2.5` | `2` | ⚠️ Tolerated — truncated toward zero, warning logged |
| `max_retries: "3"` | no retry policy | ⚠️ Tolerated — the string is ignored as if the key were absent (it is **not** parsed: that would turn a no-retry config into a retrying one), warning logged |
| `session_mode`, `feedback_template` or `cooldown_ms` without a positive `max_retries` | no retry policy | ⚠️ Tolerated — the whole `retry_policy` is dropped, warning names the ignored keys |
| non-bool `include_valid_values` (e.g. `"yes"`) | `true` | ⚠️ Tolerated — the value is ignored and the default applies, warning logged |

Everything else malformed in a `retry_policy` block is a **load error** wrapping `ErrInvalidWorkflow` (`invalid workflow structure: `):

| YAML | Error |
|------|-------|
| `max_retries: true` | `spec.stages[0].retry_policy.max_retries must be an integer, got bool` |
| `cooldown_ms: 250.5` | `spec.stages[0].retry_policy.cooldown_ms must be a whole number, got 250.5` |
| `cooldown_ms: -1` | `spec.stages[0].retry_policy.cooldown_ms must be >= 0, got -1` |
| `session_mode: warm` | `spec.stages[0].retry_policy.session_mode "warm" is not a known retry session mode (valid: continue, fresh, escalate; the full enum name such as RETRY_SESSION_MODE_FRESH is also accepted)` |

The tolerances are local to `retry_policy`. The `leveling:` block has no legacy configs to keep loading, so every malformation there is a load error — see [Capability Leveling](workflow-leveling.md#rejected-configurations).

## Builder API

### Conditional

```go
result, err := orchestrator.Conditional(classifier, "Classify this issue").
    When("bug", bugWorkflow).
    When("feature", featureWorkflow).
    Default(fallbackWorkflow).
    WithRetry(2).  // max 2 retries
    Execute(ctx)
```

### Pipeline

```go
// With LLM validation + retry
result, err := orchestrator.Pipeline("Extract data").
    WithStageRetry(extractor, "Extract: {{previous}}", "Is this valid JSON?", 2).
    WithStage(formatter, "Format: {{previous}}").
    Execute(ctx)

// With JSON Schema validation + retry
result, err := orchestrator.Pipeline("Extract data").
    WithStageSchema(extractor, "Extract: {{previous}}", `{"type":"object","required":["data"]}`, 2).
    WithStage(formatter, "Format: {{previous}}").
    Execute(ctx)
```

## Behavior Details

### Fresh Session Per Retry

Fresh is the default. Each retry uses a unique session ID (`{workflowID}-...-retry{N}`) to prevent the agent from being anchored to its previous bad output, and the agent starts with a clean conversation history on each retry.

`session_mode: continue` instead appends the validation feedback to the same session, so the agent sees its own previous attempt alongside what went wrong. `session_mode: escalate` uses `continue` for the first retry and `fresh` after that. See [session_mode](#session_mode).

### Retry Cap

All retry counts are capped at `maxOutputRetries = 10` regardless of the configured `max_retries` value. This prevents runaway LLM costs from misconfiguration.

### Context Cancellation

All retry loops check `ctx.Err()` at the top of each iteration. If the parent context is cancelled (timeout, client disconnect), the retry loop exits immediately.

### Validation Order (Pipeline)

When both `output_schema` and `validation_prompt` are configured on a pipeline stage:
1. Schema validation runs first (instant, free)
2. If schema passes, LLM validation runs (costs an LLM call)
3. If either fails, retry is triggered (if `retry_policy` configured)

### Default Branch Ordering (Conditional)

When both `default_branch` and `retry_policy` are configured:
1. Standard matching (exact, case-insensitive, substring)
2. Output coercion (no LLM call)
3. Retry with feedback (LLM calls)
4. Default branch (final fallback)

This means retries are attempted **before** falling back to the default branch.

### Graceful Degradation Metadata

When pipeline retries are exhausted, the workflow result includes:
```json
{
  "metadata": {
    "validation_warnings": "stage 2 (formatter): JSON Schema validation failed: ..."
  }
}
```

## See Also

- [Workflow Capability Leveling Reference](workflow-leveling.md) — escalate a failed stage output up a ladder of stronger models; consumes `output_policy.retry_policy`
- [Iterative Workflow Reference](workflow-iterative.md) — iterative pipelines with restart coordination
- [Workflow All-Fields Reference](../../examples/reference/workflows/workflow-all-fields-reference.yaml) — full YAML field reference
