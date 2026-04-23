# Loom Benchmark Suite

Publication-grade benchmarks for the Loom agent framework, designed to run on Azure Kubernetes Service (AKS) with dedicated node pools for reproducible results.

## Quick Start

```bash
# One-time: create the AKS cluster (~10 min)
bash deploy/benchmark/setup-cluster.sh

# Run the full suite (~6 hours)
bash deploy/benchmark/full-benchmark.sh

# Run a single scenario (~15 min)
bash deploy/benchmark/full-benchmark.sh --scenario=sustained_load --runs=3

# Scale down when done (~$0.10/hr idle vs ~$1.93/hr active)
bash deploy/benchmark/scale-down.sh
```

## Architecture

```
                 AKS Cluster (loom-bench-aks)
    ┌──────────────────┐     ┌──────────────────┐
    │  Server Node      │     │  Harness Node     │
    │  E32s_v5 (32 CPU) │     │  E16s_v5 (16 CPU) │
    │                   │     │                    │
    │  ┌─────────────┐  │     │  ┌──────────────┐  │
    │  │ loom-bench-  │◄─┼─gRPC─┤ loom-bench-   │  │
    │  │ server       │  │     │  │ harness       │  │
    │  │ (31 CPU,     │  │     │  │ (drives load) │  │
    │  │  112Gi)      │  │     │  └──────────────┘  │
    │  └─────────────┘  │     │                    │
    └──────────────────┘     └──────────────────┘
```

Both pods use Guaranteed QoS (requests == limits) to prevent CPU throttling.

## Configuration

All environment-specific values live in `bench.env`. To override without editing:

```bash
cp deploy/benchmark/bench.env deploy/benchmark/bench.env.local
# Edit bench.env.local with your values
```

| Variable | Default | Description |
|----------|---------|-------------|
| `BENCH_RESOURCE_GROUP` | `loom-bench` | Azure resource group |
| `BENCH_CLUSTER_NAME` | `loom-bench-aks` | AKS cluster name |
| `BENCH_LOCATION` | `eastus` | Azure region |
| `BENCH_ACR_NAME` | `loomcloudacr` | Azure Container Registry |
| `BENCH_SERVER_VM_SIZE` | `Standard_E32s_v5` | Server node VM (32 vCPU) |
| `BENCH_HARNESS_VM_SIZE` | `Standard_E16s_v5` | Harness node VM (16 vCPU) |

## Scenarios

| # | Name | Duration | Description |
|---|------|----------|-------------|
| 1 | `sustained_load` | ~22 min | 120s at 20 workers, proves throughput stability |
| 2 | `concurrency_scaling` | ~64 min | 1-1024 workers, finds the scaling cliff |
| 3 | `peak_throughput` | ~47 min | 0ms LLM, finds absolute ceiling |
| 4 | `memory_pressure` | ~20 min | 1K-500K sessions, finds OOM boundary |
| 5 | `multi_turn` | ~35 min | 10-2000 turns, finds compression wall |
| 6 | `realistic_llm` | ~53 min | 100/500/1000ms latency profiles |
| 7 | `fresh_vs_reused` | ~10 min | Quantifies session creation cost |
| 8 | `session_contention` | ~23 min | 1-200 workers on one session |
| 9 | `multi_agent` | ~12 min | 1-200 agents |
| 10 | `error_resilience` | ~22 min | 10-70% error injection |
| 11 | `streamweave` | ~18 min | Streaming throughput scaling |
| 12 | `cold_start` | ~10 min | First-request latency |

Run a single scenario: `--scenario=sustained_load`
Run all: `--scenario=all`
Run multiple: `--scenario=sustained_load,cold_start`

## LangGraph Comparison

The comparison runs both frameworks on the **same server node** with identical resources (31 CPU, 112Gi). The orchestration script deploys them sequentially to ensure fair resource allocation.

```bash
bash deploy/benchmark/full-benchmark.sh --scenario=sustained_load
```

The full-benchmark script automatically runs LangGraph after the Loom scenarios complete.

## Output

Results are written as JSON to a PersistentVolumeClaim (`bench-results`). Each scenario produces a JSON file matching the schema in the spec (Section 6):

```json
{
  "environment": { "cpu_model": "...", "ram_total_mb": ..., ... },
  "scenario": "sustained_load",
  "config": { "concurrency": 20, "duration_ms": 120000, ... },
  "runs": [{ "throughput_rps": ..., "latency_p50_us": ..., ... }],
  "aggregate": { "throughput_rps": { "median": ..., "mean": ..., "cv_pct": ... } }
}
```

Summary logs are printed to stderr (visible via `kubectl logs`).

## Cost

| Component | Cost/hr | Notes |
|-----------|---------|-------|
| E32s_v5 (server) | ~$1.22 | Scale to 0 when idle |
| E16s_v5 (harness) | ~$0.61 | Scale to 0 when idle |
| D2s_v5 (system) | ~$0.10 | Always running |
| **Full suite** | ~$1.93/hr | ~$11 for 6 hours |
| **Idle** | ~$0.10/hr | System pool only |

Scale down after benchmarking: `bash deploy/benchmark/scale-down.sh`
Tear down everything: `bash deploy/benchmark/teardown-cluster.sh`

## Files

```
deploy/
  Dockerfile              # Multi-stage build for bench binaries
  benchmark/
    bench.env             # Environment configuration
    setup-cluster.sh      # Create AKS cluster + node pools
    teardown-cluster.sh   # Delete everything
    scale-down.sh         # Scale pools to 0
    full-benchmark.sh     # Full suite orchestration
    poc-run.sh            # Quick PoC validation
    namespace.yaml        # K8s namespace
    server-deployment.yaml # Loom server pod
    server-service.yaml   # ClusterIP service
    harness-job.yaml      # Benchmark harness job
    langgraph-deployment.yaml # LangGraph comparison server
    langgraph-service.yaml
    rbac.yaml             # ServiceAccount for harness
    results-pvc.yaml      # PVC for JSON results
  comparison/
    langgraph/            # LangGraph comparison agent
```
