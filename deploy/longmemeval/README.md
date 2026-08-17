# LongMemEval on AKS

Runs the full LongMemEval benchmark (500 questions, `LongMemEval_S`) against a
real Loom server + Bedrock on Azure Kubernetes Service. Modeled on
`deploy/benchmark/` (the mock-LLM load-test rig), with the differences a real-LLM,
multi-day run requires: small nodes, real credentials, resumable chunked execution,
and results that survive pod restarts.

**Why not a laptop:** a multi-day run dies on sleep/network blips. This rig's
runner is a k8s Job that resumes at chunk granularity after any interruption.

## Quick start

```bash
# 1. One-time: create the small cluster (~$0.55/hr; scale-down when idle)
bash deploy/longmemeval/setup-cluster.sh

# 2. Launch (builds the image in ACR, deploys server, starts the runner job)
LME_BEDROCK_BEARER_TOKEN=ABSK... bash deploy/longmemeval/run-500.sh

# 3. Watch
kubectl logs -f job/lme-runner -n loom-lme

# 4. Snapshot or collect results (works mid-run)
bash deploy/longmemeval/pull-results.sh

# 5. When finished
bash deploy/longmemeval/scale-down.sh
```

Configuration lives in `lme.env` (override with `lme.env.local`): dataset variant,
run mode, concurrency, chunk size, VM sizes.

## Architecture

```
          AKS loom-lme-aks (D8s_v5 workload pool)
   ┌───────────────┐        ┌─────────────────────────┐
   │ lme-server     │◄─gRPC──┤ lme-runner (Job)         │
   │ (looms, fts5,  │        │ chunked slice loop,      │
   │  graph memory) │        │ resume via results PVC   │
   └───────┬───────┘        └───────────┬─────────────┘
           │ HTTPS                       │
           ▼                             ▼
   AWS Bedrock (Opus 4.6)     PVCs: lme-results (RWX, KEEP),
                                    lme-data (dataset cache)
```

- **Auth:** a Bedrock API key (`ABSK...`) in the `lme-bedrock` Secret, injected as
  `LOOM_LLM_BEDROCK_BEARER_TOKEN`. Loom's Bedrock client uses bearer auth only when
  configured explicitly (ambient `AWS_BEARER_TOKEN_BEDROCK` is deliberately ignored).
- **Time anchoring:** the server config sets `server.allow_time_override: true`; the
  runner passes `--occurred-at=true` so replayed conversations anchor at their
  historical dates (see `docs/guides/longmemeval-benchmark.md`).
- **Resume:** the runner writes one output pair per `(type, offset)` chunk. On any
  restart it skips chunks whose `.jsonl` exists. Failed chunks are cleaned and retried
  by the Job's backoff. Individual errored entries *inside* a completed chunk are
  visible in the `-detailed.json` (non-empty `error` field); repair them with a
  targeted `--offset/--limit` run against the same output naming.

## Time and cost — read before launching

The run is LLM-bound; wall-clock scales with Bedrock quota, not node size.
Observed on the 2026-08 pilot (multi-session mode, `_S` set, Opus 4.6):
~60 min/entry serial, so:

| Concurrency | Est. wall-clock (500 q, multi-session) |
|---|---|
| 6 (default) | ~3.5 days |
| 10 | ~2 days |
| 16 | ~1.3 days (watch for throttling) |

LLM spend is the dominant cost — expect **high hundreds to low thousands USD**
for the full 500 in multi-session mode (measure on a small chunk first; the
pilot slices are the calibration data). Cluster cost is noise by comparison
(~$0.55/hr active). `ingest` mode is cheaper and faster but is a weaker claim
than multi-session for a memory system.

Raise `LME_CONCURRENCY` only alongside your Bedrock TPM/RPM quota; the harness
retries transient throttles but sustained 429s stall entries.

## Scoring

Scoring runs **off-cluster** on pulled results (raw LongMemEval-compatible JSONL):

```bash
bash deploy/longmemeval/pull-results.sh ./results/s500
cat ./results/s500/results/s500-*.jsonl > ./results/s500/s500-all.jsonl

# Official evaluator (paper prompts). Judge model is a disclosed parameter:
#   gpt-5.1 (needs OPENAI_API_KEY)  | azure-gpt (needs AZURE_OPENAI_* env)
#   claude-opus-4-6-bedrock (same-family judge — internal use, disclose if published)
python scripts/longmemeval-eval/evaluate_qa.py \
    ./results/s500/s500-all.jsonl data/longmemeval/longmemeval_s_cleaned.json gpt-5.1
python scripts/longmemeval-eval/print_qa_metrics.py \
    ./results/s500/s500-all.jsonl.eval-results-gpt-5.1 \
    data/longmemeval/longmemeval_s_cleaned.json
```

For a public claim, report: dataset variant (`_S`), all 500 questions including
the 30 abstention questions, mode, model, judge model, commit, and raw results.

## Teardown

`teardown-cluster.sh` destroys the results PVC with the cluster — it refuses to
run until you confirm results are pulled (`LME_CONFIRM_RESULTS_PULLED=1`).
Prefer `scale-down.sh` between runs.
