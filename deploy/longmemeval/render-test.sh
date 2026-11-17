#!/usr/bin/env bash
# render-test.sh — Renders every manifest template with NONDEFAULT
# namespace/image/port/model values and asserts the manifests agree, so a
# hardcoded value can never desync the workloads when lme.env is overridden.
# No cluster access needed. Usage:
#   bash deploy/longmemeval/render-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=render-common.sh
source "${SCRIPT_DIR}/render-common.sh"

# Deliberately nondefault values for every rendered field.
export LME_NAMESPACE="lme-render-test"
export LME_IMAGE="testreg.azurecr.io/lme-render-test"
export LME_IMAGE_TAG="cafef00d"
export LME_IMAGE_DIGEST="sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
export LME_GRPC_PORT="51234"
export LME_MODEL="test.model-v9"
export LME_DATASET="small"
export LME_DATASET_FILE="test_dataset.json"
export LME_MODE="ingest"
export LME_CONCURRENCY="3"
export LME_CHUNK="7"
export LME_OCCURRED_AT="false"
export LME_RUN_ID="runid0123abcd"
export LME_RUN_MANIFEST="dataset=test_dataset.json mode=ingest occurred_at=false model=test.model-v9 image=testreg.azurecr.io/lme-render-test:cafef00d chunk=7"
export LME_ALLOW_MANIFEST_DRIFT="0"
export LME_MAX_CHUNK_ATTEMPTS="4"
export LME_CONFIG_HASH="cfg123456789"

MANIFESTS=(namespace pvcs server-config runner-script server-deployment runner-job)

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

failures=0
fail() { echo "FAIL: $*"; failures=$((failures + 1)); }

for m in "${MANIFESTS[@]}"; do
    lme_render "${SCRIPT_DIR}/${m}.yaml" > "${TMP_DIR}/${m}.yaml" \
        || fail "${m}.yaml did not render"
done

# 1. Every rendered manifest parses as YAML.
if command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' 2>/dev/null; then
    for m in "${MANIFESTS[@]}"; do
        python3 -c 'import sys, yaml; list(yaml.safe_load_all(open(sys.argv[1])))' "${TMP_DIR}/${m}.yaml" \
            || fail "${m}.yaml does not parse as YAML after rendering"
    done
elif [[ "${LME_RIG_STRICT:-0}" == "1" ]]; then
    fail "python3+pyyaml is required to validate rendered manifests (LME_RIG_STRICT=1)"
else
    echo "NOTE: python3+pyyaml not available; skipping YAML parse assertions"
fi

# 2. The embedded runner script parses as bash.
if command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' 2>/dev/null; then
    python3 -c 'import sys, yaml; sys.stdout.write(yaml.safe_load(open(sys.argv[1]))["data"]["run-slices.sh"])' \
        "${TMP_DIR}/runner-script.yaml" > "${TMP_DIR}/run-slices.sh" \
        || fail "could not extract run-slices.sh from rendered runner-script.yaml"
    bash -n "${TMP_DIR}/run-slices.sh" || fail "rendered run-slices.sh has bash syntax errors"
fi

# 3. Namespace: the Namespace object and every metadata.namespace agree.
grep -q "name: ${LME_NAMESPACE}\$" "${TMP_DIR}/namespace.yaml" \
    || fail "namespace.yaml does not create ${LME_NAMESPACE}"
for m in pvcs server-config runner-script server-deployment runner-job; do
    grep -q "namespace: ${LME_NAMESPACE}\$" "${TMP_DIR}/${m}.yaml" \
        || fail "${m}.yaml missing namespace ${LME_NAMESPACE}"
    if grep -n '^[[:space:]]*namespace: ' "${TMP_DIR}/${m}.yaml" | grep -v "namespace: ${LME_NAMESPACE}\$"; then
        fail "${m}.yaml has a namespace other than ${LME_NAMESPACE}"
    fi
done

# 4. Images: every workload image is pinned by digest — never a tag, which ACR
# lets move under a running Job — and never :latest.
for m in server-deployment runner-job; do
    grep -q "image: ${LME_IMAGE}@${LME_IMAGE_DIGEST}\$" "${TMP_DIR}/${m}.yaml" \
        || fail "${m}.yaml missing digest-pinned image ${LME_IMAGE}@${LME_IMAGE_DIGEST}"
    if grep -n '^[[:space:]]*image: ' "${TMP_DIR}/${m}.yaml" | grep -v "image: ${LME_IMAGE}@${LME_IMAGE_DIGEST}\$"; then
        fail "${m}.yaml has an image other than ${LME_IMAGE}@${LME_IMAGE_DIGEST}"
    fi
