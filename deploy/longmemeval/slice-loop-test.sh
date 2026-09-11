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

# The stub answers `info` from the real binary and synthesizes `run` output.
# POISON_IDS (space separated) are recorded as per-entry errors, exactly as
# the harness does: omitted from the .jsonl, present with an "error" in the
# detailed JSON. Every invocation is logged so re-billing is observable.
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

# ── 7. Dataset swapped underneath a resume is refused ───────────────────────
echo "=== 7. dataset drift is refused on resume ==="
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

echo ""
if [[ "${failures}" -gt 0 ]]; then
    echo "slice-loop-test: ${failures} failure(s)"
    exit 1
fi
echo "slice-loop-test: OK"
