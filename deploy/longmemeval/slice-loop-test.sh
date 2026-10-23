#!/usr/bin/env bash
# slice-loop-test.sh — Drives the real runner slice loop offline.
#
# The loop is where a multi-day, paid run can silently go wrong, so it is
# tested rather than reasoned about: the script under test is extracted from
# the rendered ConfigMap (not a copy), and only the harness is stubbed — the
# real `loom-longmemeval info --json` still supplies the per-type counts, so
# the derived-counts contract is exercised end to end.
#
# Covers:
#   1. counts come from the dataset (a dataset whose per-type counts differ
#      from any hardcoded table is walked exactly, with no overrun or omission)
#   2. resume skips only chunks with a .done marker
#   3. a deterministically-failing entry quarantines its chunk after
#      MAX_CHUNK_ATTEMPTS instead of re-billing its healthy neighbours forever,
#      and the remaining chunks still complete
#   4. a rejected attempt's output is preserved outside the scorer's glob
#   5. a dataset swapped underneath a resume is refused
#   6. a mid-chunk transient outage (every entry failing with a retryable
#      gRPC status, harness exit HARNESS_RC_TRANSIENT) never consumes the
#      deterministic budget, however many passes it lasts
#   7. RUN-INCOMPLETE.txt is written whenever a chunk is not done — a chunk
#      still failing transiently, one under its deterministic budget, or a
#      quarantined one — names each under its own heading, survives such a
#      pass even when it pre-exists, and is removed only once every chunk
#      completes
#
# No cluster or Bedrock access needed. Usage:
#   bash deploy/longmemeval/slice-loop-test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=render-common.sh
source "${SCRIPT_DIR}/render-common.sh"

# LME_RIG_STRICT=1 (CI) turns a missing prerequisite into a failure. A green
# no-op is worse than a red one here: these are the only regression tests for
# a runner that spends real money.
STRICT="${LME_RIG_STRICT:-0}"
missing() {
    if [[ "${STRICT}" == "1" ]]; then
        echo "FAIL: ${1} is required (LME_RIG_STRICT=1)"; exit 1
    fi
    echo "SKIP: ${1} not available"; exit 0
}
for tool in jq python3 go; do
    command -v "${tool}" >/dev/null 2>&1 || missing "${tool}"
done
python3 -c 'import yaml' 2>/dev/null || missing "python3 pyyaml"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

failures=0
fail() { echo "FAIL: $*"; failures=$((failures + 1)); }
ok() { echo "  ok: $*"; }

# ── Extract the script under test from the rendered ConfigMap ────────────────
export LME_NAMESPACE="lme-slice-test" LME_IMAGE="t/i" LME_IMAGE_TAG="t" \
    LME_GRPC_PORT="50051" LME_MODEL="m" LME_DATASET="small" \
    LME_DATASET_FILE="d.json" LME_MODE="ingest" LME_CONCURRENCY="1" \
    LME_CHUNK="2" LME_OCCURRED_AT="false" LME_RUN_ID="sliceTest01" \
    LME_RUN_MANIFEST="test-manifest" LME_ALLOW_MANIFEST_DRIFT="0" \
    LME_MAX_CHUNK_ATTEMPTS="2" LME_CONFIG_HASH="slicetestcfg"

lme_render "${SCRIPT_DIR}/runner-script.yaml" > "${TMP_DIR}/cm.yaml" \
    || { echo "FAIL: runner-script.yaml did not render"; exit 1; }
python3 -c 'import sys, yaml; sys.stdout.write(yaml.safe_load(open(sys.argv[1]))["data"]["run-slices.sh"])' \
    "${TMP_DIR}/cm.yaml" > "${TMP_DIR}/run-slices.sh" \
    || { echo "FAIL: could not extract run-slices.sh"; exit 1; }

# ── Real harness for `info`, stub for `run` ─────────────────────────────────
echo "Building loom-longmemeval (used for the real info --json contract)..."
go build -tags fts5 -o "${TMP_DIR}/loom-longmemeval" "${REPO_ROOT}/cmd/loom-longmemeval" \
    || { echo "FAIL: could not build loom-longmemeval"; exit 1; }

