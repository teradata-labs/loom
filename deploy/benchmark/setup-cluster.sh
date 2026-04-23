#!/usr/bin/env bash
# setup-cluster.sh — Create a dedicated AKS cluster for Loom benchmarks.
#
# Sources configuration from bench.env. Override by creating bench.env.local.
#
# Cost: ~$1.93/hr when running. Scale to 0 when idle:
#   bash deploy/benchmark/scale-down.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=bench.env
source "${SCRIPT_DIR}/bench.env"
[[ -f "${SCRIPT_DIR}/bench.env.local" ]] && source "${SCRIPT_DIR}/bench.env.local"

echo "=== Step 1/6: Creating resource group ${BENCH_RESOURCE_GROUP} ==="
az group create --name "${BENCH_RESOURCE_GROUP}" --location "${BENCH_LOCATION}" -o none

echo "=== Step 2/6: Creating AKS cluster ${BENCH_CLUSTER_NAME} ==="
az aks create \
    --resource-group "${BENCH_RESOURCE_GROUP}" \
    --name "${BENCH_CLUSTER_NAME}" \
    --node-count 1 \
    --node-vm-size "${BENCH_SYSTEM_VM_SIZE}" \
    --nodepool-name system \
    --generate-ssh-keys \
    --network-plugin azure \
    --zones "${BENCH_ZONE}" \
    -o none

echo "=== Step 3/6: Adding server node pool (${BENCH_SERVER_VM_SIZE}) ==="
az aks nodepool add \
    --cluster-name "${BENCH_CLUSTER_NAME}" \
    --resource-group "${BENCH_RESOURCE_GROUP}" \
    --name loomserver \
    --node-count 1 \
    --node-vm-size "${BENCH_SERVER_VM_SIZE}" \
    --labels loom-role=server \
    --node-taints loom-role=server:NoSchedule \
    --zones "${BENCH_ZONE}" \
    -o none

echo "=== Step 4/6: Adding harness node pool (${BENCH_HARNESS_VM_SIZE}) ==="
az aks nodepool add \
    --cluster-name "${BENCH_CLUSTER_NAME}" \
    --resource-group "${BENCH_RESOURCE_GROUP}" \
    --name loomharness \
    --node-count 1 \
    --node-vm-size "${BENCH_HARNESS_VM_SIZE}" \
    --labels loom-role=harness \
    --node-taints loom-role=harness:NoSchedule \
    --zones "${BENCH_ZONE}" \
    -o none

echo "=== Step 5/6: Getting cluster credentials ==="
az aks get-credentials \
    --resource-group "${BENCH_RESOURCE_GROUP}" \
    --name "${BENCH_CLUSTER_NAME}" \
    --overwrite-existing

echo "=== Step 6/6: Attaching ACR ${BENCH_ACR_NAME} ==="
az aks update \
    --resource-group "${BENCH_RESOURCE_GROUP}" \
    --name "${BENCH_CLUSTER_NAME}" \
    --attach-acr "${BENCH_ACR_NAME}" \
    -o none

echo ""
echo "=== Cluster ready ==="
kubectl get nodes -o wide
echo ""
echo "Context set to: $(kubectl config current-context)"
