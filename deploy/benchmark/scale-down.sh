#!/usr/bin/env bash
# scale-down.sh — Scale benchmark node pools to 0 to save costs.
# Re-scale with: az aks nodepool scale ... --node-count 1

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=bench.env
source "${SCRIPT_DIR}/bench.env"
[[ -f "${SCRIPT_DIR}/bench.env.local" ]] && source "${SCRIPT_DIR}/bench.env.local"

echo "Scaling loomserver and loomharness node pools to 0..."

az aks nodepool scale \
    --resource-group "${BENCH_RESOURCE_GROUP}" \
    --cluster-name "${BENCH_CLUSTER_NAME}" \
    --name loomserver \
    --node-count 0 \
    -o none &

az aks nodepool scale \
    --resource-group "${BENCH_RESOURCE_GROUP}" \
    --cluster-name "${BENCH_CLUSTER_NAME}" \
    --name loomharness \
    --node-count 0 \
    -o none &

wait
echo "Node pools scaled to 0. Cluster is still running (system pool active)."
echo "Cost is now ~\$0.10/hr (system pool only)."
