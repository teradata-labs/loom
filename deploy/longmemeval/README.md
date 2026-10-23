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
run mode, concurrency, chunk size, VM sizes, namespace, port, model. Every
manifest is a template rendered by `run-500.sh` (allowlisted `envsubst` via
`render-common.sh`), so overrides apply consistently across the namespace,
server, service, config, and runner job.

Two offline tests cover the rig itself (no cluster, no Bedrock, no spend):

```bash
just lme-rig-test   # both, in strict mode — what CI runs
```

CI runs these on every PR (`LME_RIG_STRICT=1`, so a missing prerequisite fails
rather than printing SKIP and going green).

`slice-loop-test.sh` extracts the runner script from the rendered ConfigMap and
runs it against a synthetic dataset, asserting the behaviours a paid multi-day
run depends on: counts derived from the dataset, resume skipping only completed
chunks, a deterministically-failing entry quarantining its chunk instead of
re-billing the run, every rejected attempt preserved under its own number
outside the scorer's glob, a failed results-volume write stopping the run
instead of reporting success, the incomplete-run sentinel naming every chunk
that is not done and clearing only once every chunk completes, and a revised
dataset refused on resume.

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
- **Run identity:** `run-500.sh` derives a run id from a manifest of everything
  that determines what the benchmark measures (dataset, mode, occurred_at, model,
  image tag and the immutable digest it resolved to, chunk size). Chunk outputs live under
  `/results/runs/<run-id>/`, and the runner verifies the stored `manifest.txt`
  before resuming — a changed configuration gets a fresh run directory instead of
  silently reusing chunks. To knowingly continue a run after e.g. an image
  rebuild: `LME_RUN_ID=<id> LME_ALLOW_MANIFEST_DRIFT=1` (drift is logged to the
  run's `manifest-drift.log`; nothing is ever deleted). The runner also pins the
  dataset itself in `dataset-stats.json` — its SHA-256 plus entry count and
  per-type counts — so a dataset revised underneath a resume (even one with
  identical counts but changed questions or answers) is refused rather than
  mixed with chunks that answered different questions.
- **Build identity:** workloads are pinned to the tag the build pushes, never
  `:latest`. `az acr build` uploads the working tree rather than the commit, so
  a dirty tree is tagged `<commit>-dirty-<fingerprint>` and gets its own run id
  — a build with uncommitted edits can never resume a clean commit's chunks
  under the same name. `run-500.sh` warns when it does this. Because ACR tags
  are mutable, `run-500.sh` resolves the tag to its immutable **digest**, records
  it in the run identity, and deploys both workloads as `image@digest`, so a
  rebuilt or retagged image yields a new run id, and a pod restarted mid-run
  pulls exactly the bytes the run records. The launch stops if the digest cannot
  be resolved. The base image floats and the build runs `apt-get update`, so a
  rebuild almost never reproduces a digest: **to resume a run, re-launch with
  `--skip-build`**. Rebuilding starts a new run id and re-bills every entry.
- **Server state:** the server pod template carries the run id, so starting a
  different run replaces the pod and its `emptyDir` state rather than inheriting
  the previous run's agents and memory. Within one run, each entry's temp agent
  (`lme-tmp-<question_id>`) is removed before it is created, so an entry retried
  after the runner was killed (skipping its cleanup) starts clean instead of
  failing "already running".
- **Per-type counts:** the slice loop derives them from
  `loom-longmemeval info --json` on the dataset it is about to run, so a dataset
  revision cannot silently drive the final chunk past the last entry or stop the
  loop early and omit questions from a published number.
- **Resume:** the runner writes each `(type, offset)` chunk to `*.tmp`, validates
  it (exact entry count, every line parses as JSON, zero errored entries in the
  `-detailed.json`), promotes it with an atomic rename, and only then writes a
  `.done` marker. The marker — not file nonemptiness — is the resume signal, so a
  killed or partial chunk is always redone. Promoted results are never deleted,
  and each rejected attempt's output is moved to
  `runs/<run-id>/rejected/<chunk>.a<NNN>.jsonl` and
  `<chunk>.a<NNN>-detailed.json` (outside the `s500-*.jsonl` glob the scorer
  reads) rather than discarded — it cost real spend and names the entries that
  failed. Every attempt, transient or deterministic, takes the next free number
  `NNN` (001, 002, ...), so a later attempt never overwrites an earlier one. Individual errored entries can no
  longer hide inside a "completed" chunk: validation rejects them and the Job's
  backoff retries the chunk.
- **Attempt budget:** validation is all-or-nothing but resume granularity is the
  whole chunk, so an entry that fails *deterministically* (a content filter on
  that question's text, say) would otherwise re-bill its ~9 healthy neighbours on
  every one of the Job's restarts and still never complete. Each chunk gets
  `LME_MAX_CHUNK_ATTEMPTS` tries (default 3), counted on the PVC so the budget
  spans pod restarts. Only *deterministic* failures count — a chunk that ran
  and produced output failing validation with at least one non-retryable entry
  error. Two cases leave the budget untouched and are bounded by the Job's
  `backoffLimit` instead, because they say nothing about the chunk's entries:
  a harness that could not run at all (nonzero exit before any entry ran, or
  an aborted run), and a chunk whose failed entries *all* carry a retryable
  gRPC status — `Unavailable` (server restart, transport drop),
  `DeadlineExceeded` (a deadline inside the server elapsed; the harness sets
  no per-call deadline of its own, because `Weave` ignores
  `WeaveRequest.timeout_seconds` — the server never reads it, #481) or
  `ResourceExhausted` (load shedding) — which the harness reports with exit
  status 75 (`HARNESS_RC_TRANSIENT` in the runner script). A Bedrock
  throttle or outage that outlasts looms' own LLM retries is in the second
  case: looms returns `ResourceExhausted` for a throttle and `Unavailable` for
  a temporary provider server fault or timeout (HTTP 408/500/502/503/504/529,
  or Bedrock's `InternalServerException`, `ServiceUnavailableException`,
  `ModelNotReadyException`, `ModelTimeoutException`), so it is not charged. A
  deterministic provider refusal (`ValidationException`,
  `AccessDeniedException`, any other 4xx except 408 and 429) stays `Internal`
  and is charged. Because outages and timeouts are never
  charged, a sustained Bedrock outage, or an entry that times out on every
  attempt, is retried until `backoffLimit` and then fails the Job; it never
  quarantines a chunk. Past the budget the chunk is quarantined with a `.failed` marker
  naming the failing entries and how to retry it (`rm` the marker), and later
  passes skip it so the remaining chunks can finish. A quarantined chunk never
  turns a partial run into a passing one: the run exits nonzero. Once only
  quarantined chunks remain, further Job restarts are no-op passes that cost
  nothing, and the Job ends `Failed` when it exhausts `backoffLimit` — which is
  the honest outcome.
- **`RUN-INCOMPLETE.txt`:** every pass that ends with any chunk not done writes
  `runs/<run-id>/RUN-INCOMPLETE.txt`, listing the not-done chunks under
  separate headings: quarantined chunks (budget spent; see each `.failed`
  marker), transient-failed chunks (the harness could not run, or every failed
  entry carried a retryable status), and chunks that failed deterministically
  but are still under the budget. The last two are retried on the next pod
  restart; if the Job has already reached `backoffLimit` there is no next
  restart, and their entries are missing from the run. The file is removed
  only by a pass in which every chunk is done.

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
# Chunks live under runs/<run-id>/ — concatenate ONE run, don't mix runs:
# (check for RUN-INCOMPLETE.txt first — it means entries are missing)
cat ./results/s500/results/runs/<run-id>/s500-*.jsonl > ./results/s500/s500-all.jsonl

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
