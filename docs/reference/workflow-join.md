# Workflow Join Policy Reference

**Version**: v1.4.0
**Status**: ✅ Implemented (fork-join, parallel, pipeline history, debate)

A hand-off carries several agents' outputs into one prompt that a single agent receives. This reference covers the bounded hand-off: full outputs go to SharedMemory, the inline copy is capped per entry and budgeted as a whole, and the receiving step may be a real agent that can fetch any entry back. It applies to four places:

| Pattern | Hand-off | Config block | Receiver |
|---------|----------|--------------|----------|
| fork-join, parallel | branch outputs → join step | `spec.join` (`JoinPolicy`) | `join.agent_id`, or the orchestrator merge LLM |
| pipeline, iterative pipeline | prior stage outputs → a stage reading `{{history}}` or under `pass_full_history` | `spec.history` (`HistoryPolicy`) | the stage agent |
| debate | every round's positions → the moderator synthesis | `spec.join` (`JoinPolicy`, no `agent_id`) | `moderator_agent_id` |
| debate | the other agents' positions → each round 2+ response prompt | `spec.join` | the responding agent |

## Table of Contents

- [The problem it solves](#the-problem-it-solves)
- [How the hand-off is bounded](#how-the-hand-off-is-bounded)
- [Fork-join and parallel](#fork-join-and-parallel)
- [Pipeline history](#pipeline-history)
- [Debate](#debate)
- [Overflow strategies](#overflow-strategies)
- [Configuration Reference](#configuration-reference)
- [Loader errors](#loader-errors)
- [Builder API](#builder-api)
- [Result metadata and cost](#result-metadata-and-cost)
- [Limits and open items](#limits-and-open-items)
- [See Also](#see-also)

## The problem it solves

Left unbounded, a hand-off prompt grows with the producers' verbosity until the provider refuses it (`prompt is too long` / `context_length_exceeded`), after every producer has already been paid for. Before these policies:

- the fork-join and parallel LLM merge strategies (`consensus`, `voting`, `summary`, `best`) sent every branch output inline in one raw merge-LLM call, with no context relief, no tools and no size check;
- a pipeline stage reading `{{history}}` (or any stage under `pass_full_history`) received every prior output in full, even though `{{previous}}` had been capped at 8 KiB with a SharedMemory pointer since the hybrid hand-off;
- the debate moderator's synthesis prompt concatenated every position and argument from every round, and each agent's round 2+ response prompt carried the other agents' full positions.

## How the hand-off is bounded

Every bounded hand-off goes through the same three steps (`pkg/orchestration/join.go`):

1. **Store.** With a SharedMemory store on the orchestrator (`orchestration.Config.SharedMemory`), each entry's full text is put in the `workflow` namespace under a key the receiver is told about. Without a store the inline copy is all the receiver gets, and the notices say so.
2. **Cap.** The inline copy of an entry longer than the per-entry byte cap is cut at the last line break in its back half (never inside a UTF-8 sequence) and carries a notice naming the key.
3. **Budget.** The whole rendered prompt is counted with the same tokenizer the agent memory uses. If it exceeds the budget, the overflow strategy brings it under: the per-entry share is `(budget - everything that is not entry text) / entries`, discounted 5% so the re-rendered prompt lands under the budget. A share below 64 tokens is an error, since a card that small carries nothing.

The budget is `max_input_tokens` when set, else half of the receiving agent's usable window (`max_context_tokens` less `reserved_output_tokens`, defaults 200000 and 20000). With no receiving agent and no explicit budget there is no check.

A receiving agent gets an `## AVAILABLE CONTEXT (SharedMemory)` header listing every stored key before the sections, and can pull any entry back in full with `shared_memory_read` when it has that tool.

## Fork-join and parallel

```yaml
spec:
  type: fork-join
  prompt: "Analyze Q3 results"
  agent_ids: [finance, sales, ops]
  merge_strategy: concatenate          # with a join agent this only picks the default prompt
  join:
    agent_id: presenter                # run the join through this agent
    prompt_template: |
      Build a 10-slide deck for: {{prompt}}
      {{branches}}
    max_branch_bytes: 4096             # inline cap per branch (0 = 8192)
    max_input_tokens: 60000            # whole-input budget (0 = half the agent's usable window)
    overflow_strategy: pre_reduce      # pre_reduce | truncate | fail
```

| Step | What happens | Extra LLM calls |
|------|--------------|-----------------|
| Store | Every non-failed branch output is written under `<workflow-id>-branch-N-output` | 0 |
| Cap | Each inline copy over `max_branch_bytes` is cut; the section carries a notice naming the key | 0 |
| Budget | The rendered input is token-counted against the budget; over budget, `overflow_strategy` runs | pre_reduce: 1 per oversize branch |
| Join | The join agent's `Chat` turn, or one merge-LLM call when no agent is set | the turn's own calls |

**Join agent.** `join.agent_id` names a registered agent. The join then runs as that agent's conversation turn (`agent.Chat`) under session `<workflow-id>-join-<agent-id>`: its own system prompt and tools apply (including `shared_memory_read` when configured), its context relief and the provider context-too-long backstop apply, and its output is the workflow's `merged_output`. The agent may also be one of the branch agents. It is validated before any branch runs, and `ExtractAgentIDs` includes it, so the multi-agent server loads it with the branch agents. With a join agent set, the agent always runs, whatever `merge_strategy` says; the strategy only selects the default prompt when `prompt_template` is empty.

**Prompt template.** `{{prompt}}` (the fan-out prompt; empty for parallel) and `{{branch_count}}` are replaced first, `{{branches}}` last by splicing, so placeholder-like text inside a branch output is never substituted. A template without `{{branches}}` gets the sections appended. When the template is empty, the merge strategy's prompt is used with the same bounded sections. Each receiving agent's header also carries `Keys you write with shared_memory_write should start with \`<workflow-id>-\` so they stay with this workflow.`

Each section looks like:

```
### Branch 2: agent sales
<inline copy>
[Branch output truncated: 4096 of 31870 bytes shown. Full output in SharedMemory: shared_memory_read(namespace="workflow", key="fork-join-7f3a9c1e-branch-2-output")]
```

A failed branch renders as `### Branch N: agent X (FAILED)` followed by its error text.

**Without a join agent**, a policy only affects the LLM merge strategies. `concatenate` and `first` return the branch text itself as the workflow output and are never cut; the executor logs a warning and ignores the policy (the YAML loader rejects that combination outright).

## Pipeline history

A stage that compiles everything reads `{{history}}`, or runs under `pass_full_history: true` with neither placeholder in its template (the history is then appended as `Previous stages:`).

**Always, policy or not:** each history entry is the same inline copy the next stage receives as `{{previous}}`. With a SharedMemory store, the full output is under `<workflow-id>-stage-N-output` (stored at stage completion) and the entry is cut to 8 KiB with the pipeline's own notice:

```
Stage 1 output:
<inline copy>

[OUTPUT TRUNCATED - Full data stored in SharedMemory. Use shared_memory_read(namespace="workflow", key="<workflow-id>-stage-1-output") to fetch complete output]
```

Without a store, nothing is cut that the stage could not fetch back: entries are the full outputs, as before. No extra LLM calls are made either way.

**With `spec.history`:** the whole stage prompt (template, `{{previous}}`, the SharedMemory header and every entry) is held to a token budget derived from the stage agent, and oversize entries are reduced, truncated or the stage fails. `{{previous}}` and the template text are never cut; only the history entries are.

```yaml
spec:
  type: pipeline
  initial_prompt: "Prepare the quarterly review"
  stages:
    - agent_id: finance-analyst
      prompt_template: "Analyze finances: {{previous}}"
    - agent_id: sales-analyst
      prompt_template: "Analyze sales: {{previous}}"
    - agent_id: report-writer
      prompt_template: "Write the report from: {{history}}"
  history:
    max_entry_bytes: 6144            # inline cap per prior stage (0 = 8192)
    max_input_tokens: 48000          # whole-prompt budget (0 = half the stage agent's usable window)
    overflow_strategy: pre_reduce    # pre_reduce (default) | truncate | fail
```

`history: {}` enables the budget with defaults. The block is rejected when no stage uses `{{history}}` and `pass_full_history` is false, since it could never apply. An iterative workflow's nested `pipeline` takes the same block; the iterative executor renders history from the full outputs and bounds them once, at render time.

The pre-reduce LLM is the stage agent's compressor role (its main LLM when none is configured). The reduce calls are added to the run's cost and counted in `history.reduce_llm_calls`.

## Debate

The debate's output is the moderator's synthesis when `moderator_agent_id` is set, else the last round's heuristic synthesis line (a confidence summary, not a written synthesis). Two prompts carry other agents' full output and are bounded by `spec.join`:

- **Round 2+ response prompts.** Each debater is shown the other agents' positions (in `agent_ids` order) and asked to respond. Under a policy the positions are stored under `<workflow-id>-round-R-position-<agent-id>`, capped inline and the prompt is held to the responding agent's budget.
- **Moderator synthesis.** Every round's positions, grouped by round, with arguments. Under a policy they are stored under the same keys, capped, and the prompt is held to the moderator's budget. `join.prompt_template` replaces the default wording: `{{prompt}}` is the topic, `{{branches}}` the rendered rounds, `{{branch_count}}` the number of positions.

```yaml
spec:
  type: debate
  topic: "Monolith or microservices?"
  agent_ids: [advocate, skeptic]
  rounds: 3
  moderator_agent_id: senior-architect
  join:
    max_branch_bytes: 4096
    max_input_tokens: 60000
    overflow_strategy: pre_reduce
```

`join.agent_id` is rejected on debate: the moderator is the join agent, and each debater receives its own response prompt. `merge_strategy` is not a debate field; the engine never read it and the loader now rejects it (the proto field is deprecated and kept for wire compatibility). A debate run through an `AgentProvider` that is not the orchestrator cannot bound hand-offs; the policy is then logged and ignored.

An over-budget `fail` is fatal on both prompts, even though an ordinary response-turn failure is not (the position stands and the debate continues).

## Overflow strategies

| `overflow_strategy` | Behavior | Cost |
|---------------------|----------|------|
| `pre_reduce` (default when unset) | Each entry over its share is reduced to a card of at most that share by an LLM: the receiving agent's compressor-role LLM (its main LLM when none is configured), else the orchestrator merge LLM. Reductions run in parallel under the orchestrator LLM semaphore. A reduction that fails or still exceeds the share is truncated to the share, so the result always fits. With no LLM available at all, falls back to truncate. | one call per oversize entry |
| `truncate` | Each entry over its share is cut to it at a line boundary with the SharedMemory notice. | none |
| `fail` | The workflow fails with `ErrJoinInputOverBudget` (wrapped as `collaboration.ErrHandoffOverBudget` for debate) before the receiving call. | none |

The budget check only runs when a budget exists: `max_input_tokens` set, or a receiving agent to derive one from. For fork-join and parallel without a join agent and without `max_input_tokens` there is no check, which is the pre-existing behavior of the raw merge path.

## Configuration Reference

Proto: `JoinPolicy` and `HistoryPolicy` in `proto/loom/v1/orchestration.proto`. Fields: `ForkJoinPattern.join_policy` (6), `ParallelPattern.join_policy` (4), `DebatePattern.join_policy` (6), `PipelinePattern.history_policy` (4).

`spec.join` (fork-join, parallel, debate):

| YAML | Default | Meaning |
|------|---------|---------|
| `agent_id` | `""` | Registered agent that performs the join. Empty keeps the raw merge-LLM call. **Rejected on debate.** |
| `prompt_template` | strategy default | Join / synthesis prompt; see placeholders above. |
| `max_branch_bytes` | `0` = 8192 | Per-entry inline cap in bytes. Must be `>= 0`. |
| `max_input_tokens` | `0` = derived | Whole-input token budget. `0` derives half of the receiving agent's usable window; for fork-join/parallel with no join agent, `0` disables the check. Must be `>= 0`. |
| `overflow_strategy` | `pre_reduce` | `pre_reduce`, `truncate` or `fail`. Also accepts `pre-reduce` and `reduce`. |

`spec.history` (pipeline, iterative pipeline):

| YAML | Default | Meaning |
|------|---------|---------|
| `max_entry_bytes` | `0` = 8192 | Per-entry inline cap in bytes; applies only to entries whose full text is in SharedMemory. Must be `>= 0`. |
| `max_input_tokens` | `0` = derived | Whole-prompt token budget, half of the stage agent's usable window when `0`. Must be `>= 0`. |
| `overflow_strategy` | `pre_reduce` | `pre_reduce`, `truncate` or `fail`. |

## Loader errors

The loader rejects dead configuration rather than carrying it:

| YAML | Error |
|------|-------|
| `join: {}` | `spec.join sets nothing` |
| `max_branch_bytes: -1` / `max_input_tokens: -1` / `max_entry_bytes: -1` | `must be >= 0` |
| `overflow_strategy: explode` | `unknown value "explode" (expected pre_reduce, truncate or fail)` |
| fork-join/parallel `overflow_strategy` without `agent_id` and without `max_input_tokens` | `can never fire` |
| fork-join/parallel `join` without `agent_id` on `merge_strategy: concatenate` or `first` | `has no effect on merge_strategy concatenate` |
| debate `join.agent_id` | `spec.join.agent_id is not used by debate: moderator_agent_id is the join agent` |
| debate `merge_strategy` | `spec.merge_strategy has no effect on debate` |
| `history: 5` / `history:` (nil) | `spec.history must be an object` |
| `history` with no `{{history}}` stage and `pass_full_history: false` | `spec.history has no effect` |
| unknown key | `spec.join.<key> is not a join field` / `spec.history.<key> is not a history field` |
| `max_input_tokens: 10.5` | `must be a whole number` |

The structure validator (`loom validate`, the weaver) accepts `join` on fork-join, parallel and debate and `history` on pipeline, and flags a debate `merge_strategy` with the replacement.

## Builder API

```go
result, err := orch.Fork("Analyze Q3 results").
    WithAgentIDs("finance", "sales", "ops").
    Join(loomv1.MergeStrategy_CONCATENATE).
    WithJoinPolicy(&loomv1.JoinPolicy{
        AgentId:        "presenter",
        PromptTemplate: "Build a 10-slide deck for: {{prompt}}\n\n{{branches}}",
        MaxBranchBytes: 4096,
    }).
    Execute(ctx)

result, err = orch.Parallel().
    WithTaskByID("writer", "Write the user guide").
    WithTaskByID("api", "Write the API notes").
    WithJoinPolicy(&loomv1.JoinPolicy{AgentId: "editor"}).
    Execute(ctx)

result, err = orch.Pipeline("Prepare the quarterly review").
    WithStage(finance, "Analyze finances: {{previous}}").
    WithStage(writer, "Write the report from: {{history}}").
    WithHistoryPolicy(&loomv1.HistoryPolicy{MaxInputTokens: 48000}).
    Execute(ctx)

result, err = orch.Debate("Monolith or microservices?").
    WithAgents(advocate, skeptic).
    WithRounds(3).
    WithModerator(architect).
    WithJoinPolicy(&loomv1.JoinPolicy{MaxBranchBytes: 4096}).
    Execute(ctx)
```

`DebateBuilder.WithMergeStrategy` is deprecated and has no effect. The orchestrator also implements `collaboration.HandoffBounder` (`BoundHandoff`), which is how the debate engine reaches the bounded hand-off without importing `pkg/orchestration`.

## Result metadata and cost

`looms workflow run` prints every result metadata key under `Metadata:` after the cost lines; over gRPC the same keys are in `WorkflowResult.metadata`.

Fork-join and parallel (`WorkflowResult.metadata`, and the `fork_join.join` / `parallel.merge` span):

| Key | Value |
|-----|-------|
| `join.mode` | `agent` or `merge_llm` |
| `join.agent_id` | the join agent (agent mode only) |
| `join.input_tokens` / `join.budget_tokens` | tokens of the prompt actually sent; the budget applied (`0` when none) |
| `join.overflow` | `none`, `pre_reduce` or `truncate` |
| `join.branches_stored` / `_truncated` / `_reduced` | branches stored in SharedMemory; cut; replaced by a reduced card |

Pipeline and iterative pipeline (present when at least one stage prompt carried history; per-stage detail on the `pipeline.stage.N` span, or the iterative `workflow.structured_context.build_prompt` span, under `history.*`):

| Key | Value |
|-----|-------|
| `history.stages_rendered` | stage prompts that carried history, retry attempts included |
| `history.budget_applied` | `true` when a `history` block was set |
| `history.stages_over_budget` | of those, prompts that needed an overflow strategy |
| `history.entries_truncated` / `history.entries_reduced` | entries cut (per-entry cap or truncate); replaced by a reduced card |
| `history.reduce_llm_calls` | pre-reduce calls made |

Debate (present when a policy applied):

| Key | Value |
|-----|-------|
| `join.mode` = `moderator`, `join.agent_id` | the moderator (synthesis ran) |
| `join.input_tokens`, `join.budget_tokens`, `join.overflow`, `join.positions_stored` / `_truncated` / `_reduced` | the synthesis prompt |
| `debate.response_prompts_bounded` / `_over_budget` | round 2+ response prompts rendered through the bounder; of those, over budget |
| `debate.response_positions_truncated` / `_reduced` | positions cut or reduced across the response prompts |

Cost: the receiving turn's usage and every pre-reduce call are added to `WorkflowCost` (`total_cost_usd`, `total_tokens`, `llm_calls`, `agent_costs_usd[<receiver>]`). A join agent's turn counts its own successful LLM calls (one for a plain completion, more when the agent ran a tool loop). The join agent's model is added to `models_used`; it is not an `agent_results` entry.

## Limits and open items

- **The receiving agent needs `shared_memory_read` to use the stored outputs.** `looms workflow run` registers the communication tools on every workflow agent, and `looms serve` registers them on the agents an `ExecuteWorkflow` call loads from the registry (the registry's own builtin table does not know `shared_memory_read`, so listing it under `tools.builtin` is not enough). An embedder that builds agents itself must wire `builtin.CommunicationTools` against the same SharedMemory store it gives the orchestrator (`orchestration.Config.SharedMemory`).
- **Every executor-managed key carries the workflow id** (`<workflow-id>-stage-N-output`, `<workflow-id>-branch-N-output`, `<workflow-id>-round-R-position-<agent-id>`), so concurrent workflows on one store, which is what a `looms serve` runs, never read, snapshot, reset or delete each other's stashes. A key belongs to a run when it is one of its managed keys or starts with `<workflow-id>-`: an iterative `reset_shared_memory` restart deletes only the run's own keys (and re-seeds the stashes of the stages it keeps), a HITL checkpoint captures only the run's own keys, and keys without a workflow prefix are shared by every workflow on the store: never reset, snapshotted or deleted on a run's behalf. The managed shapes (`-stage-N-output`, `-branch-N-output`, `-round-R-position-*`) are reserved: a key an agent writes in one of those shapes is treated as the named workflow's stash and deleted when that run ends. Client-supplied workflow ids should not be dash-prefixes of each other (`run-1` and `run-1-b`), or the shorter run owns the longer run's agent-written keys. The prompts list the exact keys and ask agents to prefix keys they write themselves with the workflow id; nothing enforces that for agent-written keys.
- **A run's stash is dropped when the run ends.** The orchestrator deletes the workflow's managed keys as soon as `ExecutePattern` or a resumed run returns, a HITL suspension included (the checkpoint carries every stage output and the resume re-seeds the keys from it, so a host that never resumes, such as the gRPC server, does not keep the stash), and the debate engine does the same for its positions, so a long-lived server does not accumulate every run's outputs. Agent-written keys are left alone.
- **A stage whose stash failed is passed in full.** The executors record per stage whether the store `Put` succeeded; a history entry for a stage without a stash is rendered in full with no pointer, as `{{previous}}` is for that stage. A resumed run re-seeds every completed stage first.
- **Without a moderator, a debate's output is not a synthesis** but the last round's confidence summary line, and `checkConsensus` is an average-confidence threshold. The policy bounds the moderator's work; it does not add a moderator.
- **Budget derivation assumes the agent's configured window.** With `max_context_tokens` unset it uses the 200000 / 20000 defaults; set `max_input_tokens` explicitly for a smaller model.

## See Also

- [Multi-Agent Orchestration](../architecture/multi-agent.md): the executors and the collaboration engine
- [Iterative Workflows](workflow-iterative.md): the pipeline stage hand-off (`{{previous}}`, `<workflow-id>-stage-N-output`)
- [Workflow Output Retry](workflow-output-retry.md): bound outputs at the source with `output_policy`
- `examples/reference/workflows/orchestration-patterns/fork-join-presentation.yaml`, `pipeline-history-report.yaml`, `architecture-debate.yaml`
