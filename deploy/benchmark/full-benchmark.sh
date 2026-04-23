#!/usr/bin/env bash
# full-benchmark.sh — Orchestrate the full publication-grade benchmark suite on AKS.
#
# This script:
#   1. Ensures node pools are scaled up
#   2. Builds and pushes Docker images
#   3. Deploys Loom server, runs all 12 scenarios
#   4. Swaps to LangGraph server (same node, same resources), runs comparison
#   5. Collects all results
#   6. Optionally scales down node pools (--cleanup flag)
#
# Resume: if the harness pod crashes, re-run this script with --skip-build.
# The harness will skip scenarios that already have results in /results/.
# To force a clean start, add --no-resume.
#
# Usage:
#   bash deploy/benchmark/full-benchmark.sh [--cleanup] [--scenario=NAME] [--runs=N]
#   bash deploy/benchmark/full-benchmark.sh --skip-build          # resume after crash
#   bash deploy/benchmark/full-benchmark.sh --no-resume            # start fresh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# shellcheck source=bench.env
source "${SCRIPT_DIR}/bench.env"
[[ -f "${SCRIPT_DIR}/bench.env.local" ]] && source "${SCRIPT_DIR}/bench.env.local"

# Parse flags
CLEANUP=false
SCENARIO="all"
RUNS=10
WARMUP_RUNS=2
SKIP_BUILD=false
RESUME=true

for arg in "$@"; do
    case $arg in
        --cleanup) CLEANUP=true ;;
        --scenario=*) SCENARIO="${arg#*=}" ;;
        --runs=*) RUNS="${arg#*=}" ;;
        --warmup-runs=*) WARMUP_RUNS="${arg#*=}" ;;
        --skip-build) SKIP_BUILD=true ;;
        --no-resume) RESUME=false ;;
        *) echo "Unknown flag: $arg"; exit 1 ;;
    esac
done

GIT_COMMIT="$(git -C "${REPO_ROOT}" rev-parse --short HEAD)"
TIMESTAMP="$(date -u +%Y%m%d-%H%M%S)"
RESULTS_DIR="${REPO_ROOT}/results/${TIMESTAMP}"

echo "=== Loom Publication Benchmark Suite ==="
echo "Commit:   ${GIT_COMMIT}"
echo "Scenario: ${SCENARIO}"
echo "Runs:     ${RUNS} measured + ${WARMUP_RUNS} warmup"
echo "Resume:   ${RESUME}"
echo "Results:  ${RESULTS_DIR}"
echo ""

# --- Step 1: Ensure infrastructure ---
echo "=== Step 1/6: Ensuring AKS infrastructure ==="
kubectl config use-context "${BENCH_CLUSTER_NAME}"

for pool in loomserver loomharness; do
    count=$(az aks nodepool show --cluster-name "${BENCH_CLUSTER_NAME}" \
        --resource-group "${BENCH_RESOURCE_GROUP}" --name "${pool}" \
        --query count -o tsv 2>/dev/null || echo "0")
    if [ "${count}" = "0" ]; then
        echo "Scaling up ${pool}..."
        az aks nodepool scale --cluster-name "${BENCH_CLUSTER_NAME}" \
            --resource-group "${BENCH_RESOURCE_GROUP}" --name "${pool}" \
            --node-count 1 -o none
    fi
done

echo "Waiting for nodes..."
while ! kubectl get nodes -l loom-role=server 2>/dev/null | grep -q Ready; do sleep 10; done
while ! kubectl get nodes -l loom-role=harness 2>/dev/null | grep -q Ready; do sleep 10; done
echo "Nodes ready."

# --- Step 2: Build and push images ---
if [ "${SKIP_BUILD}" = "false" ]; then
    echo "=== Step 2/6: Building and pushing Docker images ==="
    az acr login --name "${BENCH_ACR_NAME}"

    docker build \
        -t "${BENCH_IMAGE}:${GIT_COMMIT}" \
        -t "${BENCH_IMAGE}:latest" \
        --build-arg "GIT_COMMIT=${GIT_COMMIT}" \
        -f "${REPO_ROOT}/deploy/Dockerfile" \
        "${REPO_ROOT}"
    docker push "${BENCH_IMAGE}:${GIT_COMMIT}"
    docker push "${BENCH_IMAGE}:latest"

    docker build --platform linux/amd64 \
        -t "${BENCH_ACR_LOGIN_SERVER}/langgraph-bench:latest" \
        "${REPO_ROOT}/deploy/comparison/langgraph/"
    docker push "${BENCH_ACR_LOGIN_SERVER}/langgraph-bench:latest"
