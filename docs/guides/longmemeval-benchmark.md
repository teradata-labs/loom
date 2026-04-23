# LongMemEval Benchmark Guide

**Version**: v1.2.0 | **Status**: ✅ Implemented (branch `feat/longmemeval-benchmark`)

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Common Tasks](#common-tasks)
  - [Download the Dataset](#download-the-dataset)
  - [Run the Benchmark](#run-the-benchmark)
  - [Score Results](#score-results)
  - [Filter by Question Type](#filter-by-question-type)
- [Run Modes](#run-modes)
- [Results We Observed](#results-we-observed)
- [Troubleshooting](#troubleshooting)

---

## Overview

`loom-longmemeval` evaluates Loom's graph memory against the LongMemEval benchmark (ICLR 2025) — 500 questions across six categories that test long-term memory across multiple conversation sessions. The harness drives each question through a running Loom server via gRPC, then scores each hypothesis with the Loom `JudgeService` using a LongMemEval-specific rubric.

The six question categories are:

| Type | Count | What it tests |
|------|-------|---------------|
| `temporal-reasoning` | 133 | Relative ordering and date arithmetic |
| `multi-session` | 133 | Recall across multiple prior sessions |
| `knowledge-update` | 78 | Handling changed or superseded facts |
| `single-session-user` | 70 | Facts the user stated once |
| `single-session-assistant` | 56 | Facts the assistant stated once |
| `single-session-preference` | 30 | Respecting user preferences in recommendations |

## Prerequisites

- Loom server running (`just build-server && ./bin/looms serve`)
- An LLM provider configured in `~/.loom/looms.yaml` with judge access (examples in this guide use Bedrock Opus 4.6)
- Benchmark binary built: `just build-longmemeval`
- Network access to `huggingface.co` for the one-time dataset download (~285 MB for the oracle set)

## Quick Start

```bash
# 1. Build
just build-server build-longmemeval

# 2. Make sure the server is running
nohup ./bin/looms serve > /tmp/looms.log 2>&1 &

# 3. Download the oracle dataset (one-time, ~285 MB)
./bin/loom-longmemeval download

# 4. Run 30 temporal-reasoning questions
./bin/loom-longmemeval run \
  --mode ingest \
  --types temporal-reasoning \
  --limit 30 \
  --concurrency 2 \
  --output /tmp/out.jsonl \
  --detailed /tmp/out.json

# 5. Score the run
./bin/loom-longmemeval score --results /tmp/out.json --concurrency 5
```

Expected tail of the `score` output:

```
Judge-Scored Results:
  PASS:    24/30 (80%)
  PARTIAL: 5/30
  FAIL:    0/30
  ERRORS:  1/30
  Avg Score: 90.0/100
```

## Common Tasks

### Download the Dataset

```bash
# Default: oracle (~285 MB)
./bin/loom-longmemeval download

# Other sets
./bin/loom-longmemeval download --datasets small
./bin/loom-longmemeval download --datasets medium

# Custom directory
./bin/loom-longmemeval --data-dir /var/data/longmemeval download
```

Files are downloaded to `./data/longmemeval/` by default and are in `.gitignore`.

### Run the Benchmark

Run every question in the oracle set (500 total):

```bash
./bin/loom-longmemeval run \
  --mode ingest \
  --concurrency 2 \
  --output /tmp/full.jsonl \
  --detailed /tmp/full.json
```

Run a specific question range (offset + limit):

```bash
./bin/loom-longmemeval run \
  --types temporal-reasoning \
  --offset 50 --limit 30 \
  --output /tmp/range.jsonl \
  --detailed /tmp/range.json
```

### Score Results

```bash
./bin/loom-longmemeval score --results /tmp/out.json --concurrency 5
```

The scorer:
- Registers a LongMemEval-specific correctness judge on the running server.
- Feeds each (question, ground truth, hypothesis) tuple to `JudgeService.EvaluateWithJudges`.
- Writes a scored JSON file at `<results>.scored.json` containing `verdict` (`PASS`/`PARTIAL`/`FAIL`/`ERROR`/`JUDGE_ERROR`) and `score` (0-100) per entry.

The judge uses the criteria-driven prompt path (requires the `fix(judge): honor JudgeConfig.Criteria` commit from April 2026; without that patch, the judge falls back to a SQL-oriented hardcoded prompt and mis-scores non-SQL tasks).

### Filter by Question Type

Single type:

```bash
./bin/loom-longmemeval run --types knowledge-update --limit 30 ...
```

Multiple types (comma-separated):

```bash
./bin/loom-longmemeval run --types temporal-reasoning,multi-session --limit 60 ...
```

List available types and counts:

```bash
./bin/loom-longmemeval info
```

Expected output:

```
Dataset:  data/longmemeval/longmemeval_oracle.json
Entries:  500

Question Types:
  knowledge-update               78
  multi-session                  133
  single-session-assistant       56
  single-session-preference      30
  single-session-user            70
  temporal-reasoning             133

Total sessions: 948 (avg 1.9/entry)
Total turns:    10960 (avg 11.6/session)
```

## Run Modes

Selected with `--mode`:

| Mode | Behavior | Use when |
|------|----------|----------|
| `ingest` (default) | All haystack sessions + the question run through one agent session. Memory is built during ingestion; the question is asked in the same session. | Evaluating how well graph memory builds from a contiguous stream. |
| `multi-session` | Separate agent session per haystack conversation, then the question is asked in a fresh session. Cross-session recall via graph memory + conversation memory. | Evaluating real-world Loom usage where sessions are bounded. Note: underperforms ingest mode today (see [Results](#results-we-observed)). |
| `context-stuffing` | All session history is injected directly into one Weave call as prompt context; no memory system involved. | Baseline comparison. |

Additional run flags:

| Flag | Default | What it does |
|------|---------|--------------|
| `--isolate` | `true` | Create a fresh `lme-tmp-<qid>` agent per entry; delete it afterward. Ensures no cross-entry memory bleed. |
| `--concurrency` | `3` | Number of entries processed in parallel. Lower to `2` if you see Bedrock throttling. |
| `--server` | `localhost:60051` | Loom gRPC address. |
| `--agent` | `""` | Use a specific agent ID instead of cloning the default. |
| `--offset` | `0` | Skip the first N filtered entries. |
| `--limit` | `0` (all) | Max entries to process after filtering. |

## Results We Observed

Against Bedrock Opus 4.6 (`global.anthropic.claude-opus-4-6-v1`) with the April 2026 memory + judge fixes applied:

| Category | n | PASS % | Avg |
|----------|---|--------|-----|
| temporal-reasoning | 30 | 80% | 90.0 |
| knowledge-update | 30 | 60% | 84.4 |
| multi-session | 30 | 57% | 82.1 |
| single-session-assistant | 30 | 67% | 87.5 |
| single-session-user | 30 | 77% | 85.9 |
| single-session-preference | 30 | 40% | 70.7 |

Key findings from this evaluation campaign:

- **Judge criteria were being ignored before the fix.** The original judge prompt template was hardcoded to a SQL-evaluation framing; `JudgeConfig.Criteria` never reached the judge LLM. Fixing this alone moved temporal-reasoning from 73% → 87% PASS on the same answer data.
- **Temporal ordering regressions at answer time** were caused by the graph memory extractor keeping relative time phrases (`"about 2 months ago"`) in prose. Adding an explicit `event_date` column populated at extraction time, and rendering it as `[YYYY-MM-DD]` in the injected context, flipped two of four targeted wrong-order cases from FAIL to PASS.
- **The remaining failure mode is extraction coverage**, not reasoning. "I don't know" responses dominate the residual FAILs — facts mentioned once in passing (e.g. a GPS issue after a car service, a specific workshop date) were never captured in graph memory.
- **`single-session-preference` scoring is noisy** because the dataset's ground-truth field is a *preference description* (e.g. "prefers Sony-compatible gear"), not a reference answer. The current rubric partially handles this but can score technically-compliant recommendations as PARTIAL when they don't verbally reference the preference.

## Troubleshooting

### `agent lme-tmp-<qid> is already running`

A previous run crashed mid-entry, leaving a registered-but-still-running agent. Restart the Loom server or delete the stuck agent via the admin API. This produces `verdict=ERROR` for affected entries.

### `lookup bedrock-runtime.us-west-2.amazonaws.com: no such host`

Transient DNS or network drop. The Bedrock SDK does not refresh its resolver mid-run, so once a worker hits this it keeps failing until the run ends. If you see this, kill the benchmark and restart once DNS resolves again (`host bedrock-runtime.us-west-2.amazonaws.com`).

### `failed to parse verdict: invalid verdict: `

The judge LLM returned JSON that the scorer couldn't parse. These entries are marked `JUDGE_ERROR` and not counted in PASS/PARTIAL/FAIL. At 5 concurrency we see ~1-2% of entries hit this; rerunning the scorer usually clears them.

### `duplicate column name: embedding` on server startup

The migration numbering changed when `000003_tasks` landed on `main`; older local databases applied the embedding/event-date migrations under the old numbering (3 and 4). Fix the `schema_migrations` table in `~/.loom/loom.db`:

```sql
DELETE FROM schema_migrations WHERE version IN (3, 4);
INSERT INTO schema_migrations (version, applied_at, description) VALUES
  (5, strftime('%s','now'), 'embedding_column (re-numbered)'),
  (6, strftime('%s','now'), 'memory_event_date (re-numbered)');
```

Then restart the server. Tasks migration (version 3) will apply fresh on next startup.

### Dataset not found

`run`, `info`, and `score` auto-detect the dataset at `./data/longmemeval/longmemeval_oracle.json` (then `_s_cleaned.json`, then `_m_cleaned.json`). Override with `--dataset /path/to/file.json` or `--data-dir /path/to/dir`.

## Next Steps

- [Graph Memory Architecture](/docs/architecture/graph-memory.md) — how event-date anchoring and salience-driven recall work internally.
- [Judge CLI Guide](/docs/guides/judge_cli_guide.md) — for registering and running additional custom judges.
