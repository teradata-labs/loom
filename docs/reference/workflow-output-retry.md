# Workflow Output Retry Reference

**Version**: v1.2.0

Output retry adds automatic retry with informative feedback when agent output doesn't match expected formats. Supported by conditional, pipeline, and swarm workflow patterns.

## Table of Contents

- [Quick Reference](#quick-reference)
- [OutputRetryPolicy](#outputretrypolicy)
- [Conditional Pattern Retry](#conditional-pattern-retry)
- [Pipeline Pattern Retry](#pipeline-pattern-retry)
- [Swarm Pattern Retry](#swarm-pattern-retry)
- [Output Coercion](#output-coercion)
- [Configuration Reference](#configuration-reference)
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
}
```

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
```

#### Swarm Pattern

```yaml
retry_policy:              # Optional
  max_retries: 2           # int, 0-10, default 0
  include_valid_values: true  # bool, default true
```

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

Each retry uses a unique session ID (`{workflowID}-...-retry{N}`) to prevent the agent from being anchored to its previous bad output. The agent starts with a clean conversation history on each retry.

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

- [Iterative Workflow Reference](workflow-iterative.md) — iterative pipelines with restart coordination
- [Workflow All-Fields Reference](../../examples/reference/workflows/workflow-all-fields-reference.yaml) — complete YAML field reference
