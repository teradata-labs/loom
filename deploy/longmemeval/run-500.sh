#!/usr/bin/env bash
# run-500.sh — Build, deploy, and launch the full LongMemEval run on AKS.
#
# Prerequisites:
#   - setup-cluster.sh has been run (or kubectl context points at the cluster)
#   - LME_BEDROCK_BEARER_TOKEN exported (an ABSK... Bedrock API key), OR the
#     lme-bedrock secret already exists in the namespace
#
# Usage:
#   LME_BEDROCK_BEARER_TOKEN=ABSK... bash deploy/longmemeval/run-500.sh
#   bash deploy/longmemeval/run-500.sh --skip-build   # reuse a pushed image tag

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=lme.env
source "${SCRIPT_DIR}/lme.env"
[[ -f "${SCRIPT_DIR}/lme.env.local" ]] && source "${SCRIPT_DIR}/lme.env.local"
# shellcheck source=render-common.sh
source "${SCRIPT_DIR}/render-common.sh"

SKIP_BUILD=0
[[ "${1:-}" == "--skip-build" ]] && SKIP_BUILD=1

case "${LME_DATASET}" in
  oracle) LME_DATASET_FILE="longmemeval_oracle.json" ;;
  small)  LME_DATASET_FILE="longmemeval_s_cleaned.json" ;;
  medium) LME_DATASET_FILE="longmemeval_m_cleaned.json" ;;
  *) echo "Unknown LME_DATASET: ${LME_DATASET}"; exit 1 ;;
esac

# Workloads are pinned to a tag that identifies the *build context* — never
# :latest — and the tag is part of the run's identity manifest below.
#
# az acr build uploads the working tree, not the commit, and ACR tags are
# mutable: a dirty tree at commit abc123 would otherwise be pushed as :abc123,
# a name a clean build of abc123 may already own. The manifest would then
# match and the runner would happily resume chunks produced by a different
# binary — exactly the cross-run contamination the run identity exists to
# prevent. A dirty tree therefore gets its own tag and its own run id.
GIT_COMMIT="$(git -C "${REPO_ROOT}" rev-parse --short HEAD)"
GIT_BUILD_ID="$(lme_build_id "${REPO_ROOT}")"
if [[ "${GIT_BUILD_ID}" != "${GIT_COMMIT}" ]]; then
    echo "WARNING: the working tree is dirty; az acr build uploads it as-is."
    echo "         Tagging this build ${GIT_BUILD_ID} so it cannot be confused"
    echo "         with a clean build of ${GIT_COMMIT} (it gets its own run id)."
fi
LME_IMAGE_TAG="${LME_IMAGE_TAG:-${GIT_BUILD_ID}}"
LME_IMAGE_REPO="${LME_IMAGE#*/}"
LME_MAX_CHUNK_ATTEMPTS="${LME_MAX_CHUNK_ATTEMPTS:-3}"

if [[ "${SKIP_BUILD}" -eq 0 ]]; then
    echo "=== Building image in ACR (${LME_IMAGE}:${LME_IMAGE_TAG}) ==="
    az acr build \
        --registry "${LME_ACR_NAME}" \
        --image "${LME_IMAGE_REPO}:${LME_IMAGE_TAG}" \
        --file "${SCRIPT_DIR}/Dockerfile" \
        --build-arg "GIT_COMMIT=${GIT_BUILD_ID}" \
        "${REPO_ROOT}"
else
    echo "=== Skipping build; verifying ${LME_IMAGE}:${LME_IMAGE_TAG} exists in ACR ==="
    if ! az acr repository show --name "${LME_ACR_NAME}" --image "${LME_IMAGE_REPO}:${LME_IMAGE_TAG}" -o none; then
        echo "ERROR: ${LME_IMAGE}:${LME_IMAGE_TAG} not found in ACR."
        echo "Run without --skip-build, or set LME_IMAGE_TAG to a tag that was pushed."
        exit 1
    fi
fi

# ACR tags are mutable, and Kubernetes defaults a non-:latest tag to
# imagePullPolicy: IfNotPresent — so a rebuilt or retagged image can serve a
# run whose manifest records the same tag string. Resolve the tag to its
# immutable digest and make THAT part of the run identity, so a moved tag
# yields a new run id instead of silently resuming onto chunks produced by
# different bytes. Empty (older az, or no permission) degrades to tag-only
# identity rather than blocking the run.
LME_IMAGE_DIGEST="$(az acr repository show \
    --name "${LME_ACR_NAME}" \
    --image "${LME_IMAGE_REPO}:${LME_IMAGE_TAG}" \
    --query digest -o tsv 2>/dev/null || true)"
