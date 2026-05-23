#!/usr/bin/env bash
# teardown-cluster.sh — Delete the benchmark AKS cluster and its resource group.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=bench.env
source "${SCRIPT_DIR}/bench.env"
[[ -f "${SCRIPT_DIR}/bench.env.local" ]] && source "${SCRIPT_DIR}/bench.env.local"

echo "Deleting resource group ${BENCH_RESOURCE_GROUP} and all resources within it..."
echo "This includes the ${BENCH_CLUSTER_NAME} cluster and all node pools."
echo ""
read -p "Are you sure? (y/N) " -n 1 -r
echo ""
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Aborted."
    exit 0
fi

az group delete --name "${BENCH_RESOURCE_GROUP}" --yes --no-wait
echo "Deletion initiated (async). Resource group will be removed in a few minutes."