else
    echo "=== Step 2/6: Skipping build (--skip-build) ==="
fi

# --- Step 3: Deploy Loom server ---
echo "=== Step 3/6: Deploying Loom server ==="
kubectl apply -f "${SCRIPT_DIR}/namespace.yaml"
kubectl apply -f "${SCRIPT_DIR}/rbac.yaml"
kubectl apply -f "${SCRIPT_DIR}/results-pvc.yaml"
kubectl apply -f "${SCRIPT_DIR}/server-service.yaml"
kubectl apply -f "${SCRIPT_DIR}/langgraph-service.yaml"

# Clean up old jobs but leave PVC (results) intact
kubectl delete job --all -n "${BENCH_NAMESPACE}" --ignore-not-found 2>/dev/null || true

# --no-resume only affects which scenarios the harness skips.
# It NEVER deletes the PVC. Results are append-only.
# To manually wipe results: kubectl delete pvc bench-results -n loom-bench

# Deploy server (delete first to ensure fresh image pull)
kubectl delete deployment loom-bench-server -n "${BENCH_NAMESPACE}" --ignore-not-found 2>/dev/null || true
kubectl delete deployment langgraph-bench-server -n "${BENCH_NAMESPACE}" --ignore-not-found 2>/dev/null || true
sleep 5
kubectl apply -f "${SCRIPT_DIR}/server-deployment.yaml"
kubectl rollout status deployment/loom-bench-server -n "${BENCH_NAMESPACE}"

RESUME_FLAG="--resume=true"
if [ "${RESUME}" = "false" ]; then
    RESUME_FLAG="--resume=false"
fi

# --- Step 4: Run Loom scenarios ---
echo "=== Step 4/6: Running Loom scenarios (${SCENARIO}) ==="
cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: bench-loom-${TIMESTAMP}
  namespace: ${BENCH_NAMESPACE}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      serviceAccountName: bench-harness
      nodeSelector:
        loom-role: harness
      tolerations:
        - key: loom-role
          value: harness
          effect: NoSchedule
      containers:
        - name: harness
          image: ${BENCH_IMAGE}:latest
          imagePullPolicy: Always
          command: ["/loom-bench-harness"]
          args:
            - "--server-addr=loom-bench-server:${BENCH_GRPC_PORT}"
            - "--http-addr=loom-bench-server:8080"
            - "--scenario=${SCENARIO}"
            - "--runs=${RUNS}"
            - "--warmup-runs=${WARMUP_RUNS}"
            - "--output-dir=/results"
            - "${RESUME_FLAG}"
          volumeMounts:
            - {name: results, mountPath: /results}
          resources:
            requests: {cpu: "14", memory: "40Gi"}
            limits: {cpu: "14", memory: "40Gi"}
      volumes:
        - name: results
          persistentVolumeClaim:
            claimName: bench-results
EOF

echo "Waiting for Loom benchmark to complete (this may take several hours)..."
echo "Monitor: kubectl logs -f job/bench-loom-${TIMESTAMP} -n ${BENCH_NAMESPACE}"

# Wait indefinitely (no timeout) — poll every 30s
while true; do
    status=$(kubectl get job "bench-loom-${TIMESTAMP}" -n "${BENCH_NAMESPACE}" \
        -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}' 2>/dev/null)
    failed=$(kubectl get job "bench-loom-${TIMESTAMP}" -n "${BENCH_NAMESPACE}" \
        -o jsonpath='{.status.conditions[?(@.type=="Failed")].status}' 2>/dev/null)

    if [ "${status}" = "True" ]; then
        echo "Loom benchmark complete."
        break
    fi
    if [ "${failed}" = "True" ]; then
        echo "Loom benchmark FAILED."
        kubectl logs "job/bench-loom-${TIMESTAMP}" -n "${BENCH_NAMESPACE}" --tail=30
        echo ""
        echo "To resume: bash deploy/benchmark/full-benchmark.sh --skip-build"
        exit 1
    fi
    sleep 30