if [[ -z "${LME_IMAGE_DIGEST}" ]]; then
    echo "WARNING: could not resolve the image digest; run identity falls back to the"
    echo "         tag alone, which ACR allows to move. Resume cannot detect a rebuild."
    LME_IMAGE_DIGEST="unresolved"
fi

# Run identity: a manifest of every input that determines what the benchmark
# measures. Chunk outputs are namespaced by its hash on the results PVC, so a
# configuration change can never silently reuse chunks from a different run;
# the runner also verifies the stored manifest before resuming.
LME_RUN_MANIFEST="dataset=${LME_DATASET_FILE} mode=${LME_MODE} occurred_at=${LME_OCCURRED_AT} model=${LME_MODEL} image=${LME_IMAGE}:${LME_IMAGE_TAG} digest=${LME_IMAGE_DIGEST} chunk=${LME_CHUNK}"
LME_RUN_ID="${LME_RUN_ID:-$(lme_sha256_12 "${LME_RUN_MANIFEST}")}"
LME_ALLOW_MANIFEST_DRIFT="${LME_ALLOW_MANIFEST_DRIFT:-0}"

export LME_NAMESPACE LME_IMAGE LME_IMAGE_TAG LME_GRPC_PORT LME_MODEL \
    LME_DATASET LME_DATASET_FILE LME_MODE LME_CONCURRENCY LME_CHUNK \
    LME_OCCURRED_AT LME_RUN_ID LME_RUN_MANIFEST LME_ALLOW_MANIFEST_DRIFT \
    LME_MAX_CHUNK_ATTEMPTS

echo "=== Run identity ==="
echo "  run id:   ${LME_RUN_ID}"
echo "  manifest: ${LME_RUN_MANIFEST}"
echo "  digest:   ${LME_IMAGE_DIGEST}"
echo "  outputs:  PVC lme-results:/results/runs/${LME_RUN_ID}/"

# Hash the rendered server config so a config-only change (model, port,
# anything in looms.yaml) rolls the server pod. Computed BEFORE the config is
# applied, from exactly the bytes that will be applied.
LME_CONFIG_HASH="$(lme_render "${SCRIPT_DIR}/server-config.yaml" | lme_sha256_12_stdin)"
export LME_CONFIG_HASH

echo "=== Applying namespace, PVCs, config (namespace=${LME_NAMESPACE}, config ${LME_CONFIG_HASH}) ==="
for manifest in namespace pvcs server-config runner-script; do
    lme_render "${SCRIPT_DIR}/${manifest}.yaml" | kubectl apply -f -
done

echo "=== Ensuring Bedrock secret ==="
if ! kubectl get secret lme-bedrock -n "${LME_NAMESPACE}" &>/dev/null; then
    if [[ -z "${LME_BEDROCK_BEARER_TOKEN:-}" ]]; then
        echo "ERROR: secret lme-bedrock not found and LME_BEDROCK_BEARER_TOKEN not set."
        echo "Export a Bedrock API key (ABSK...) and re-run."
        exit 1
    fi
    kubectl create secret generic lme-bedrock \
        -n "${LME_NAMESPACE}" \
        --from-literal=bearer-token="${LME_BEDROCK_BEARER_TOKEN}"
fi

echo "=== Deploying server (${LME_IMAGE}:${LME_IMAGE_TAG}, port ${LME_GRPC_PORT}) ==="
lme_render "${SCRIPT_DIR}/server-deployment.yaml" | kubectl apply -f -
kubectl rollout status deployment/lme-server -n "${LME_NAMESPACE}" --timeout=300s

echo "=== Launching runner job (mode=${LME_MODE}, concurrency=${LME_CONCURRENCY}, chunk=${LME_CHUNK}, run=${LME_RUN_ID}) ==="
# Delete any previous job object (results on the PVC are untouched; chunks
# with a .done marker are skipped on the next run — this is the resume path).
kubectl delete job lme-runner -n "${LME_NAMESPACE}" --ignore-not-found
lme_render "${SCRIPT_DIR}/runner-job.yaml" | kubectl apply -f -

echo ""
echo "=== Running. Monitor with: ==="
echo "  kubectl logs -f job/lme-runner -n ${LME_NAMESPACE}"
echo "  bash ${SCRIPT_DIR}/pull-results.sh   # snapshot results mid-run"
