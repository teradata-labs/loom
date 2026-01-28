
# Docker Backend Guide

Learn how to use Loom's Docker backend for secure, containerized code execution with automatic distributed tracing. This guide shows how to programmatically execute Python, Node.js, or custom code in isolated Docker containers.

**Status**: ✅ Available (v1.0.0-beta.2)

> **Note:** The Docker backend is a Go library (`pkg/docker`). There is currently no CLI interface. For CLI-based code execution, see the [planned roadmap](https://github.com/teradata-labs/loom/issues).


## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Common Tasks](#common-tasks)
- [Examples](#examples)
- [Configuration Reference](#configuration-reference)
- [Troubleshooting](#troubleshooting)
- [Performance Tips](#performance-tips)
- [Security Best Practices](#security-best-practices)
- [Next Steps](#next-steps)


## Overview

The Docker backend executes code in isolated Docker containers with:
- **Runtimes**: Python 3.11, Node.js 20, custom Docker images
- **Security**: Non-root user, resource limits, network isolation
- **Observability**: Automatic distributed tracing to Hawk
- **Performance**: Container pooling (50-100ms warm, 1-3s cold start)
- **Package Management**: Automatic pip/npm installation

**Use Cases**:
1. **AI Agent Code Execution**: Run LLM-generated Python/Node.js code safely
2. **MCP Server Hosting**: Containerize MCP servers (Teradata, GitHub, filesystem)
3. **Multi-Language Toolchains**: Support non-SQL domains (Rust, Ruby, etc.)


## Prerequisites

Before using the Docker backend:

1. **Docker Daemon** must be running:
   ```bash
   docker info
   # Should show Docker version and system info
   ```

2. **Docker Images** (automatically pulled on first use):
   - Python: `python:3.11-slim` (~180MB)
   - Node.js: `node:20-slim` (~140MB)
   - Custom: User-specified image

3. **Go Development Environment** (for programmatic usage):
   ```bash
   go version
   # Should be Go 1.21+
   ```

4. **Hawk Service** (optional, for distributed tracing):
   ```bash
   # Optional: Run Hawk for observability
   # See: /docs/guides/integration/observability/
   ```


## Quick Start

### Minimal Python Execution

```go
package main

import (
	"context"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/docker"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

func main() {
	ctx := context.Background()

	// Create logger
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	// Create scheduler
	scheduler, err := docker.NewLocalScheduler(ctx, docker.LocalSchedulerConfig{
		Logger: logger,
	})
	if err != nil {
		panic(err)
	}
	defer scheduler.Close()

	// Create executor with mock tracer (or use real Hawk tracer)
	executor, err := docker.NewDockerExecutor(ctx, docker.DockerExecutorConfig{
		Scheduler: scheduler,
		Logger:    logger,
		Tracer:    observability.NewMockTracer(),
	})
	if err != nil {
		panic(err)
	}
	defer executor.Close()

	// Execute Python code
	req := &loomv1.ExecuteRequest{
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		Command:     []string{"python3", "-c", "print('Hello from Docker!')"},
		Config: &loomv1.DockerBackendConfig{
			Name:        "hello-python",
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
			ImageSource: &loomv1.DockerBackendConfig_BaseImage{
				BaseImage: "python:3.11-slim",
			},
		},
	}

	resp, err := executor.Execute(ctx, req)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Exit Code: %d\n", resp.ExitCode)
	fmt.Printf("Stdout: %s\n", resp.Stdout)
	fmt.Printf("Stderr: %s\n", resp.Stderr)
}
```

**Expected Output**:
```
Exit Code: 0
Stdout: Hello from Docker!
Stderr:
```


## Common Tasks

### Task 1: Execute Python Script with pip Packages

Install packages dynamically before execution:

```go
req := &loomv1.ExecuteRequest{
	RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
	Command:     []string{"python3", "-c", `
import requests
import pandas as pd

response = requests.get('https://api.github.com/repos/teradata-labs/loom')
print(f"Stars: {response.json()['stargazers_count']}")
`},
	Config: &loomv1.DockerBackendConfig{
		Name:        "python-with-packages",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		ImageSource: &loomv1.DockerBackendConfig_BaseImage{
			BaseImage: "python:3.11-slim",
		},
		Packages: []string{"requests", "pandas"},
	},
}

resp, err := executor.Execute(ctx, req)
if err != nil {
	log.Fatalf("Execution failed: %v", err)
}
```

**What happens**:
1. Docker backend creates Python container (or reuses existing)
2. Installs `requests` and `pandas` via `pip install --no-cache-dir`
3. Executes script inside container
4. Returns output and exit code

**Performance**:
- First run: 3-5 seconds (pip install)
- Subsequent runs: 50-100ms (warm container pool)


### Task 2: Execute Node.js Script with npm Packages

Run Node.js code with npm dependencies:

```go
req := &loomv1.ExecuteRequest{
	RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_NODE,
	Command:     []string{"node", "-e", `
const axios = require('axios');
const _ = require('lodash');

async function main() {
  const response = await axios.get('https://api.github.com/repos/teradata-labs/loom');
  console.log('Stars:', _.get(response, 'data.stargazers_count'));
}

main();
`},
	Config: &loomv1.DockerBackendConfig{
		Name:        "node-with-packages",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_NODE,
		ImageSource: &loomv1.DockerBackendConfig_BaseImage{
			BaseImage: "node:20-slim",
		},
		Packages: []string{"axios", "lodash"},
	},
}
```


### Task 3: Execute Code with Custom Docker Image

Use a custom Docker image for specialized environments:

```go
req := &loomv1.ExecuteRequest{
	RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_CUSTOM,
	Command:     []string{"python3", "-c", "print('Running in custom image')"},
	Config: &loomv1.DockerBackendConfig{
		Name:        "custom-image",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_CUSTOM,
		ImageSource: &loomv1.DockerBackendConfig_CustomImage{
			CustomImage: "myregistry/custom-python:latest",
		},
	},
}
```

**Use cases**:
- Pre-installed dependencies (no install step)
- Specialized tools (ffmpeg, ImageMagick, etc.)
- Company-approved base images with security scanning


### Task 4: Configure Container Resource Limits

Set CPU, memory, and storage limits:

```go
req := &loomv1.ExecuteRequest{
	RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
	Command:     []string{"python3", "-c", "print('Resource-limited execution')"},
	Config: &loomv1.DockerBackendConfig{
		Name:        "resource-limited",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		ImageSource: &loomv1.DockerBackendConfig_BaseImage{
			BaseImage: "python:3.11-slim",
		},
		ResourceLimits: &loomv1.ResourceLimits{
			CpuCores:                1.0,   // 1 CPU core
			MemoryMb:                512,   // 512 MB RAM
			StorageMb:               1024,  // 1 GB disk
			PidsLimit:               100,   // Max 100 processes
			ExecutionTimeoutSeconds: 60,    // Kill after 60s
		},
	},
}
```


### Task 5: Configure Container Rotation

Containers are automatically rotated to prevent state accumulation:

```go
req := &loomv1.ExecuteRequest{
	RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
	Command:     []string{"python3", "-c", "print('Hello')"},
	Config: &loomv1.DockerBackendConfig{
		Name:        "rotated-container",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		ImageSource: &loomv1.DockerBackendConfig_BaseImage{
			BaseImage: "python:3.11-slim",
		},
		Lifecycle: &loomv1.ContainerLifecycleConfig{
			MaxExecutions:             1000, // Rotate after 1000 executions (default)
			RotationIntervalHours:     4,    // Rotate after 4 hours (default)
			HealthCheckIntervalSeconds: 60,   // Health check every 60s
			AutoCleanup:               true, // Clean up failed containers
		},
	},
}
```

**Rotation triggers**:
- After 1000 executions (default)
- After 4 hours of age (default)
- When health check fails

**Note**: Container rotation is not truly zero-downtime. New container created on next execution after old one removed.


## Examples

### Example 1: Data Processing with Distributed Tracing

Execute Python script with automatic trace span creation:

```python
# data_processor.py
from loom_trace import tracer, trace_span
import pandas as pd

def main():
    # loom_trace library automatically installed and imported
    print(f"Trace ID: {tracer.trace_id}")
    print(f"Parent Span ID: {tracer.parent_span_id}")

    with trace_span("load_data"):
        # Simulate data loading
        df = pd.DataFrame({"col": [1, 2, 3, 4, 5]})

    with trace_span("process_data"):
        # Process data
        result = df["col"].sum()
        print(f"Sum: {result}")

    with trace_span("save_results"):
        # Save results
        print("Results saved")

if __name__ == "__main__":
    main()
```

Execute with Hawk tracer:

```go
// Create Hawk tracer (instead of mock)
hawkTracer, err := observability.NewHawkTracer(observability.HawkConfig{
	Endpoint: "localhost:50051",
	ServiceName: "loom-docker",
})
if err != nil {
	panic(err)
}

executor, err := docker.NewDockerExecutor(ctx, docker.DockerExecutorConfig{
	Scheduler: scheduler,
	Logger:    logger,
	Tracer:    hawkTracer, // Use Hawk tracer
})

// Read script from file
script, _ := os.ReadFile("data_processor.py")

req := &loomv1.ExecuteRequest{
	RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
	Command:     []string{"python3", "-c", string(script)},
	Config: &loomv1.DockerBackendConfig{
		Name:        "data-processor",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		ImageSource: &loomv1.DockerBackendConfig_BaseImage{
			BaseImage: "python:3.11-slim",
		},
		Packages: []string{"pandas"},
	},
}

resp, err := executor.Execute(ctx, req)
```

**Trace hierarchy in Hawk**:
```
docker.execute (host span)
├── load_data (container span)
├── process_data (container span)
└── save_results (container span)
```


### Example 2: Node.js API Integration with Error Handling

Execute Node.js code with automatic trace capture:

```javascript
// api_integration.js
const { tracer, traceSpanSync } = require('loom-trace.js');
const axios = require('axios');

async function main() {
    console.log(`Trace ID: ${tracer.traceId}`);

    try {
        await traceSpanSync('fetch_user_data', { api: 'github' }, async () => {
            const response = await axios.get('https://api.github.com/users/octocat');
            console.log(`User: ${response.data.login}`);
            console.log(`Public repos: ${response.data.public_repos}`);
        });
    } catch (error) {
        // Span automatically marked as error
        console.error('API call failed:', error.message);
        process.exit(1);
    }

    await traceSpanSync('process_response', {}, async () => {
        console.log('Processing completed');
    });
}

main();
```

Execute:

```go
script, _ := os.ReadFile("api_integration.js")

req := &loomv1.ExecuteRequest{
	RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_NODE,
	Command:     []string{"node", "-e", string(script)},
	Config: &loomv1.DockerBackendConfig{
		Name:        "api-integration",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_NODE,
		ImageSource: &loomv1.DockerBackendConfig_BaseImage{
			BaseImage: "node:20-slim",
		},
		Packages: []string{"axios"},
	},
}
```


### Example 3: Custom Image with Pre-Installed Tools

Create a custom Docker image with pre-installed tools:

```dockerfile
# Dockerfile
FROM python:3.11-slim

# Install system tools
RUN apt-get update && apt-get install -y \
    ffmpeg \
    imagemagick \
    && rm -rf /var/lib/apt/lists/*

# Install Python packages
RUN pip install --no-cache-dir \
    pillow \
    opencv-python \
    numpy

# Set non-root user (security best practice)
USER nobody
```

Build and push:
```bash
docker build -t mycompany/media-processor:v1 .
docker push mycompany/media-processor:v1
```

Use in Loom:

```go
req := &loomv1.ExecuteRequest{
	RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_CUSTOM,
	Command:     []string{"python3", "-c", `
import cv2
print('OpenCV version:', cv2.__version__)
# Process images with OpenCV
`},
	Config: &loomv1.DockerBackendConfig{
		Name:        "media-processor",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_CUSTOM,
		ImageSource: &loomv1.DockerBackendConfig_CustomImage{
			CustomImage: "mycompany/media-processor:v1",
		},
	},
}
```


## Configuration Reference

### ExecuteRequest Proto

```protobuf
message ExecuteRequest {
  RuntimeType runtime_type = 1;              // PYTHON | NODE | CUSTOM
  repeated string command = 2;               // Command to execute
  DockerBackendConfig config = 3;            // Docker configuration
  map<string, string> env = 4;               // User environment variables
  bytes stdin = 5;                           // Optional stdin input
}
```

### DockerBackendConfig Proto

```protobuf
message DockerBackendConfig {
  string name = 1;                           // Container name/ID
  RuntimeType runtime_type = 2;              // Runtime type
  oneof image_source {
    string base_image = 3;                   // Base image (e.g., python:3.11-slim)
    string custom_image = 4;                 // Custom image (e.g., mycompany/app:v1)
  }
  repeated string packages = 5;              // Packages to install (pip/npm)
  ResourceLimits resource_limits = 7;        // CPU, memory, storage limits
  ContainerLifecycleConfig lifecycle = 40;   // Rotation and cleanup config
}
```

### ResourceLimits Proto

```protobuf
message ResourceLimits {
  double cpu_cores = 1;                      // CPU cores (default: 1.0)
  int64 memory_mb = 2;                       // Memory in MB (default: 512)
  int64 storage_mb = 3;                      // Storage in MB (default: 1024)
  int32 pids_limit = 4;                      // Max processes (default: 100)
  int32 execution_timeout_seconds = 5;       // Timeout in seconds (default: 300)
}
```

### ContainerLifecycleConfig Proto

```protobuf
message ContainerLifecycleConfig {
  int32 rotation_interval_hours = 1;         // Rotate after N hours (default: 4)
  int32 max_executions = 2;                  // Rotate after N executions (default: 1000)
  int32 health_check_interval_seconds = 3;   // Health check frequency (default: 60)
  bool auto_cleanup = 4;                     // Auto-cleanup failed containers (default: true)
}
```


## Troubleshooting

### Issue: "Cannot connect to Docker daemon"

**Symptoms**:
```
Error: Cannot connect to the Docker daemon at unix:///var/run/docker.sock
```

**Solution**:
```bash
# Check Docker daemon status
docker info

# If not running, start Docker
sudo systemctl start docker  # Linux
# or use Docker Desktop GUI  # macOS/Windows
```


### Issue: "Container creation timeout"

**Symptoms**:
```
Error: context deadline exceeded
```

**Solutions**:

1. **Check Docker daemon load**:
   ```bash
   docker ps
   # If many containers running, free up resources
   ```

2. **Pull image manually** (for large images):
   ```bash
   docker pull python:3.11-slim
   # Then retry execution
   ```

3. **Increase context timeout** in Go code:
   ```go
   ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
   defer cancel()
   resp, err := executor.Execute(ctx, req)
   ```


### Issue: "Package installation failed"

**Symptoms**:
```
Error: pip install failed: Could not find a version that satisfies...
```

**Solutions**:

1. **Check package name spelling**:
   ```bash
   # Verify package exists
   pip search <package-name>
   ```

2. **Specify version**:
   ```go
   Packages: []string{"requests==2.28.0"},  // Pin version
   ```

3. **Use custom image** with pre-installed packages (see Example 3)


### Issue: "Trace spans not appearing in Hawk"

**Symptoms**:
- Code executes successfully
- No trace spans visible in Hawk UI

**Solutions**:

1. **Verify Hawk tracer configured**:
   ```go
   // Make sure using HawkTracer, not MockTracer
   hawkTracer, err := observability.NewHawkTracer(observability.HawkConfig{
   	Endpoint: "localhost:50051",
   })
   ```

2. **Check trace library imported in container code**:
   ```python
   # Python: Check library loaded
   from loom_trace import tracer
   print(f"Trace ID: {tracer.trace_id}")  # Should print valid ID
   ```

3. **Verify Hawk service reachable**:
   ```bash
   grpcurl localhost:50051 list
   # Should show Hawk service methods
   ```


### Issue: "Permission denied in container"

**Symptoms**:
```
Error: Permission denied: '/app/data.txt'
```

**Solution**:

Containers run as non-root user (`nobody`) by default for security. Use writable directories:

```python
# Good: Use /tmp (world-writable)
with open('/tmp/data.txt', 'w') as f:
    f.write('data')

# Bad: Use /app (read-only for nobody user)
with open('/app/data.txt', 'w') as f:  # Permission denied
    f.write('data')
```


## Performance Tips

### Tip 1: Reuse DockerExecutor Across Executions

Create executor once, reuse for multiple executions:

```go
// DON'T: Create executor per execution (slow)
for i := 0; i < 100; i++ {
	executor, _ := docker.NewDockerExecutor(ctx, config)
	executor.Execute(ctx, req)
	executor.Close()
}

// DO: Reuse executor (fast)
executor, _ := docker.NewDockerExecutor(ctx, config)
defer executor.Close()

for i := 0; i < 100; i++ {
	executor.Execute(ctx, req)
}
```

**Benefit**: Container pool persists across executions (50-100ms warm vs 1-3s cold).


### Tip 2: Use Custom Images for Heavy Dependencies

Instead of installing large packages every time:

```dockerfile
# Dockerfile
FROM python:3.11-slim
RUN pip install --no-cache-dir \
    torch torchvision torchaudio --index-url https://download.pytorch.org/whl/cpu
USER nobody
```

```bash
docker build -t mycompany/pytorch:latest .
```

```go
// Fast execution (no pip install)
req := &loomv1.ExecuteRequest{
	RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_CUSTOM,
	Command:     []string{"python3", "-c", "import torch; print(torch.__version__)"},
	Config: &loomv1.DockerBackendConfig{
		Name:        "pytorch",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_CUSTOM,
		ImageSource: &loomv1.DockerBackendConfig_CustomImage{
			CustomImage: "mycompany/pytorch:latest",
		},
	},
}
```


### Tip 3: Batch Operations in Single Execution

For multiple related operations, combine into single script:

```go
// DON'T: Multiple separate executions (3x container overhead)
executor.Execute(ctx, req1)
executor.Execute(ctx, req2)
executor.Execute(ctx, req3)

// DO: Single execution with multiple operations
script := `
from loom_trace import trace_span

with trace_span("operation_1"):
    process_data_1()

with trace_span("operation_2"):
    process_data_2()

with trace_span("operation_3"):
    process_data_3()
`
executor.Execute(ctx, &loomv1.ExecuteRequest{
	Command: []string{"python3", "-c", script},
	Config:  config,
})
```

**Benefit**: Single container creation, 3x faster than separate calls.


## Security Best Practices

### Practice 1: Use Non-Root Containers

All default runtimes run as `nobody` user. For custom images:

```dockerfile
FROM python:3.11-slim
# ... install packages ...
USER nobody  # Always run as non-root
```


### Practice 2: Set Resource Limits

Prevent resource exhaustion:

```go
ResourceLimits: &loomv1.ResourceLimits{
	CpuCores:                1.0,  // Max 1 CPU core
	MemoryMb:                512,  // Max 512MB RAM
	ExecutionTimeoutSeconds: 60,   // Kill after 60s
	PidsLimit:               100,  // Prevent fork bombs
},
```


### Practice 3: Rotate Containers Frequently

For untrusted code, rotate more aggressively:

```go
Lifecycle: &loomv1.ContainerLifecycleConfig{
	MaxExecutions:         10,  // Rotate after 10 executions (vs default 1000)
	RotationIntervalHours: 1,   // Rotate after 1 hour (vs default 4)
},
```


### Practice 4: Disable Network for Untrusted Code

> **Note:** Network mode configuration is not currently exposed in the proto API. Network is hardcoded to `bridge` mode in `pkg/docker/runtime/*_runtime.go`. See [issue #XXX](https://github.com/teradata-labs/loom/issues) for planned `network_mode` field.


## Next Steps

- **Architecture**: [Docker Backend Architecture](/docs/architecture/docker-backend/) - Deep dive into design decisions
- **Integration**: [Observability Guide](/docs/guides/integration/observability/) - Configure Hawk tracing
- **Integration**: [MCP Integration Guide](/docs/guides/integration/mcp/) - Use MCP servers in containers
- **Examples**: [Docker Backend Examples](/examples/docker/) - Complete example projects


## FAQ

**Q: Is there a CLI interface for Docker backend?**

A: Not yet. The Docker backend is currently a Go library (`pkg/docker`) with no CLI interface. Use the Go API as shown in this guide. CLI commands (`loom execute`, `loom containers list`) are [planned for v1.1](https://github.com/teradata-labs/loom/issues).


**Q: Can I use Docker Compose with Loom?**

A: Not directly. Loom manages individual containers, not multi-container applications. For multi-container setups, use external orchestration or wait for Kubernetes backend.


**Q: How do I debug container execution?**

A: Enable debug logging:

```go
logger, _ := zap.NewDevelopment()  // Debug mode
executor, _ := docker.NewDockerExecutor(ctx, docker.DockerExecutorConfig{
	Logger: logger,
	// ...
})
```

Check logs for:
- Container creation (`Creating container for runtime`)
- Package installation (`Installing packages: [...]`)
- Trace collection (`Collected N spans from container`)


**Q: Does Docker backend work with Podman?**

A: Yes, if Podman socket enabled:

```bash
# Enable Podman socket
systemctl --user enable --now podman.socket

# Configure Docker host
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
```

Then create executor with custom Docker host:

```go
executor, _ := docker.NewDockerExecutor(ctx, docker.DockerExecutorConfig{
	DockerHost: os.Getenv("DOCKER_HOST"),
	// ...
})
```


**Q: How do I monitor container resource usage?**

A: Use `docker stats`:

```bash
docker stats $(docker ps --filter "name=loom-" --format "{{.ID}}")
```

Or integrate with Prometheus via Docker metrics endpoint.


## Version Compatibility

| Loom Version | Docker API | Python Runtime | Node Runtime | Status |
|--------------|------------|----------------|--------------|--------|
| v1.0.0-beta.2 | v1.40+     | 3.11-slim      | 20-slim    | ✅ Current |
| v1.0.0-beta.1 | v1.40+     | 3.11-slim      | 20-alpine    | ⚠️ No trace library injection |

**Minimum Docker Version**: 20.10 (API v1.40)

**Tested On**:
- Docker Desktop 4.30+ (macOS/Windows)
- Docker Engine 25.0+ (Linux)
- Podman 4.0+ (with compatibility mode)