# Dataset with per-type counts that match no hardcoded table: 5 + 3 entries.
# With CHUNK=2 that is chunks of 2,2,1 (alpha) and 2,1 (beta) — the ragged
# final chunk a wrong hardcoded count would overrun or truncate.
python3 - "${TMP_DIR}/dataset.json" <<'PY'
import json, sys
entries = []
for t, n in (("alpha-type", 5), ("beta-type", 3)):
    for i in range(n):
        entries.append({
            "question_id": f"{t}-{i}",
            "question_type": t,
            "question": "q",
            "answer": "a",
            "haystack_sessions": [[{"role": "user", "content": "hi"}]],
        })
json.dump(entries, open(sys.argv[1], "w"))
PY

# The transient exit status comes from the script under test, never a copy:
# TestRunnerScriptTransientExitCode pins it to the harness's ExitTransient, so
# the stub speaks exactly the protocol the real binary does.
HARNESS_RC_TRANSIENT="$(sed -n 's/^ *HARNESS_RC_TRANSIENT=\([0-9][0-9]*\) *$/\1/p' "${TMP_DIR}/run-slices.sh")"
[[ -n "${HARNESS_RC_TRANSIENT}" ]] \
    || { echo "FAIL: run-slices.sh does not define HARNESS_RC_TRANSIENT"; exit 1; }
export HARNESS_RC_TRANSIENT

# The stub answers `info` from the real binary and synthesizes `run` output.
# POISON_IDS (space separated) are recorded as per-entry errors, exactly as
# the harness does: omitted from the .jsonl, present with an "error" in the
# detailed JSON. Every invocation is logged so re-billing is observable.
#
# TRANSIENT_IDS (space separated) simulate an outage: for the first
# TRANSIENT_PASSES invocations of a chunk holding one of them, EVERY entry in
# the chunk fails with a retryable gRPC status and the stub exits
# HARNESS_RC_TRANSIENT, as the real harness does; after that the chunk
# succeeds.
#
# CRASH_IDS (space separated) simulate a harness that could not run at all:
# a chunk holding one of them exits 1 without writing any output.
cat > "${TMP_DIR}/harness-stub.sh" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail
REAL="${TMP_DIR}/loom-longmemeval"
if [[ "${1:-}" == "info" ]]; then
    shift
    exec "${REAL}" info "$@"
fi
shift  # drop "run"
dataset="" types="" offset=0 limit=0 out="" det=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --dataset) dataset="$2"; shift 2 ;;
        --types) types="$2"; shift 2 ;;
        --offset) offset="$2"; shift 2 ;;
        --limit) limit="$2"; shift 2 ;;
        --output) out="$2"; shift 2 ;;
        --detailed) det="$2"; shift 2 ;;
        --occurred-at=*) shift ;;
        *) shift 2 ;;
    esac
done
echo "${types} ${offset} ${limit}" >> "${TMP_DIR}/invocations.log"

ids="$(jq -r --arg t "${types}" --argjson o "${offset}" --argjson l "${limit}" \
    '[.[] | select(.question_type == $t) | .question_id][$o:($o+$l)][]' "${dataset}")"

for id in ${ids}; do
    for cid in ${CRASH_IDS:-}; do [[ "${id}" == "${cid}" ]] && exit 1; done
done

outage=0
for id in ${ids}; do
    for tid in ${TRANSIENT_IDS:-}; do [[ "${id}" == "${tid}" ]] && outage=1; done
done
if [[ ${outage} -eq 1 ]]; then
    count_file="${TMP_DIR}/transient-${types}-${offset}.count"
    seen=$(( $(cat "${count_file}" 2>/dev/null || echo 0) + 1 ))
    echo "${seen}" > "${count_file}"
    if [[ ${seen} -le ${TRANSIENT_PASSES:-0} ]]; then
        : > "${out}"
        printf '[' > "${det}"
        first=1
        for id in ${ids}; do
            [[ ${first} -eq 1 ]] || printf ',' >> "${det}"
            first=0
            printf '{"question_id":"%s","error":"ingest session 0: rpc error: code = Unavailable desc = connection refused"}' "${id}" >> "${det}"
        done
        printf ']' >> "${det}"
        exit "${HARNESS_RC_TRANSIENT}"
    fi