done

kubectl logs "job/bench-loom-${TIMESTAMP}" -n "${BENCH_NAMESPACE}" | grep -E "=== |throughput|ERROR"

# --- Step 5: Swap to LangGraph and run comparison ---
echo "=== Step 5/6: Swapping to LangGraph server (same node, same resources) ==="
kubectl delete deployment loom-bench-server -n "${BENCH_NAMESPACE}" --grace-period=10
sleep 10

kubectl apply -f "${SCRIPT_DIR}/langgraph-deployment.yaml"
kubectl rollout status deployment/langgraph-bench-server -n "${BENCH_NAMESPACE}"

cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: bench-langgraph-${TIMESTAMP}
  namespace: ${BENCH_NAMESPACE}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      serviceAccountName: bench-harness
      nodeSelector:
        loom-role: harness
      tolerations:
        - key: loom-role
          value: harness
          effect: NoSchedule
      containers:
        - name: harness
          image: ${BENCH_IMAGE}:latest
          imagePullPolicy: Always
          command: ["/loom-bench-harness"]
          args:
            - "--server-addr=langgraph-bench-server:50051"
            - "--http-addr=langgraph-bench-server:50051"
            - "--langgraph-addr=langgraph-bench-server:50051"
            - "--scenario=comparison_langgraph_only"
            - "--runs=${RUNS}"
            - "--warmup-runs=${WARMUP_RUNS}"
            - "--output-dir=/results"
          volumeMounts:
            - {name: results, mountPath: /results}
          resources:
            requests: {cpu: "14", memory: "40Gi"}
            limits: {cpu: "14", memory: "40Gi"}
      volumes:
        - name: results
          persistentVolumeClaim:
            claimName: bench-results
EOF

echo "Waiting for LangGraph benchmark..."
while true; do
    status=$(kubectl get job "bench-langgraph-${TIMESTAMP}" -n "${BENCH_NAMESPACE}" \
        -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}' 2>/dev/null)
    failed=$(kubectl get job "bench-langgraph-${TIMESTAMP}" -n "${BENCH_NAMESPACE}" \
        -o jsonpath='{.status.conditions[?(@.type=="Failed")].status}' 2>/dev/null)
    if [ "${status}" = "True" ]; then break; fi
    if [ "${failed}" = "True" ]; then
        echo "LangGraph benchmark failed."
        kubectl logs "job/bench-langgraph-${TIMESTAMP}" -n "${BENCH_NAMESPACE}" --tail=20
        break
    fi
    sleep 15
done

kubectl logs "job/bench-langgraph-${TIMESTAMP}" -n "${BENCH_NAMESPACE}" | grep -E "=== |throughput|ERROR"

# --- Step 6: Collect results ---
echo "=== Step 6/6: Collecting results ==="
mkdir -p "${RESULTS_DIR}"
kubectl logs "job/bench-loom-${TIMESTAMP}" -n "${BENCH_NAMESPACE}" > "${RESULTS_DIR}/loom-log.txt" 2>&1
kubectl logs "job/bench-langgraph-${TIMESTAMP}" -n "${BENCH_NAMESPACE}" > "${RESULTS_DIR}/langgraph-log.txt" 2>&1

# Always pull results from PVC to local disk
echo "Pulling JSON results from PVC to ${RESULTS_DIR}..."
bash "${SCRIPT_DIR}/pull-results.sh" "${RESULTS_DIR}"

if [ "${CLEANUP}" = "true" ]; then
    echo "Scaling down node pools..."
    bash "${SCRIPT_DIR}/scale-down.sh"
fi

echo ""
echo "=== Benchmark suite complete ==="
echo "Results: ${RESULTS_DIR}/"
echo ""
echo "To resume after a crash: bash deploy/benchmark/full-benchmark.sh --skip-build"
