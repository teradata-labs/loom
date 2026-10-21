# Arize Phoenix Observability Guide

**Version**: v1.4.0

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Common Tasks](#common-tasks)
- [What a Turn Looks Like in Phoenix](#what-a-turn-looks-like-in-phoenix)
- [Verification Checklist](#verification-checklist)
- [VM Rollout](#vm-rollout)
- [Troubleshooting](#troubleshooting)
- [Next Steps](#next-steps)

## Overview

Loom exports its traces to [Arize Phoenix](https://github.com/Arize-ai/phoenix) over OTLP/HTTP with OpenInference attributes, so Phoenix shows each `Weave` turn as one trace with typed LLM, agent, tool and retriever spans, token counts that include cached prompt tokens, and cost. Judge evaluations can be posted to Phoenix as trace annotations.

**Status**

✅ Implemented (tested, and checked against Phoenix 20.22.0):
- One trace per turn, with the trace ID returned in the `Weave` / `StreamWeave` response (`traceId`)
- OpenInference span kinds, `input.value` / `output.value`, token counts with cached prompt tokens, cost
- Nested spans: LLM and tool calls under the conversation loop; memory recall, extraction, context relief and salience boosts as their own spans; `llm.role` on every LLM call
- Experiment tags from `WeaveRequest.context` on the server and agent spans
- The embedded `eval_runs` record kept while exporting (`otlp_record_eval_runs`)
- Each LLM call's messages, tool schemas and parameters, opt-in (`otlp_capture_llm_messages`)
- Judge scores posted as Phoenix trace annotations (`phoenix_base_url`)

📋 Planned:
- Experiment-harness telemetry and per-game annotations (Mastermind, card sorting), after the harness moves to its own repository
- The VM rollout below has not been run yet

Phoenix 20.22.0 is the version these checks used. Re-run the [verification checklist](#verification-checklist) before moving to another version.

## Prerequisites

- `looms` built from this repository (`go build -tags fts5 ./cmd/looms`)
- An LLM provider configured in the example config (Bedrock by default)
- Phoenix 20.22.0, through Docker or pip
- For judge evaluations from the command line: [grpcurl](https://github.com/fullstorydev/grpcurl); the example configs enable gRPC reflection

## Quick Start

### 1. Start Phoenix

Docker publishes only the UI and OTLP/HTTP port, on the loopback address:

```bash
docker run -d --name phoenix \
  -p 127.0.0.1:6006:6006 \
  -e PHOENIX_WORKING_DIR=/data \
  -v phoenix_data:/data \
  arizephoenix/phoenix:version-20.22.0
curl -fsS http://127.0.0.1:6006/healthz
```

Without Docker (Windows example):

```powershell
python -m pip install arize-phoenix==20.22.0
$env:PHOENIX_HOST = "127.0.0.1"
phoenix serve
```

The pip server also listens for OTLP/gRPC on TCP 4317 on every interface; `PHOENIX_HOST` only moves the HTTP listener. Block inbound 4317 before starting it, and stop it when you are done.

### 2. Start Loom

```powershell
looms serve --config examples\config\otlp-phoenix.yaml
```

`otlp-phoenix.yaml` exports every span to the `loom-dev` project with 200-rune previews. For experiment runs use `otlp-phoenix-research.yaml` (see [Task 6](#task-6-run-an-experiment-with-the-research-preset)).

### 3. Send a turn

```powershell
$body = @{ query = "Say hello and name one tool you have."; sessionId = "phoenix-demo"; agentId = "<agent-name>" } | ConvertTo-Json
$resp = Invoke-RestMethod -Method Post -Uri http://127.0.0.1:5006/v1/weave -ContentType application/json -Body $body
$resp.traceId
```

Expected output: a 32-character lowercase hex trace ID, for example `f035ee4067a1c2d3b4e5f60718293a4b`. Open http://127.0.0.1:6006, pick the `loom-dev` project, and search for that trace ID.

## Common Tasks

### Task 1: Export a project's spans

Phoenix returns at most 100 spans per request by default and at most 1000 with `limit`; follow `next_cursor` for the rest. A two-turn session with one tool call produces about 110 spans.

```python
import json
import urllib.request

def phoenix_spans(project, base="http://127.0.0.1:6006", **filters):
    """All spans of a project; filters are extra query parameters (for example trace_id=...)."""
    spans, cursor = [], None
    while True:
        params = {"limit": "1000", **filters}
        if cursor:
            params["cursor"] = cursor
        query = "&".join(f"{k}={v}" for k, v in params.items())
        with urllib.request.urlopen(f"{base}/v1/projects/{project}/spans?{query}") as r:
            page = json.load(r)
        spans += page["data"]
        cursor = page.get("next_cursor")
        if not cursor:
            return spans

spans = phoenix_spans("loom-dev")
print(len(spans), "spans")
```

Expected output: `114 spans` for the two-turn example. Loom-specific attributes are `loom.`-prefixed in the export (`loom.llm.role`, `loom.recall.outcome`); OpenInference and OTel keys are not (`input.value`, `llm.token_count.prompt`, `gen_ai.request.model`, `session.id`).

### Task 2: Tag turns with experiment metadata

Add whitelisted keys to the request's `context` map. Loom copies `experiment_id`, `replication_id`, `condition`, `game_name`, `phase`, `call_number`, `trial` and `run_id` (values up to 256 runes) into the OpenInference `metadata` attribute of `server.Weave` and `agent.chat`; other keys are ignored. Values must be strings.

```powershell
$body = @{
  query = "Guess the code."
  sessionId = "mm-001"
  agentId = "<agent-name>"
  context = @{ experiment_id = "mm-001"; condition = "memory_on"; call_number = "3" }
} | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:5006/v1/weave -ContentType application/json -Body $body
```

In Phoenix the span attributes show `metadata.condition = memory_on`, and the trace can be filtered on it.

### Task 3: Post judge scores to Phoenix

Set `observability.phoenix_base_url` (the research preset does) and restart Loom. The API key, when Phoenix requires one, comes from the keyring key `phoenix_api_key` or the `PHOENIX_API_KEY` environment variable.

Register a judge and evaluate a turn, passing its trace ID:

```bash
grpcurl -plaintext -d '{"config":{"id":"quality","name":"Quality","criteria":"Correct and complete answer"}}' \
  127.0.0.1:60051 loom.v1.JudgeService/RegisterJudge

grpcurl -plaintext -d '{"context":{"prompt":"Say hello and name one tool you have.","response":"Hello! One tool I have is file_read.","traceId":"<traceId from the Weave response>"},"judgeIds":["quality"]}' \
  127.0.0.1:60051 loom.v1.JudgeService/EvaluateWithJudges
```

The trace then carries one annotation per score: `loom.overall`, `loom.quality`, `loom.correctness`, `loom.completeness` and `loom.safety` (0–100), labeled with the verdict (`PASS`, `PARTIAL`, `FAIL`) and the judge's reasoning as explanation. Evaluating again with the same judge replaces its annotations. The judge's own LLM call appears in Phoenix as an `llm.completion` span with `llm.role = judge` under `judge.evaluation`.

Annotations are sent from a background queue and never slow the evaluation down. Phoenix holds an annotation whose trace has not arrived yet and retries it (Phoenix 20.22.0: 60 attempts, 10 seconds apart), which covers the few seconds the OTLP exporter batches spans. That queue is in Phoenix's memory, so an annotation still waiting when Phoenix restarts is lost.

### Task 4: Read memory activity from a trace

The memory spans carry their results as attributes:

| Span | Attributes |
|---|---|
| `graph_memory.recall` (RETRIEVER) | `loom.recall.outcome` (`injected`, `fallback_injected` for the session-start fallback, `no_candidates`, `rerank_none`, `vector_error`, ...), `loom.recall.selected_ids`, `loom.recall.injected_tokens`, `loom.recall.vector_candidates` when the agent has an embedder; the query as `input.value` and the injected block as `output.value` when memories were injected |
| `graph_memory.extract` | `loom.extract.outcome` (`completed`, `no_messages`, `llm_error`, `parse_error`, `store_error`), `loom.extract.entities_created`, `loom.extract.relationships_created`, `loom.extract.memories_created` |
| `memory.release_pressure` | start and end tokens, marks, and the evict / fold / offload operations |
| `llm.completion` | `loom.llm.role`: `agent` for the agent's own calls; `memory_query`, `memory_rerank`, `memory_extract`, `memory_compress`, `memory_search_rerank`, `skills_router` for Loom's own calls; `judge` |

Memory LLM cost is the sum of `llm.cost.total` over `llm.completion` spans whose `loom.llm.role` starts with `memory_`.

### Task 5: See exactly what the model received

By default an `llm.completion` span shows a preview of the call's newest input. With

```yaml
observability:
  otlp_capture_llm_messages: true
```

each LLM span also records the whole request, which Phoenix shows in the span's messages, tools and invocation parameters:
- the system prompt, the history, injected memory blocks, tool calls with their arguments and tool results;
- the tool schemas offered;
- the model, max tokens, temperature and thinking (Bedrock, Anthropic and OpenAI-compatible providers);
- the response.

Each content follows `otlp_max_content_length`. A span keeps at most 256 KiB of messages and 64 KiB of tool schemas. When a long conversation is over that, the system prompt and the newest messages are kept, and a message with role `loom` says how many earlier ones were left out (also counted in `loom.llm.messages.omitted`). Redaction applies to all of it. Phoenix's database grows with every call's full prompt, so turn it on for research runs rather than by default, and check the span sizes on a small run first. The local `eval_runs` record does not keep these attributes.

### Task 6: Run an experiment with the research preset

```powershell
looms serve --config examples\config\otlp-phoenix-research.yaml
```

Compared with `otlp-phoenix.yaml` it keeps previews up to 32768 runes, records each LLM call's messages (Task 5), writes the embedded `eval_runs` record to `./observability.db` for the `trace_reports` analysis, posts judge scores to Phoenix, and uses the `loom-research` project. PII redaction stays on in the export; the local `eval_runs` record is not redacted.

The `Weave` cost (`cost.totalCostUsd`) is the total of the turn's conversation calls. Builds before the trace-correlation change reported only the turn's last LLM call there, so do not compare cost figures across that change. Loom's memory calls are not in that total; use Task 4.

## What a Turn Looks Like in Phoenix

```
server.Weave                          CHAIN    the user's message in, the response out
└── agent.chat                        AGENT    agent.name, session.id, metadata
    └── agent.conversation_loop       CHAIN
        ├── graph_memory.recall       RETRIEVER
        │   ├── llm.completion        LLM      role memory_query
        │   └── llm.completion        LLM      role memory_rerank
        ├── llm.completion            LLM      role agent
        ├── agent.tool_execution      TOOL     arguments in, result out
        ├── llm.completion            LLM      role agent
        └── graph_memory.extract      CHAIN    may finish after the response
            └── llm.completion        LLM      role memory_extract
```

Phoenix's trace token totals count only LLM spans, so `agent.chat` does not double the turn's tokens.

Streaming requests have `server.StreamWeave` as the root instead of `server.Weave`. The root carries the request's message and final response, which is what Phoenix shows in its trace list and as a session's first input and last output; turns that a workflow injects into an agent (sub-agent messages) start at `agent.chat` instead.

## Verification Checklist

Run this after changing Loom's tracing code or the Phoenix version. Use a three-turn session with the same `sessionId`, at least one turn calling a tool, and export the spans with paging (Task 1).

1. Every `Weave` response has a 32-hex `traceId` that matches a Phoenix trace.
2. The tree matches [What a Turn Looks Like in Phoenix](#what-a-turn-looks-like-in-phoenix), and no exported span has a parent that is missing from the export.
3. A trace's prompt and completion totals equal the sum of its `llm.completion` spans.
4. `agent.chat`, `llm.completion` and `agent.tool_execution` spans have `input.value` and `output.value`; a recall span has `output.value` when it injected memories.
5. All three traces share `session.id`.
6. Stop Phoenix, send a fourth turn: it completes. Start Phoenix again: a fifth turn exports.
7. With the research preset, `observability.db` has one `eval_runs` row per finished span, and after a clean shutdown its `evals` row is `completed`.
8. A judge evaluation with the turn's `traceId` (Task 3) shows the `loom.*` annotations on that trace.

### Last run

2026-10-16, Loom built from `feat/phoenix-observability` merged with `feat/agent-tools-none` and `fix/extraction-without-cancel`; Phoenix 20.22.0 (pip, SQLite) on Windows; Bedrock Claude Sonnet 4.5; `otlp-phoenix-research.yaml` settings (32768-rune previews, `otlp_record_eval_runs`, `phoenix_base_url`). Five turns on one session: turn 2 read a file with `file_read`, turn 4 ran while Phoenix was stopped.

| # | Result |
| --- | --- |
| 1 | ✅ Every `traceId` is 32-hex. Turns 1, 2 and 5 are complete traces in Phoenix (18, 52 and 31 spans). |
| 2 | ✅ Turn 2 matches the tree above: `graph_memory.recall` RETRIEVER (with its `memory_query` LLM call), `llm.completion` agent, `agent.tool_execution` TOOL, `graph_memory.extract` CHAIN with its `memory_extract` LLM call and `sqlite.graph_memory.*` store spans, all under `agent.conversation_loop`. The builtin `file_read` runs in-process, so the tool span has no children. No orphan in any complete trace. |
| 3 | ✅ Phoenix's session totals equal the sum of the `llm.completion` spans: 26,243 / 1,146 prompt / completion tokens for the session, 1,363 / 105 for a zero-tool agent, 35,698 / 5,623 for a Mastermind game. No non-LLM span carries token counts. |
| 4 | ✅ `input.value` and `output.value` on 12 of 12 `agent.chat`, 55 of 55 `llm.completion` and the tool span; recall spans have `input.value` on all 13 and `output.value` on the 7 that injected memories. Longest value 12,029 runes, under the 32,768 bound. |
| 5 | ✅ The complete traces share `session.id`; only `server.Weave` and `agent.chat` carry it. |
| 6 | ✅ Turn 4 answered in 5.9 s with Phoenix stopped (turns 1, 3 and 5: 5.7 s median), no error in the server log; turn 5 exported completely after the restart. ⚠️ Spans in flight when Phoenix stopped were dropped, not re-sent: all of turn 4 and the unflushed part of turn 3 (its `server.Weave` and `agent.chat`), so Phoenix shows the session with three traces. The server log has no warning for the failed exports. |
| 7 | ✅ `eval_runs` holds every span Phoenix received (339 of 339 span IDs) plus 47 it never received, including turn 4; the `evals` row is `completed` after shutdown. `analyze_mastermind.py --compare-batches --observability-db --loom-db` and the `trace_scripts` session and turn queries ran on a one-game Mastermind run recorded this way and on a second game recorded with `mode: embedded`, with the same columns; turns of the same shape count the same calls (one agent call plus memory query, rerank and extract). ⚠️ Both scripts assign rows to turns by position: the LLM calls of the server's startup skills-index rebuild (`llm.role = skills_router`) landed inside the first session's range and were counted as agent calls. The rows carry `llm.role`, which the scripts do not read yet. |
| 8 | ✅ `loom.overall`, `loom.correctness`, `loom.completeness`, `loom.quality` and `loom.safety` (LLM annotator) on turn 2's trace; a CODE session annotation on the session. |

Also checked: an agent created through the API with `tools: {none: true}` and graph memory on made three LLM calls with `loom.llm.tools.count = 0` and no TOOL span. Its reply still named artifact tools, taken from the ROM text in its system prompt; experiment agents should keep `rom: none`. Each server start also exports its skills-index rebuild LLM calls (`loom.llm.role = skills_router`) without their parent span.

## VM Rollout

📋 Not run yet. The build for it exists (the merge above, built with `go build -tags fts5` for `linux/amd64` with `CGO_ENABLED=0`; record its commit and SHA-256 with the run). Planned layout: Phoenix and PostgreSQL in Docker on a private network, Phoenix reachable only on the VM's loopback address, and the workstation connecting through an SSH tunnel. Back up the Loom databases and the previous binary and configuration first, and do not restart Loom while an experiment is running.

```bash
docker network create phoenix-net
install -d -m 700 ~/phoenix
password="$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32)"
cat >~/phoenix/pg.env <<EOF
POSTGRES_USER=phoenix
POSTGRES_DB=phoenix
POSTGRES_PASSWORD=$password
EOF
cat >~/phoenix/phoenix.env <<EOF
PHOENIX_POSTGRES_HOST=phoenix-db
PHOENIX_POSTGRES_PORT=5432
PHOENIX_POSTGRES_USER=phoenix
PHOENIX_POSTGRES_PASSWORD=$password
PHOENIX_POSTGRES_DB=phoenix
EOF
chmod 600 ~/phoenix/pg.env ~/phoenix/phoenix.env
unset password

docker run -d --name phoenix-db --network phoenix-net --restart unless-stopped \
  --env-file ~/phoenix/pg.env -v phoenix_pg:/var/lib/postgresql/data postgres:16
docker run -d --name phoenix --network phoenix-net --restart unless-stopped \
  -p 127.0.0.1:6006:6006 --env-file ~/phoenix/phoenix.env \
  arizephoenix/phoenix:version-20.22.0
```

The password is alphanumeric because Phoenix's discrete `PHOENIX_POSTGRES_*` variables are used instead of a database URL, where reserved characters would need escaping. Neither 5432 nor 4317 is published.

Before the first experiment:

```bash
ss -ltnp                                   # 6006 only on 127.0.0.1
docker exec phoenix-db psql -U phoenix -d phoenix -c 'select count(*) from spans;'
```

From the workstation, ports 6006, 4317 and 5432 on the VM must refuse connections; through the tunnel Phoenix answers:

```powershell
ssh -N -L 127.0.0.1:16006:127.0.0.1:6006 <user>@<vm>
curl.exe http://127.0.0.1:16006/healthz
```

Record the Loom commit, the `looms` binary SHA-256 and both image tags with each run.

**Rollback**: set `observability.mode: embedded` and restart Loom. The `eval_runs` record continues; export stops.

## Troubleshooting

**No traces in Phoenix.** Check that `otlp_endpoint` ends in `/v1/traces` and that the project in `x-project-name` is the one you are looking at. Loom logs `Failed to create OTLP tracer` at startup when the exporter cannot be built.

**Spans missing from an export.** The export stopped at the first page; follow `next_cursor` (Task 1).

**A `Weave` response has no `traceId`.** The server runs without a tracer (`observability.enabled: false` or `mode: none`).

**No annotations after an evaluation.** Check that `phoenix_base_url` is set (Loom logs `Phoenix annotations enabled` at startup), that the evaluation's `traceId` is the 32-hex ID from the `Weave` response, and look for `phoenix annotations not sent` warnings, which carry the HTTP status.

**Port 4317 open on the network.** A pip-installed Phoenix opens it on every interface. Use Docker, or block inbound 4317.

## Next Steps

- [OTLP integration architecture](../../architecture/otlp-integration.md): attribute mapping, span tree, redaction and content bounds
- [Embedded observability](embedded-observability-guide.md): the local `eval_runs` store
- [Observability with Hawk](observability.md)