fi

: > "${out}"
printf '[' > "${det}"
first=1
for id in ${ids}; do
    poisoned=0
    for p in ${POISON_IDS:-}; do [[ "${id}" == "${p}" ]] && poisoned=1; done
    [[ ${first} -eq 1 ]] || printf ',' >> "${det}"
    first=0
    if [[ ${poisoned} -eq 1 ]]; then
        printf '{"question_id":"%s","error":"deterministic content-filter rejection"}' "${id}" >> "${det}"
    else
        printf '{"question_id":"%s","hypothesis":"h","error":""}' "${id}" >> "${det}"
        printf '{"question_id":"%s","hypothesis":"h"}\n' "${id}" >> "${out}"
    fi
done
printf ']' >> "${det}"
exit 0
STUB
chmod +x "${TMP_DIR}/harness-stub.sh"

# run_loop <results-dir> — one pass of the slice loop (one pod lifetime).
run_loop() {
    TMP_DIR="${TMP_DIR}" POISON_IDS="${POISON_IDS:-}" \
    TRANSIENT_IDS="${TRANSIENT_IDS:-}" TRANSIENT_PASSES="${TRANSIENT_PASSES:-0}" \
    CRASH_IDS="${CRASH_IDS:-}" \
    HARNESS_BIN="${TMP_DIR}/harness-stub.sh" \
    SERVER="stub:1" DATASET_FILE="${TMP_DIR}/dataset.json" MODE="ingest" \
    CONCURRENCY="1" CHUNK="2" OCCURRED_AT="false" RESULTS_DIR="$1" \
    RUN_ID="${LME_RUN_ID}" RUN_MANIFEST="${LME_RUN_MANIFEST}" \
    MAX_CHUNK_ATTEMPTS="${LME_MAX_CHUNK_ATTEMPTS}" \
        bash "${TMP_DIR}/run-slices.sh" > "${TMP_DIR}/loop.log" 2>&1
}

# ── 1. Happy path: counts derived from the dataset, every entry covered ─────
echo "=== 1. derived counts cover every entry exactly ==="
R1="${TMP_DIR}/r1"; mkdir -p "${R1}"
: > "${TMP_DIR}/invocations.log"
POISON_IDS="" run_loop "${R1}"
rc=$?
run_dir="${R1}/runs/${LME_RUN_ID}"
[[ ${rc} -eq 0 ]] || fail "happy-path loop exited ${rc} (log: $(tail -3 "${TMP_DIR}/loop.log"))"
[[ ${rc} -eq 0 ]] && ok "loop exited 0"

want_chunks=5   # alpha: 0,2,4  beta: 0,2
got_done="$(find "${run_dir}" -name '*.done' | wc -l | tr -d ' ')"
[[ "${got_done}" == "${want_chunks}" ]] \
    && ok "${want_chunks} chunks completed" \
    || fail "expected ${want_chunks} .done markers, got ${got_done}"

got_entries="$(cat "${run_dir}"/s500-*.jsonl | wc -l | tr -d ' ')"
[[ "${got_entries}" == "8" ]] \
    && ok "all 8 dataset entries present in the results glob" \
    || fail "expected 8 result rows, got ${got_entries}"

# The ragged final chunk must be sized to what remains, never overrun the type.
grep -q '^alpha-type 4 1$' "${TMP_DIR}/invocations.log" \
    && ok "final alpha chunk requested limit 1 (not the full CHUNK)" \
    || fail "final ragged chunk was not sized from the derived count"
grep -q "^alpha-type 6" "${TMP_DIR}/invocations.log" \
    && fail "loop ran past the last alpha entry" \
    || ok "loop never ran past the last entry"

# ── 2. Resume skips completed chunks (no re-billing) ────────────────────────
echo "=== 2. resume skips completed chunks ==="
: > "${TMP_DIR}/invocations.log"
POISON_IDS="" run_loop "${R1}"
rc=$?
[[ ${rc} -eq 0 ]] || fail "resume pass exited ${rc}"
[[ ! -s "${TMP_DIR}/invocations.log" ]] \
    && ok "second pass made zero harness calls" \
    || fail "second pass re-ran chunks: $(cat "${TMP_DIR}/invocations.log")"

