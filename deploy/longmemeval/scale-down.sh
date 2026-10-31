#!/usr/bin/env bash
# scale-down.sh — Scale the workload node pool to 0 when idle.
# The results and data PVCs survive; scale back up with:
#   az aks nodepool scale ... --node-count 1

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lme.env
source "${SCRIPT_DIR}/lme.env"
[[ -f "${SCRIPT_DIR}/lme.env.local" ]] && source "${SCRIPT_DIR}/lme.env.local"

echo "Scaling workload pool to 0 (server and any running job will stop)..."
az aks nodepool scale \
    --cluster-name "${LME_CLUSTER_NAME}" \
    --resource-group "${LME_RESOURCE_GROUP}" \
    --name loomlme \
    --node-count 0 \
    -o none

echo "Done. Idle cost is the system pool + storage only."
