#!/usr/bin/env bash
# setup-cluster.sh — Create a small AKS cluster for the LongMemEval run.
#
# Unlike deploy/benchmark (CPU-bound load tests on big nodes), this run is
# LLM-bound: the pods mostly wait on Bedrock, so nodes are small and cheap
# (~$0.55/hr total). Sources lme.env; override via lme.env.local.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lme.env
source "${SCRIPT_DIR}/lme.env"
[[ -f "${SCRIPT_DIR}/lme.env.local" ]] && source "${SCRIPT_DIR}/lme.env.local"

echo "=== Step 1/5: Creating resource group ${LME_RESOURCE_GROUP} ==="
az group create --name "${LME_RESOURCE_GROUP}" --location "${LME_LOCATION}" -o none

echo "=== Step 2/5: Creating AKS cluster ${LME_CLUSTER_NAME} ==="
az aks create \
    --resource-group "${LME_RESOURCE_GROUP}" \
    --name "${LME_CLUSTER_NAME}" \
    --node-count 1 \
    --node-vm-size "${LME_SYSTEM_VM_SIZE}" \
    --nodepool-name system \
    --generate-ssh-keys \
    --network-plugin azure \
    -o none

echo "=== Step 3/5: Adding workload node pool (${LME_WORK_VM_SIZE}) ==="
az aks nodepool add \
    --cluster-name "${LME_CLUSTER_NAME}" \
    --resource-group "${LME_RESOURCE_GROUP}" \
    --name loomlme \
    --node-count 1 \
    --node-vm-size "${LME_WORK_VM_SIZE}" \
    --labels loom-role=lme \
    --node-taints loom-role=lme:NoSchedule \
    -o none

echo "=== Step 4/5: Getting cluster credentials ==="
az aks get-credentials \
    --resource-group "${LME_RESOURCE_GROUP}" \
    --name "${LME_CLUSTER_NAME}" \
    --overwrite-existing

echo "=== Step 5/5: Attaching ACR ${LME_ACR_NAME} ==="
az aks update \
    --resource-group "${LME_RESOURCE_GROUP}" \
    --name "${LME_CLUSTER_NAME}" \
    --attach-acr "${LME_ACR_NAME}" \
    -o none

echo ""
echo "=== Cluster ready ==="
kubectl get nodes -o wide