# ── 3-4. A poison entry quarantines its chunk, not the whole run ────────────
echo "=== 3. deterministic failure quarantines its chunk ==="
R2="${TMP_DIR}/r2"; mkdir -p "${R2}"
run_dir2="${R2}/runs/${LME_RUN_ID}"
: > "${TMP_DIR}/invocations.log"
export POISON_IDS="alpha-type-2"   # lands in chunk alpha offset 2
for pass in 1 2 3 4; do
    run_loop "${R2}"
    rc=$?
done
[[ ${rc} -ne 0 ]] \
    && ok "run still exits nonzero while a chunk is missing" \
    || fail "run exited 0 despite a quarantined chunk"

[[ -f "${run_dir2}/s500-alpha-type-o002.failed" ]] \
    && ok "poison chunk quarantined" \
    || fail "poison chunk has no .failed marker"
grep -q 'alpha-type-2' "${run_dir2}/s500-alpha-type-o002.failed" \
    && ok "quarantine marker names the failing entry" \
    || fail "quarantine marker does not name the failing entry"

# Bounded re-billing: 2 attempts, not one per pass.
attempts="$(grep -c '^alpha-type 2 ' "${TMP_DIR}/invocations.log")"
[[ "${attempts}" == "${LME_MAX_CHUNK_ATTEMPTS}" ]] \
    && ok "poison chunk attempted exactly ${LME_MAX_CHUNK_ATTEMPTS} times across 4 passes" \
    || fail "poison chunk attempted ${attempts} times, expected ${LME_MAX_CHUNK_ATTEMPTS}"

healthy_done="$(find "${run_dir2}" -name '*.done' | wc -l | tr -d ' ')"
[[ "${healthy_done}" == "4" ]] \
    && ok "the other 4 chunks completed despite the poison chunk" \
    || fail "expected 4 healthy chunks to complete, got ${healthy_done}"

[[ -f "${run_dir2}/RUN-INCOMPLETE.txt" ]] \
    && ok "RUN-INCOMPLETE.txt records that the run is missing entries" \
    || fail "no RUN-INCOMPLETE.txt written"

echo "=== 4. rejected output is preserved, outside the scorer's glob ==="
[[ -s "${run_dir2}/rejected/s500-alpha-type-o002-detailed.json" ]] \
    && ok "rejected attempt preserved under rejected/" \
    || fail "rejected attempt output was discarded"
if compgen -G "${run_dir2}/s500-*.jsonl" >/dev/null; then
    if cat "${run_dir2}"/s500-*.jsonl | grep -q 'alpha-type-3'; then
        fail "an unpromoted entry leaked into the scorer's glob"
    else
        ok "no unpromoted entry in the s500-*.jsonl glob"
    fi
fi

# ── 5. A failed PVC write stops the run instead of reporting success ────────
echo "=== 5. a persistence failure fails closed ==="
if [[ "$(id -u)" == "0" ]]; then
    echo "  note: running as root; chmod cannot make a directory unwritable — skipping"
else
    R3="${TMP_DIR}/r3"; mkdir -p "${R3}/runs/${LME_RUN_ID}"
    chmod a-w "${R3}/runs/${LME_RUN_ID}"
    POISON_IDS="" run_loop "${R3}"
    rc=$?
    chmod u+w "${R3}/runs/${LME_RUN_ID}"

    [[ ${rc} -ne 0 ]] \
        && ok "a run whose results volume rejects writes exits nonzero" \
        || fail "the loop reported success while persistence was failing (rc=${rc})"
    grep -q "FATAL: results PVC write failed" "${TMP_DIR}/loop.log" \
        && ok "the failure names the volume, not the chunk" \
        || fail "no PVC failure diagnostic in the log"
fi

