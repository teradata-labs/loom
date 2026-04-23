#!/usr/bin/env bash
# poc-run.sh — Build, push, deploy, and run the benchmark PoC on AKS.
#
# Prerequisites:
#   - AKS cluster created via setup-cluster.sh
#   - Docker running locally
#   - az CLI authenticated
#   - kubectl configured with loom-bench-aks context

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# shellcheck source=bench.env
source "${SCRIPT_DIR}/bench.env"
[[ -f "${SCRIPT_DIR}/bench.env.local" ]] && source "${SCRIPT_DIR}/bench.env.local"

GIT_COMMIT="$(git -C "${REPO_ROOT}" rev-parse --short HEAD)"
IMAGE_TAG="${BENCH_IMAGE}:${GIT_COMMIT}"
IMAGE_LATEST="${BENCH_IMAGE}:latest"

echo "=== Step 1/7: Authenticate with ACR ==="
az acr login --name "${BENCH_ACR_NAME}"

echo "=== Step 2/7: Build Docker image ==="
docker build \
    -t "${IMAGE_TAG}" \
    -t "${IMAGE_LATEST}" \
    --build-arg "GIT_COMMIT=${GIT_COMMIT}" \
    -f "${REPO_ROOT}/deploy/Dockerfile" \
    "${REPO_ROOT}"

echo "=== Step 3/7: Push to ACR ==="
docker push "${IMAGE_TAG}"
docker push "${IMAGE_LATEST}"

echo "=== Step 4/7: Deploy infrastructure ==="
kubectl config use-context "${BENCH_CLUSTER_NAME}"
kubectl apply -f "${SCRIPT_DIR}/namespace.yaml"
kubectl apply -f "${SCRIPT_DIR}/rbac.yaml"
kubectl apply -f "${SCRIPT_DIR}/results-pvc.yaml"
kubectl apply -f "${SCRIPT_DIR}/server-deployment.yaml"
kubectl apply -f "${SCRIPT_DIR}/server-service.yaml"

echo "Waiting for server pod to be ready..."
kubectl rollout status deployment/loom-bench-server -n "${BENCH_NAMESPACE}" --timeout=120s

echo "=== Step 5/7: Run harness job ==="
kubectl delete job loom-bench-harness -n "${BENCH_NAMESPACE}" --ignore-not-found
kubectl apply -f "${SCRIPT_DIR}/harness-job.yaml"

echo "Waiting for harness job to complete..."
kubectl wait --for=condition=complete job/loom-bench-harness \
    -n "${BENCH_NAMESPACE}" --timeout=600s || {
    echo "Job did not complete in time. Checking status:"
    kubectl describe job/loom-bench-harness -n "${BENCH_NAMESPACE}"
    kubectl logs job/loom-bench-harness -n "${BENCH_NAMESPACE}" --tail=50
    exit 1
}

echo "=== Step 6/7: Collect results ==="
echo ""
echo "=== BENCHMARK LOG ==="
kubectl logs job/loom-bench-harness -n "${BENCH_NAMESPACE}"

echo ""
echo "=== Step 7/7: Copy result files ==="
RESULTS_DIR="${REPO_ROOT}/results"
mkdir -p "${RESULTS_DIR}"

# Get the harness pod name for kubectl cp
HARNESS_POD=$(kubectl get pods -n "${BENCH_NAMESPACE}" -l job-name=loom-bench-harness \
    -o jsonpath='{.items[0].metadata.name}')

kubectl cp "${BENCH_NAMESPACE}/${HARNESS_POD}:/results" "${RESULTS_DIR}" 2>/dev/null || \
    echo "Note: kubectl cp failed (distroless image). Results are in the PVC."

echo ""
echo "=== PoC complete ==="
echo "Results are stored in the PVC bench-results in namespace ${BENCH_NAMESPACE}."
echo "To copy manually: kubectl cp ${BENCH_NAMESPACE}/<pod>:/results ./results"