done
if grep -rn ':latest' "${TMP_DIR}"/*.yaml; then
    fail "a rendered manifest still references :latest"
fi

# 4b. The registry is derived after lme.env.local: overriding LME_ACR_NAME
# alone must move the pods' image too, not only the build.
derived="$(
    unset LME_IMAGE LME_ACR_LOGIN_SERVER
    # shellcheck source=lme.env
    source "${SCRIPT_DIR}/lme.env"
    LME_ACR_NAME="otherreg"   # what an lme.env.local override does
    lme_derive_image
    printf '%s' "${LME_IMAGE}"
)"
[[ "${derived}" == "otherreg.azurecr.io/loom-longmemeval" ]] \
    || fail "LME_IMAGE ignores an LME_ACR_NAME override (got ${derived})"

# 5. Port: server config, containerPort, Service port, and the runner's SERVER
# address must all agree; the old default must not survive anywhere.
grep -q "port: ${LME_GRPC_PORT}\$" "${TMP_DIR}/server-config.yaml" \
    || fail "server-config.yaml port != ${LME_GRPC_PORT}"
grep -q "containerPort: ${LME_GRPC_PORT}\$" "${TMP_DIR}/server-deployment.yaml" \
    || fail "server-deployment.yaml containerPort != ${LME_GRPC_PORT}"
grep -q "port: ${LME_GRPC_PORT}\$" "${TMP_DIR}/server-deployment.yaml" \
    || fail "server-deployment.yaml Service port != ${LME_GRPC_PORT}"
grep -q "value: \"lme-server:${LME_GRPC_PORT}\"" "${TMP_DIR}/runner-job.yaml" \
    || fail "runner-job.yaml SERVER != lme-server:${LME_GRPC_PORT}"
if grep -rn '60051' "${TMP_DIR}"/*.yaml; then
    fail "a rendered manifest still hardcodes port 60051"
fi

# 6. Model, dataset, and run identity land where they must.
grep -q "bedrock_model_id: ${LME_MODEL}\$" "${TMP_DIR}/server-config.yaml" \
    || fail "server-config.yaml model != ${LME_MODEL}"
grep -q "value: \"/data/longmemeval/${LME_DATASET_FILE}\"" "${TMP_DIR}/runner-job.yaml" \
    || fail "runner-job.yaml DATASET_FILE not rendered"
grep -q "value: \"${LME_RUN_ID}\"" "${TMP_DIR}/runner-job.yaml" \
    || fail "runner-job.yaml RUN_ID not rendered"
grep -q "value: \"${LME_RUN_MANIFEST}\"" "${TMP_DIR}/runner-job.yaml" \
    || fail "runner-job.yaml RUN_MANIFEST not rendered"
grep -q "value: \"${LME_MAX_CHUNK_ATTEMPTS}\"" "${TMP_DIR}/runner-job.yaml" \
    || fail "runner-job.yaml MAX_CHUNK_ATTEMPTS not rendered"
# A config-only change must roll the server pod: looms reads its config once
# at startup, so without this a new model would never actually be served.
grep -q "loom.dev/config-checksum: \"${LME_CONFIG_HASH}\"" "${TMP_DIR}/server-deployment.yaml" \
    || fail "server-deployment.yaml pod template missing the config checksum annotation"
# A new run must not inherit the previous run's server state (emptyDir).
grep -q "loom.dev/run-id: \"${LME_RUN_ID}\"" "${TMP_DIR}/server-deployment.yaml" \
    || fail "server-deployment.yaml pod template missing the run id annotation"

# 6b. run-500.sh computes LME_CONFIG_HASH by rendering server-config.yaml
# BEFORE that variable exists. lme_render must therefore require only the
# variables a given template references — requiring the whole global allowlist
# broke the documented invocation, and every test here pre-seeding the value
# is precisely why CI stayed green through it.
(
    unset LME_CONFIG_HASH
    lme_render "${SCRIPT_DIR}/server-config.yaml" >/dev/null 2>&1
) || fail "server-config.yaml must render with LME_CONFIG_HASH unset (run-500.sh's hash step)"

# ...but a variable the template DOES reference must still be required.
(
    unset LME_MODEL
    lme_render "${SCRIPT_DIR}/server-config.yaml" >/dev/null 2>&1
) && fail "lme_render accepted server-config.yaml with LME_MODEL unset" || true

# 6c. The Bedrock bearer token must never be passed on kubectl's command
# line (visible to other local users via ps); run-500.sh feeds it on stdin.
if grep -n -- '--from-literal=bearer-token' "${SCRIPT_DIR}/run-500.sh"; then
    fail "run-500.sh passes the bearer token with --from-literal (use --from-file=bearer-token=/dev/stdin)"
fi
grep -q -- '--from-file=bearer-token=/dev/stdin' "${SCRIPT_DIR}/run-500.sh" \
    || fail "run-500.sh no longer creates lme-bedrock from stdin"

# 7. No unrendered placeholders (lme_render also guards; belt and suspenders).
if grep -rn '\${LME_' "${TMP_DIR}"/*.yaml; then
    fail "unrendered LME_* placeholder in rendered output"
fi

if [[ "${failures}" -gt 0 ]]; then
    echo ""
    echo "render-test: ${failures} failure(s)"
    exit 1
fi
echo "render-test: OK — all manifests agree on nondefault namespace/image/port/model/run-id"