# ── 5b. A failure at the promotion step itself fails closed ─────────────────
# Case 5 fails on the run's first write, before any chunk is promoted. These
# inject the failure into each promotion step instead: a validated chunk whose
# output or marker never lands must stop the pass, and must never be left
# marked complete — otherwise later passes skip it and the scored run silently
# loses those entries.
echo "=== 5b. a failed promotion or marker write fails closed ==="
SHIM_DIR="${TMP_DIR}/mv-shim"; mkdir -p "${SHIM_DIR}"
REAL_MV="$(command -v mv)"
cat > "${SHIM_DIR}/mv" <<SHIM
#!/usr/bin/env bash
# Fails a rename whose destination matches MV_FAIL_GLOB; otherwise real mv.
dest="\${@: -1}"
case "\${dest}" in \${MV_FAIL_GLOB:-__none__}) echo "mv-shim: injected failure for \${dest}" >&2; exit 1 ;; esac
exec "${REAL_MV}" "\$@"
SHIM
chmod +x "${SHIM_DIR}/mv"
for step in '*-detailed.json' '*.jsonl' '*.done'; do
    R5="$(mktemp -d "${TMP_DIR}/r5.XXXX")"
    (
        export PATH="${SHIM_DIR}:${PATH}" MV_FAIL_GLOB="${step}"
        POISON_IDS="" run_loop "${R5}"
    )
    rc=$?
    [[ ${rc} -ne 0 ]] \
        && ok "promotion failure at ${step} exits nonzero" \
        || fail "promotion failure at ${step} reported success (rc=${rc})"
    grep -q "FATAL: results PVC write failed" "${TMP_DIR}/loop.log" \
        && ok "promotion failure at ${step} is reported as a PVC failure" \
        || fail "no PVC failure diagnostic for a promotion failure at ${step}"
    if grep -q '^chunk complete:' "${TMP_DIR}/loop.log"; then
        fail "a chunk was reported complete although its ${step} rename failed"
    fi
    leaked="$(find "${R5}" -name '*.done' | wc -l | tr -d ' ')"
    [[ "${leaked}" == "0" ]] \
        && ok "no .done marker after a failed ${step} rename" \
        || fail "${leaked} .done marker(s) written although the ${step} rename failed"
done

# ── 6. A recovered run clears the incomplete sentinel ───────────────────────
echo "=== 6. RUN-INCOMPLETE.txt is cleared once every chunk completes ==="
run_dir1="${R1}/runs/${LME_RUN_ID}"
# R1 completed in step 1; plant a stale sentinel as a recovered quarantine
# would have left behind, then run a no-op pass.
echo "stale" > "${run_dir1}/RUN-INCOMPLETE.txt"
POISON_IDS="" run_loop "${R1}"
rc=$?
[[ ${rc} -eq 0 ]] \
    && ok "a fully-completed run exits zero" \
    || fail "completed run exited ${rc}"
[[ ! -f "${run_dir1}/RUN-INCOMPLETE.txt" ]] \
    && ok "stale sentinel removed — a complete run is not reported as missing entries" \
    || fail "RUN-INCOMPLETE.txt survived a fully-completed run"

# ── 7. A transient outage never consumes the deterministic budget ───────────
echo "=== 7. transient mid-chunk failures leave the budget untouched ==="
R4="${TMP_DIR}/r4"; mkdir -p "${R4}"
run_dir4="${R4}/runs/${LME_RUN_ID}"
: > "${TMP_DIR}/invocations.log"
# Every entry of chunk beta offset 0 fails with Unavailable for
# MAX_CHUNK_ATTEMPTS passes — enough to quarantine it if the outage were
# charged — and then the outage ends.
outage_passes="${LME_MAX_CHUNK_ATTEMPTS}"
for pass in $(seq 1 "${outage_passes}"); do
    POISON_IDS="" TRANSIENT_IDS="beta-type-0" TRANSIENT_PASSES="${outage_passes}" run_loop "${R4}"
    rc=$?
    [[ ${rc} -ne 0 ]] \
        || fail "outage pass ${pass} exited 0 while a chunk was still failing"
    [[ ! -f "${run_dir4}/s500-beta-type-o000.failed" ]] \
        || fail "outage pass ${pass} quarantined a chunk whose failures were all transient"
    [[ ! -f "${run_dir4}/s500-beta-type-o000.attempts" ]] \
        || fail "outage pass ${pass} charged the deterministic budget ($(cat "${run_dir4}/s500-beta-type-o000.attempts"))"
    [[ -f "${run_dir4}/RUN-INCOMPLETE.txt" ]] \
        || fail "outage pass ${pass} wrote no RUN-INCOMPLETE.txt while a chunk was failing"
