#!/usr/bin/env bash
# pull-results.sh — Copy all benchmark results from the AKS PVC to local disk.
#
# This mounts the PVC in a temporary pod, tars the contents, and streams
# them to a local directory. Works with distroless images (no shell needed
# in the harness/server pods).
#
# Usage:
#   bash deploy/benchmark/pull-results.sh [LOCAL_DIR]
#   # Default: ./results/pulled-YYYYMMDD-HHMMSS/

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=bench.env
source "${SCRIPT_DIR}/bench.env"
[[ -f "${SCRIPT_DIR}/bench.env.local" ]] && source "${SCRIPT_DIR}/bench.env.local"

TIMESTAMP="$(date -u +%Y%m%d-%H%M%S)"
LOCAL_DIR="${1:-./results/pulled-${TIMESTAMP}}"

echo "=== Pulling benchmark results from PVC ==="
echo "Namespace: ${BENCH_NAMESPACE}"
echo "PVC:       bench-results"
echo "Local dir: ${LOCAL_DIR}"
echo ""

kubectl config use-context "${BENCH_CLUSTER_NAME}" 2>/dev/null

# Verify PVC exists
if ! kubectl get pvc bench-results -n "${BENCH_NAMESPACE}" &>/dev/null; then
    echo "ERROR: PVC bench-results not found in namespace ${BENCH_NAMESPACE}"
    exit 1
fi

# Create a temporary alpine pod that mounts the PVC (alpine has tar/sh)
POD_NAME="results-puller-${TIMESTAMP}"

echo "Creating temporary pod to access PVC..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
  namespace: ${BENCH_NAMESPACE}
spec:
  restartPolicy: Never
  containers:
    - name: puller
      image: alpine:3.21
      command: ["sleep", "3600"]
      volumeMounts:
        - name: results
          mountPath: /results
          readOnly: true
      resources:
        requests:
          cpu: "100m"
          memory: "128Mi"
        limits:
          cpu: "500m"
          memory: "256Mi"
  volumes:
    - name: results
      persistentVolumeClaim:
        claimName: bench-results
EOF

echo "Waiting for puller pod to be ready..."
kubectl wait --for=condition=Ready "pod/${POD_NAME}" -n "${BENCH_NAMESPACE}" --timeout=120s

# List what's on the PVC
echo ""
echo "=== Files on PVC ==="
kubectl exec "${POD_NAME}" -n "${BENCH_NAMESPACE}" -- ls -lh /results/
echo ""

# Count files
FILE_COUNT=$(kubectl exec "${POD_NAME}" -n "${BENCH_NAMESPACE}" -- sh -c 'ls /results/*.json 2>/dev/null | wc -l')
echo "Found ${FILE_COUNT} JSON result files."

if [ "${FILE_COUNT}" -eq 0 ]; then
    echo "No results to pull."
    kubectl delete pod "${POD_NAME}" -n "${BENCH_NAMESPACE}" --grace-period=5
    exit 0
fi

# Copy files locally
mkdir -p "${LOCAL_DIR}"
echo "Copying results to ${LOCAL_DIR}..."
kubectl cp "${BENCH_NAMESPACE}/${POD_NAME}:/results" "${LOCAL_DIR}"

# Clean up the puller pod
kubectl delete pod "${POD_NAME}" -n "${BENCH_NAMESPACE}" --grace-period=5

# Show what we got
echo ""
echo "=== Downloaded results ==="
ls -lh "${LOCAL_DIR}"/*.json 2>/dev/null | head -30
TOTAL_SIZE=$(du -sh "${LOCAL_DIR}" | cut -f1)
LOCAL_COUNT=$(ls "${LOCAL_DIR}"/*.json 2>/dev/null | wc -l)
echo ""
echo "${LOCAL_COUNT} files, ${TOTAL_SIZE} total"
echo "Saved to: ${LOCAL_DIR}"
