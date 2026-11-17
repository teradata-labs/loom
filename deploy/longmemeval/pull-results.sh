#!/usr/bin/env bash
# pull-results.sh — Copy LongMemEval results from the AKS PVC to local disk.
# The results PVC is RWX (azurefile), so this works mid-run without
# disturbing the runner.
#
# Usage: bash deploy/longmemeval/pull-results.sh [LOCAL_DIR]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lme.env
source "${SCRIPT_DIR}/lme.env"
[[ -f "${SCRIPT_DIR}/lme.env.local" ]] && source "${SCRIPT_DIR}/lme.env.local"

TIMESTAMP="$(date -u +%Y%m%d-%H%M%S)"
LOCAL_DIR="${1:-./results/lme-pulled-${TIMESTAMP}}"
POD_NAME="lme-results-puller-${TIMESTAMP}"

if ! kubectl get pvc lme-results -n "${LME_NAMESPACE}" &>/dev/null; then
    echo "ERROR: PVC lme-results not found in namespace ${LME_NAMESPACE}"
    exit 1
fi

# Under set -e, any failure below (wait, exec, cp) would otherwise skip the
# final delete and leak the pod — always delete exactly the pod we generated.
cleanup() {
    kubectl delete pod "${POD_NAME}" -n "${LME_NAMESPACE}" --ignore-not-found --grace-period=5
}
trap cleanup EXIT

echo "Creating temporary puller pod..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
  namespace: ${LME_NAMESPACE}
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
        claimName: lme-results
EOF

kubectl wait --for=condition=Ready "pod/${POD_NAME}" -n "${LME_NAMESPACE}" --timeout=120s

echo ""
echo "=== Files on PVC ==="
kubectl exec "${POD_NAME}" -n "${LME_NAMESPACE}" -- sh -c 'ls -lhR /results/ | tail -40; echo; echo "completed chunks (all runs): $(find /results -name "*.done" 2>/dev/null | wc -l)"'

mkdir -p "${LOCAL_DIR}"
kubectl cp "${LME_NAMESPACE}/${POD_NAME}:/results" "${LOCAL_DIR}"

echo ""
echo "Saved to: ${LOCAL_DIR} ($(du -sh "${LOCAL_DIR}" | cut -f1))"
echo "Score with the official evaluator (see README.md § Scoring)."