done
grep -q 'transient entry failures, budget untouched' "${TMP_DIR}/loop.log" \
    && ok "outage reported as transient, budget untouched" \
    || fail "no transient-failure diagnostic in the log"
POISON_IDS="" TRANSIENT_IDS="beta-type-0" TRANSIENT_PASSES="${outage_passes}" run_loop "${R4}"
rc=$?
[[ ${rc} -eq 0 ]] \
    && ok "the pass after the outage ends completes the run" \
    || fail "post-outage pass exited ${rc} (log: $(tail -3 "${TMP_DIR}/loop.log"))"
[[ ! -f "${run_dir4}/s500-beta-type-o000.failed" ]] \
    && ok "no .failed marker after ${outage_passes} transient passes (MAX_CHUNK_ATTEMPTS=${LME_MAX_CHUNK_ATTEMPTS})" \
    || fail "transient chunk was quarantined"
[[ -f "${run_dir4}/s500-beta-type-o000.done" ]] \
    && ok "the outage chunk completed once the outage ended" \
    || fail "the outage chunk never completed"
done4="$(find "${run_dir4}" -name '*.done' | wc -l | tr -d ' ')"
[[ "${done4}" == "5" ]] \
    && ok "all 5 chunks completed" \
    || fail "expected 5 completed chunks after the outage, got ${done4}"
[[ ! -f "${run_dir4}/RUN-INCOMPLETE.txt" ]] \
    && ok "no RUN-INCOMPLETE.txt for a run that recovered from an outage" \
    || fail "RUN-INCOMPLETE.txt written for a recovered run"
outage_attempts="$(grep -c '^beta-type 0 ' "${TMP_DIR}/invocations.log")"
[[ "${outage_attempts}" == "$((outage_passes + 1))" ]] \
    && ok "outage chunk retried on every pass (${outage_attempts} invocations)" \
    || fail "outage chunk invoked ${outage_attempts} times, expected $((outage_passes + 1))"

# ── 7b. A chunk that never recovers keeps the incomplete sentinel ───────────
# If the transient outage outlasts the Job's backoffLimit, the last pass is
# the run's final state: the sentinel must say which chunk is missing, even
# though no chunk was quarantined, and a pre-existing sentinel must not be
# taken for a stale one.
echo "=== 7b. RUN-INCOMPLETE.txt names chunks that are not done ==="
R6="${TMP_DIR}/r6"; mkdir -p "${R6}/runs/${LME_RUN_ID}"
run_dir6="${R6}/runs/${LME_RUN_ID}"
rm -f "${TMP_DIR}/transient-alpha-type-4.count"
echo "stale" > "${run_dir6}/RUN-INCOMPLETE.txt"
# alpha offset 4 fails transiently on every pass; the harness cannot run at
# all for beta offset 2; alpha offset 0 holds a poison entry, so after pass 1
# it is under its deterministic budget and after pass 2 it is quarantined.
for pass in 1 2; do
    POISON_IDS="alpha-type-0" TRANSIENT_IDS="alpha-type-4" TRANSIENT_PASSES=1000 \
        CRASH_IDS="beta-type-2" run_loop "${R6}"
    rc=$?
    sentinel="${run_dir6}/RUN-INCOMPLETE.txt"
    [[ ${rc} -ne 0 ]] \
        && ok "pass ${pass} exits nonzero while chunks are not done" \
        || fail "pass ${pass} exited 0 with a chunk still failing"
    if [[ ! -f "${sentinel}" ]]; then
        fail "pass ${pass} removed RUN-INCOMPLETE.txt although chunks are not done"
        continue
    fi
    grep -qx 'stale' "${sentinel}" \
        && fail "pass ${pass} left the pre-existing sentinel unrewritten" \
        || ok "pass ${pass} rewrote the sentinel"
    sed -n '/^transient-failed chunks/,/^[^ ]/p' "${sentinel}" | grep -q '^  s500-alpha-type-o004$' \
        && ok "pass ${pass}: sentinel lists the transient chunk under its heading" \
        || fail "pass ${pass}: sentinel does not list s500-alpha-type-o004 as transient-failed ($(cat "${sentinel}"))"
    sed -n '/^transient-failed chunks/,/^[^ ]/p' "${sentinel}" | grep -q '^  s500-beta-type-o002$' \
        && ok "pass ${pass}: sentinel lists the chunk whose harness could not run as transient-failed" \
        || fail "pass ${pass}: sentinel does not list s500-beta-type-o002 as transient-failed ($(cat "${sentinel}"))"
    if [[ ${pass} -eq 1 ]]; then
        sed -n '/^deterministically failed chunks/,/^[^ ]/p' "${sentinel}" | grep -q '^  s500-alpha-type-o000$' \
            && ok "pass 1: a chunk under its deterministic budget is listed under its own heading" \
            || fail "pass 1: sentinel does not list s500-alpha-type-o000 as deterministically failed ($(cat "${sentinel}"))"
    fi
    grep -Eq '^  transient-failed chunks: +2$' "${TMP_DIR}/loop.log" \
        && ok "pass ${pass}: run summary prints the transient count" \
        || fail "pass ${pass}: run summary does not print the transient count"
