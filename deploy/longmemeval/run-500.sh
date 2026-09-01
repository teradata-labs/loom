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

# Workloads are pinned to an immutable commit tag — never :latest — and the
# tag is part of the run's identity manifest below.
GIT_COMMIT="$(git -C "${REPO_ROOT}" rev-parse --short HEAD)"
LME_IMAGE_TAG="${LME_IMAGE_TAG:-${GIT_COMMIT}}"
LME_IMAGE_REPO="${LME_IMAGE#*/}"

if [[ "${SKIP_BUILD}" -eq 0 ]]; then
    echo "=== Building image in ACR (${LME_IMAGE}:${LME_IMAGE_TAG}) ==="
    az acr build \
        --registry "${LME_ACR_NAME}" \
        --image "${LME_IMAGE_REPO}:${LME_IMAGE_TAG}" \
        --file "${SCRIPT_DIR}/Dockerfile" \
        --build-arg "GIT_COMMIT=${GIT_COMMIT}" \
        "${REPO_ROOT}"
else
    echo "=== Skipping build; verifying ${LME_IMAGE}:${LME_IMAGE_TAG} exists in ACR ==="
    if ! az acr repository show --name "${LME_ACR_NAME}" --image "${LME_IMAGE_REPO}:${LME_IMAGE_TAG}" -o none; then
        echo "ERROR: ${LME_IMAGE}:${LME_IMAGE_TAG} not found in ACR."
        echo "Run without --skip-build, or set LME_IMAGE_TAG to a tag that was pushed."
        exit 1
    fi
fi

# Run identity: a manifest of every input that determines what the benchmark
# measures. Chunk outputs are namespaced by its hash on the results PVC, so a
# configuration change can never silently reuse chunks from a different run;
# the runner also verifies the stored manifest before resuming.
LME_RUN_MANIFEST="dataset=${LME_DATASET_FILE} mode=${LME_MODE} occurred_at=${LME_OCCURRED_AT} model=${LME_MODEL} image=${LME_IMAGE}:${LME_IMAGE_TAG} chunk=${LME_CHUNK}"
LME_RUN_ID="${LME_RUN_ID:-$(lme_sha256_12 "${LME_RUN_MANIFEST}")}"
LME_ALLOW_MANIFEST_DRIFT="${LME_ALLOW_MANIFEST_DRIFT:-0}"

export LME_NAMESPACE LME_IMAGE LME_IMAGE_TAG LME_GRPC_PORT LME_MODEL \
    LME_DATASET LME_DATASET_FILE LME_MODE LME_CONCURRENCY LME_CHUNK \
    LME_OCCURRED_AT LME_RUN_ID LME_RUN_MANIFEST LME_ALLOW_MANIFEST_DRIFT

echo "=== Run identity ==="
echo "  run id:   ${LME_RUN_ID}"
echo "  manifest: ${LME_RUN_MANIFEST}"
echo "  outputs:  PVC lme-results:/results/runs/${LME_RUN_ID}/"

echo "=== Applying namespace, PVCs, config (namespace=${LME_NAMESPACE}) ==="
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
