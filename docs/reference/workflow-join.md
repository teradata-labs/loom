# Workflow Join Policy Reference

**Version**: v1.4.0
**Status**: ✅ Implemented (fork-join and parallel patterns)

A join policy bounds the hand-off from a fan-out pattern's branches to its join step, and can run that step through a registered agent instead of one raw merge-LLM call.

## Table of Contents

- [The problem it solves](#the-problem-it-solves)
- [Quick Reference](#quick-reference)
- [How the hand-off is bounded](#how-the-hand-off-is-bounded)
- [Join agent](#join-agent)
- [Prompt template](#prompt-template)
- [Overflow strategies](#overflow-strategies)
- [Configuration Reference](#configuration-reference)
- [Loader errors](#loader-errors)
- [Builder API](#builder-api)
- [Result metadata and cost](#result-metadata-and-cost)
- [Limits and open items](#limits-and-open-items)
- [See Also](#see-also)

## The problem it solves

Without a join policy, the LLM merge strategies (`consensus`, `voting`, `summary`, `best`) build one prompt that contains every branch output in full and send it to the orchestrator merge LLM as a single raw call. That call has no context relief, no tools and no size check. Once the branches are chatty enough, the provider refuses the prompt (`prompt is too long` / `context_length_exceeded`) and the workflow fails after every branch has already been paid for.

Pipelines already avoid this between stages: each stage's full output is stored in SharedMemory under `stage-N-output` and the next stage receives an 8 KiB inline copy plus a note on how to fetch the rest (see [Iterative Workflows](workflow-iterative.md)). The join policy brings the same hybrid hand-off to fork-join and parallel, adds a whole-input token budget, and lets the join be a real agent.

## Quick Reference

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
| Store | Every non-failed branch output is written to SharedMemory (`workflow` namespace, key `<workflow-id>-branch-N-output`) when the orchestrator has a SharedMemory store | 0 |
| Cap | Each inline copy over `max_branch_bytes` is cut at a line boundary; the section carries a notice naming the key | 0 |
| Budget | The rendered input is token-counted against `max_input_tokens`; over budget, `overflow_strategy` runs | pre_reduce: 1 per oversize branch |
| Join | The join agent's `Chat` turn, or one merge-LLM call when no agent is set | 1 |

## How the hand-off is bounded

1. **Store.** With a SharedMemory store on the orchestrator, each branch's full output is put in the `workflow` namespace under `<workflow-id>-branch-N-output` (N is the configured branch order, 1-based). Failed branches (error results) are not stored. Without a store the inline copy is all the join gets, and notices say so.
2. **Cap.** The inline copy of a branch longer than `max_branch_bytes` is cut at the last line break in its back half (never inside a UTF-8 sequence). The section then ends with `[Branch output truncated: X of Y bytes shown. Full output in SharedMemory: shared_memory_read(namespace="workflow", key="...")]`.
3. **Budget.** The whole rendered prompt (template, header, every section) is counted with the same tokenizer the agent memory uses. If it exceeds the budget, the overflow strategy brings it under. The per-branch share is `(budget - everything that is not branch text) / branches`, discounted 5% so the re-rendered input lands under the budget. A share below 64 tokens is an error, since a card that small carries nothing.

## Join agent

`join.agent_id` names a registered agent. The join then runs as that agent's conversation turn (`agent.Chat`) under session `<workflow-id>-join-<agent-id>`, which means:

- the agent's own system prompt and tools apply, including `shared_memory_read` when the agent has it, so it can pull any branch back in full;
- the agent's context relief and the provider context-too-long backstop apply, instead of a bare provider call;
- its output is the workflow's `merged_output`, and it can build large deliverables through tools rather than as one completion.

The agent gets an `## AVAILABLE CONTEXT (SharedMemory)` header listing every stored key before the sections. The join agent may also be one of the branch agents. It is validated before any branch runs, so a missing join agent fails the workflow before anything is paid for.

With a join agent set, the agent always runs, whatever `merge_strategy` says. The strategy only selects the default prompt when `prompt_template` is empty.

## Prompt template

`join.prompt_template` is filled by plain substitution:

| Placeholder | Value |
|-------------|-------|
| `{{prompt}}` | the fan-out prompt (fork-join); empty for parallel |
| `{{branch_count}}` | number of branches |
| `{{branches}}` | the rendered sections, one per branch, in configured order |

`{{prompt}}` and `{{branch_count}}` are replaced first and `{{branches}}` last by splicing, so placeholder-like text inside a branch output is never substituted. A template without `{{branches}}` gets the sections appended, so the join never runs blind.

When the template is empty, the merge strategy's prompt is used with the same bounded sections: the `summary`, `consensus`, `voting` and `best` wording matches the raw merge path; `concatenate`, `first` and an unset strategy get a plain "combine these outputs" instruction (they only reach the LLM with a join agent).

Each section looks like:

```
### Branch 2: agent sales
<inline copy>
[Branch output truncated: 4096 of 31870 bytes shown. Full output in SharedMemory: shared_memory_read(namespace="workflow", key="fork-join-7f3a-branch-2-output")]
```

A failed branch renders as `### Branch N: agent X (FAILED)` followed by its error text.

## Overflow strategies

| `overflow_strategy` | Behavior | Cost |
|---------------------|----------|------|
| `pre_reduce` (default when unset) | Each branch over its share is reduced to a card of at most that share by an LLM: the join agent's compressor-role LLM (its main LLM when none is configured), else the orchestrator merge LLM. Reductions run in parallel under the orchestrator LLM semaphore. A reduction that fails or still exceeds the share is truncated to the share, so the result always fits. With no LLM available at all, falls back to truncate. | one call per oversize branch |
| `truncate` | Each branch over its share is cut to it at a line boundary with the SharedMemory notice. | none |
| `fail` | The workflow fails with `ErrJoinInputOverBudget` before the join call. | none |

The budget check only runs when a budget exists: `max_input_tokens` set, or a join agent to derive one from. With neither there is no check, which is the pre-existing behavior of the raw merge path.

## Configuration Reference

Proto: `JoinPolicy` in `proto/loom/v1/orchestration.proto`, field `join_policy` on `ForkJoinPattern` (6) and `ParallelPattern` (4).

| YAML | Proto | Default | Meaning |
|------|-------|---------|---------|
| `agent_id` | `agent_id` | `""` | Registered agent that performs the join. Empty keeps the raw merge-LLM call. |
| `prompt_template` | `prompt_template` | strategy default | Join prompt; see placeholders above. |
| `max_branch_bytes` | `max_branch_bytes` | `0` = 8192 | Per-branch inline cap in bytes. Must be `>= 0`. |
| `max_input_tokens` | `max_input_tokens` | `0` = derived | Whole-input token budget. `0` derives half of the join agent's usable window (`max_context_tokens` less `reserved_output_tokens`, defaults 200000 and 20000); with no agent, `0` disables the check. Must be `>= 0`. |
| `overflow_strategy` | `overflow_strategy` | `pre_reduce` | `pre_reduce`, `truncate` or `fail`. Also accepts `pre-reduce` and `reduce`. |

Without a join agent, a policy only affects the LLM merge strategies. `concatenate` and `first` return the branch text itself as the workflow output and are never cut; the executor logs a warning and ignores the policy (the YAML loader rejects that combination outright).

## Loader errors

The loader rejects dead configuration rather than carrying it:

| YAML | Error |
|------|-------|
| `join: {}` | `spec.join sets nothing` |
| `max_branch_bytes: -1` / `max_input_tokens: -1` | `must be >= 0` |
| `overflow_strategy: explode` | `unknown value "explode" (expected pre_reduce, truncate or fail)` |
| `overflow_strategy` without `agent_id` and without `max_input_tokens` | `can never fire` |
| `join` without `agent_id` on `merge_strategy: concatenate` or `first` | `has no effect on merge_strategy concatenate` |
| unknown key | `spec.join.<key> is not a join field` |
| `max_input_tokens: 10.5` | `must be a whole number` |

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
```

## Result metadata and cost

When a policy is active the `WorkflowResult` carries these metadata keys, and the `fork_join.join` / `parallel.merge` span gets them as attributes:

| Key | Value |
|-----|-------|
| `join.mode` | `agent` or `merge_llm` |
| `join.agent_id` | the join agent (agent mode only) |
| `join.input_tokens` | tokens of the prompt actually sent |
| `join.budget_tokens` | the budget applied; `0` when none |
| `join.overflow` | `none`, `pre_reduce` or `truncate` |
| `join.branches_stored` | branches whose full output is in SharedMemory |
| `join.branches_truncated` | branches whose inline copy was cut |
| `join.branches_reduced` | branches replaced by an LLM-reduced card |

The join's own LLM usage (the join turn plus every successful pre-reduce call) is added to `WorkflowCost`: `total_cost_usd`, `total_tokens`, `llm_calls`, and `agent_costs_usd[<join agent>]`. The join agent's model is added to `models_used`. Branch results are unchanged; the join agent is not an `agent_results` entry.

## Limits and open items

- **The join agent needs `shared_memory_read` to use the stored outputs.** The policy stores and announces the keys; it does not inject the tool. Give the join agent `shared_memory_read` in its tools config, and wire the same SharedMemory store into the orchestrator (`orchestration.Config.SharedMemory`) and the agent tools.
- **`join.llm_calls` counts the join turn as one call.** A join agent that runs a tool loop makes more; the turn's usage is still summed in full.
- **Pipeline `{{history}}` and `pass_full_history` are not capped.** They pass every prior stage output in full. A final pipeline stage that reads history has the same exposure this policy removes for fork-join and parallel.
- **Pipeline stage keys (`stage-N-output`) are not scoped by workflow ID**; join branch keys are. Concurrent pipelines in one SharedMemory store overwrite each other's stashes.
- **Budget derivation assumes the agent's configured window.** With `max_context_tokens` unset it uses the 200000 / 20000 defaults; set `max_input_tokens` explicitly for a smaller model.

## See Also

- [Multi-Agent Orchestration](../architecture/multi-agent.md): fork-join and parallel executors
- [Iterative Workflows](workflow-iterative.md): the pipeline stage hand-off this mirrors
- [Workflow Output Retry](workflow-output-retry.md): bound branch outputs at the source with `output_policy`
- `examples/reference/workflows/orchestration-patterns/fork-join-presentation.yaml`
