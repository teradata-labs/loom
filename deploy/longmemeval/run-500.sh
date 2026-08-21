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
#   bash deploy/longmemeval/run-500.sh --skip-build   # reuse the pushed image

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=lme.env
source "${SCRIPT_DIR}/lme.env"
[[ -f "${SCRIPT_DIR}/lme.env.local" ]] && source "${SCRIPT_DIR}/lme.env.local"

SKIP_BUILD=0
[[ "${1:-}" == "--skip-build" ]] && SKIP_BUILD=1

case "${LME_DATASET}" in
  oracle) LME_DATASET_FILE="longmemeval_oracle.json" ;;
  small)  LME_DATASET_FILE="longmemeval_s_cleaned.json" ;;
  medium) LME_DATASET_FILE="longmemeval_m_cleaned.json" ;;
  *) echo "Unknown LME_DATASET: ${LME_DATASET}"; exit 1 ;;
esac
export LME_IMAGE LME_GRPC_PORT LME_DATASET LME_DATASET_FILE LME_MODE LME_CONCURRENCY LME_CHUNK LME_OCCURRED_AT

if [[ "${SKIP_BUILD}" -eq 0 ]]; then
    GIT_COMMIT="$(git -C "${REPO_ROOT}" rev-parse --short HEAD)"
    echo "=== Building image in ACR (${LME_IMAGE}:latest @ ${GIT_COMMIT}) ==="
    az acr build \
        --registry "${LME_ACR_NAME}" \
        --image "loom-longmemeval:latest" \
        --image "loom-longmemeval:${GIT_COMMIT}" \
        --file "${SCRIPT_DIR}/Dockerfile" \
        --build-arg "GIT_COMMIT=${GIT_COMMIT}" \
        "${REPO_ROOT}"
fi

echo "=== Applying namespace, PVCs, config ==="
kubectl apply -f "${SCRIPT_DIR}/namespace.yaml"
kubectl apply -f "${SCRIPT_DIR}/pvcs.yaml"
kubectl apply -f "${SCRIPT_DIR}/server-config.yaml"
kubectl apply -f "${SCRIPT_DIR}/runner-script.yaml"

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

echo "=== Deploying server ==="
kubectl apply -f "${SCRIPT_DIR}/server-deployment.yaml"
kubectl rollout status deployment/lme-server -n "${LME_NAMESPACE}" --timeout=300s

echo "=== Launching runner job (mode=${LME_MODE}, concurrency=${LME_CONCURRENCY}, chunk=${LME_CHUNK}) ==="
# Delete any previous job (results on the PVC are untouched; completed chunks
# are skipped on the next run — this is the resume path).
kubectl delete job lme-runner -n "${LME_NAMESPACE}" --ignore-not-found
envsubst < "${SCRIPT_DIR}/runner-job.yaml" | kubectl apply -f -

echo ""
echo "=== Running. Monitor with: ==="
echo "  kubectl logs -f job/lme-runner -n ${LME_NAMESPACE}"
echo "  bash ${SCRIPT_DIR}/pull-results.sh   # snapshot results mid-run"