done
sed -n '/^quarantined chunks/,/^[^ ]/p' "${run_dir6}/RUN-INCOMPLETE.txt" | grep -q '^  s500-alpha-type-o000$' \
    && ok "after the budget is spent the poison chunk is listed as quarantined" \
    || fail "sentinel does not list s500-alpha-type-o000 as quarantined ($(cat "${run_dir6}/RUN-INCOMPLETE.txt"))"
sed -n '/^transient-failed chunks/,/^[^ ]/p' "${run_dir6}/RUN-INCOMPLETE.txt" | grep -q 's500-alpha-type-o000' \
    && fail "the quarantined chunk is also listed as transient-failed" \
    || ok "quarantined and transient-failed chunks are listed separately"

# ── 8. Dataset swapped underneath a resume is refused ───────────────────────
echo "=== 8. dataset drift is refused on resume ==="
python3 - "${TMP_DIR}/dataset.json" <<'PY'
import json, sys
entries = json.load(open(sys.argv[1]))
entries.append({"question_id": "alpha-type-9", "question_type": "alpha-type",
                "question": "q", "answer": "a", "haystack_sessions": []})
json.dump(entries, open(sys.argv[1], "w"))
PY
POISON_IDS="" run_loop "${R1}"
rc=$?
[[ ${rc} -ne 0 ]] && grep -q 'no longer holds the' "${TMP_DIR}/loop.log" \
    && ok "resume refused after the dataset changed under it" \
    || fail "a revised dataset was accepted on resume (rc=${rc})"

# ── 8b. A same-shape revision is refused too ────────────────────────────────
# Case 8 changes the entry count, which summary statistics alone would catch.
# This revision keeps every count and type and changes only one question's
# text, so only the dataset content hash can refuse it.
echo "=== 8b. a same-count, same-type dataset edit is refused on resume ==="
python3 - "${TMP_DIR}/dataset.json" <<'PY'
import json, sys
entries = [e for e in json.load(open(sys.argv[1])) if e["question_id"] != "alpha-type-9"]
assert len(entries) == 8, len(entries)
entries[0]["question"] = "a different question"
json.dump(entries, open(sys.argv[1], "w"))
PY
POISON_IDS="" run_loop "${R1}"
rc=$?
[[ ${rc} -ne 0 ]] && grep -q 'no longer holds the' "${TMP_DIR}/loop.log" \
    && ok "resume refused after a same-shape dataset edit" \
    || fail "a same-count, same-type dataset edit was accepted on resume (rc=${rc})"

echo ""
if [[ "${failures}" -gt 0 ]]; then
    echo "slice-loop-test: ${failures} failure(s)"
    exit 1
fi
echo "slice-loop-test: OK"
