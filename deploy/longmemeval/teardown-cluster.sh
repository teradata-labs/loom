#!/usr/bin/env bash
# teardown-cluster.sh — Delete the LongMemEval cluster and resource group.
#
# SAFETY: results on the lme-results PVC represent days of paid LLM compute
# and are destroyed with the cluster. This script refuses to run unless you
# confirm the results have been pulled (pull-results.sh) by setting
# LME_CONFIRM_RESULTS_PULLED=1.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lme.env
source "${SCRIPT_DIR}/lme.env"
[[ -f "${SCRIPT_DIR}/lme.env.local" ]] && source "${SCRIPT_DIR}/lme.env.local"

if [[ "${LME_CONFIRM_RESULTS_PULLED:-0}" != "1" ]]; then
    echo "REFUSING to tear down: the lme-results PVC (and every benchmark result on it)"
    echo "will be destroyed with the cluster."
    echo ""
    echo "  1. bash ${SCRIPT_DIR}/pull-results.sh"
    echo "  2. verify the local copy"
    echo "  3. LME_CONFIRM_RESULTS_PULLED=1 bash ${SCRIPT_DIR}/teardown-cluster.sh"
    exit 1
fi

echo "Deleting resource group ${LME_RESOURCE_GROUP} (cluster, PVCs, everything)..."
az group delete --name "${LME_RESOURCE_GROUP}" --yes --no-wait
echo "Deletion started (runs in background in Azure)."
