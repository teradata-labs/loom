// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/docker/runtime"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

const (
	// BaggageKeyTenantID is the W3C baggage key for tenant identification.
	BaggageKeyTenantID = "tenant_id"
	// BaggageKeyOrgID is the W3C baggage key for organization identification.
	BaggageKeyOrgID = "org_id"
)

// DockerExecutor handles Docker container lifecycle and execution.
//
// Responsibilities:
//   - Container creation using runtime strategies
//   - Command execution in containers
//   - stdout/stderr capture
//   - Container health checks
//   - Container rotation (time-based and execution-based)
//   - Resource cleanup
//
// Container Lifecycle:
//  1. GetOrCreateContainer() via scheduler
//  2. If new container: CreateContainer() with runtime config
//  3. Execute() command in container
//  4. Check rotation criteria (executions >= max OR time >= interval)
//  5. If rotation needed: RemoveContainer(), create new on next execution
//
// Thread Safety: All methods are thread-safe.
type DockerExecutor struct {
	scheduler      ContainerScheduler
	dockerClient   *client.Client
	runtimes       map[loomv1.RuntimeType]runtime.Runtime
	logger         *zap.Logger
	tracer         observability.Tracer
	traceCollector *TraceCollector
}

// DockerExecutorConfig configures DockerExecutor.
type DockerExecutorConfig struct {
	// DockerHost is the Docker daemon endpoint (default: "unix:///var/run/docker.sock")
	DockerHost string

	// Scheduler is the container scheduler (required)
	Scheduler ContainerScheduler

	// Logger is the zap logger (required)
	Logger *zap.Logger

	// Tracer is the observability tracer (optional - if nil, tracing is disabled)
	Tracer observability.Tracer
}

// NewDockerExecutor creates a new DockerExecutor instance.
func NewDockerExecutor(ctx context.Context, config DockerExecutorConfig) (*DockerExecutor, error) {
	if config.Scheduler == nil {
		return nil, fmt.Errorf("scheduler is required")
	}

	if config.Logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	// Detect Docker host if not explicitly provided
	if config.DockerHost == "" {
		config.DockerHost = detectDockerHost()
	}

	config.Logger.Info("creating docker executor", zap.String("docker_host", config.DockerHost))

	// Create Docker client
	dockerClient, err := client.NewClientWithOpts(
		client.WithHost(config.DockerHost),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		config.Logger.Error("failed to create Docker client", zap.Error(err))
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	// Verify Docker daemon is reachable
	config.Logger.Debug("pinging Docker daemon")
	if _, err := dockerClient.Ping(ctx); err != nil {
		dockerClient.Close()
		config.Logger.Error("failed to ping Docker daemon", zap.Error(err))
		return nil, fmt.Errorf("failed to ping Docker daemon: %w", err)
	}

	config.Logger.Info("Docker daemon is reachable")

	// Initialize runtime strategies
	runtimes := map[loomv1.RuntimeType]runtime.Runtime{
		loomv1.RuntimeType_RUNTIME_TYPE_PYTHON: runtime.NewPythonRuntime(),
		loomv1.RuntimeType_RUNTIME_TYPE_NODE:   runtime.NewNodeRuntime(),
		loomv1.RuntimeType_RUNTIME_TYPE_CUSTOM: runtime.NewCustomRuntime(),
		// Future: Add Ruby, Rust, Go runtimes
	}

	config.Logger.Info("docker executor created successfully")

	// Initialize trace collector if tracer is provided
	var traceCollector *TraceCollector
	if config.Tracer != nil {
		var err error
		traceCollector, err = NewTraceCollector(TraceCollectorConfig{
			Tracer: config.Tracer,
			Logger: config.Logger,
		})
		if err != nil {
			dockerClient.Close()
			config.Logger.Error("failed to create trace collector", zap.Error(err))
			return nil, fmt.Errorf("failed to create trace collector: %w", err)
		}
		config.Logger.Info("trace collector initialized for container observability")
	} else {
		config.Logger.Info("tracing disabled (no tracer provided)")
	}

	return &DockerExecutor{
		scheduler:      config.Scheduler,
		dockerClient:   dockerClient,
		runtimes:       runtimes,
		logger:         config.Logger,
		tracer:         config.Tracer,
		traceCollector: traceCollector,
	}, nil
}

// Execute executes a command in a Docker container.
//
// Flow:
//  1. GetOrCreateContainer() from scheduler
//  2. If new container: CreateContainer() with runtime config
//  3. StartContainer() if not running
//  4. Execute command with stdin/stdout/stderr handling
//  5. Check rotation criteria
//  6. Return ExecuteResponse with output and metadata
func (de *DockerExecutor) Execute(ctx context.Context, req *loomv1.ExecuteRequest) (*loomv1.ExecuteResponse, error) {
	if req == nil {
		de.logger.Error("execute request is nil")
		return nil, fmt.Errorf("execute request is nil")
	}

	de.logger.Info("executing command in container",
		zap.String("runtime", req.RuntimeType.String()),
		zap.Strings("command", req.Command),
	)

	startTime := time.Now()

	// Get or create container
	containerID, wasCreated, err := de.scheduler.GetOrCreateContainer(ctx, &loomv1.ContainerRequest{
		RuntimeType: req.RuntimeType,
		Config:      req.Config,
	})
	if err != nil {
		de.logger.Error("failed to get or create container", zap.Error(err))
		return nil, fmt.Errorf("failed to get or create container: %w", err)
	}

	de.logger.Debug("container obtained",
		zap.String("container_id", containerID),
		zap.Bool("was_created", wasCreated),
	)

	// If container was newly created, actually create it in Docker
	var actualContainerID string
	if wasCreated {
		var err error
		actualContainerID, err = de.createContainer(ctx, containerID, req.Config)
		if err != nil {
			de.logger.Error("failed to create Docker container",
				zap.String("container_id", containerID),
				zap.Error(err),
			)
			return nil, fmt.Errorf("failed to create Docker container: %w", err)
		}
		de.logger.Info("using actual Docker container ID",
			zap.String("pre_generated_id", containerID),
			zap.String("actual_docker_id", actualContainerID),
		)
		// Update containerID to use actual Docker ID
		containerID = actualContainerID
	}

	// Ensure container is started
	if err := de.ensureContainerStarted(ctx, containerID); err != nil {
		de.logger.Error("failed to start container",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		return nil, fmt.Errorf("failed to start container: %w", err)
	}

	// Execute command
	stdout, stderr, exitCode, err := de.executeCommand(ctx, containerID, req.Command, req.Stdin, req.WorkingDir, req.Env)
	if err != nil {
		de.logger.Error("failed to execute command",
			zap.String("container_id", containerID),
			zap.Strings("command", req.Command),
			zap.Error(err),
		)
		return nil, fmt.Errorf("failed to execute command: %w", err)
	}

	duration := time.Since(startTime)

	de.logger.Info("command executed successfully",
		zap.String("container_id", containerID),
		zap.Int("exit_code", exitCode),
		zap.Int64("duration_ms", duration.Milliseconds()),
	)

	// Check if rotation is needed
	if err := de.checkRotation(ctx, containerID, req.Config); err != nil {
		de.logger.Warn("rotation check failed",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		// Rotation will be handled on next execution
	}

	return &loomv1.ExecuteResponse{
		ContainerId:      containerID,
		ExitCode:         int32(exitCode),
		Stdout:           stdout,
		Stderr:           stderr,
		DurationMs:       duration.Milliseconds(),
		ContainerCreated: wasCreated,
	}, nil
}

// createContainer creates a Docker container with runtime-specific configuration.
// Returns the actual Docker container ID.
func (de *DockerExecutor) createContainer(ctx context.Context, containerID string, config *loomv1.DockerBackendConfig) (string, error) {
	if config == nil {
		de.logger.Error("docker backend config is nil")
		return "", fmt.Errorf("docker backend config is nil")
	}

	de.logger.Info("creating container",
		zap.String("container_id", containerID),
		zap.String("runtime", config.RuntimeType.String()),
	)

	// Get runtime strategy
	rt, ok := de.runtimes[config.RuntimeType]
	if !ok {
		de.logger.Error("unsupported runtime type", zap.String("runtime", config.RuntimeType.String()))
		return "", fmt.Errorf("unsupported runtime type: %v", config.RuntimeType)
	}

	// Prepare image (pull if necessary)
	de.logger.Debug("preparing image", zap.String("container_id", containerID))
	_, err := rt.PrepareImage(ctx, config)
	if err != nil {
		de.logger.Error("failed to prepare image",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		return "", fmt.Errorf("failed to prepare image: %w", err)
	}

	// Build container config
	containerConfig, err := rt.BuildContainerConfig(ctx, config)
	if err != nil {
		de.logger.Error("failed to build container config",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		return "", fmt.Errorf("failed to build container config: %w", err)
	}

	// Build host config
	hostConfig, err := rt.BuildHostConfig(ctx, config)
	if err != nil {
		de.logger.Error("failed to build host config",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		return "", fmt.Errorf("failed to build host config: %w", err)
	}

	// Create container
	de.logger.Debug("creating Docker container", zap.String("container_id", containerID))
	resp, err := de.dockerClient.ContainerCreate(
		ctx,
		containerConfig,
		hostConfig,
		nil,         // NetworkingConfig
		nil,         // Platform
		containerID, // Container name
	)
	if err != nil {
		de.logger.Error("failed to create container",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	de.logger.Info("container created successfully",
		zap.String("container_id", containerID),
		zap.String("docker_id", resp.ID),
	)

	// Update scheduler with actual Docker container ID
	// Note: For LocalScheduler, containerID is pre-generated and matches
	// For future DistributedScheduler, this updates the mapping

	// Start container
	de.logger.Debug("starting container", zap.String("container_id", resp.ID))
	if err := de.dockerClient.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		de.logger.Error("failed to start container",
			zap.String("container_id", resp.ID),
			zap.Error(err),
		)
		return "", fmt.Errorf("failed to start container: %w", err)
	}

	de.logger.Info("container started", zap.String("container_id", resp.ID))

	// Install packages (if required)
	installCommands, err := rt.InstallPackages(ctx, config)
	if err != nil {
		de.logger.Error("failed to get install commands",
			zap.String("container_id", resp.ID),
			zap.Error(err),
		)
		return "", fmt.Errorf("failed to get install commands: %w", err)
	}

	if len(installCommands) > 0 {
		de.logger.Info("installing packages",
			zap.String("container_id", resp.ID),
			zap.Int("num_commands", len(installCommands)),
		)

		for i, installCmd := range installCommands {
			de.logger.Debug("running install command",
				zap.String("container_id", resp.ID),
				zap.Int("command_index", i),
				zap.Strings("command", installCmd),
			)
			_, _, _, err := de.executeCommand(ctx, resp.ID, installCmd, nil, "", nil)
			if err != nil {
				de.logger.Error("failed to install packages",
					zap.String("container_id", resp.ID),
					zap.Strings("command", installCmd),
					zap.Error(err),
				)
				return "", fmt.Errorf("failed to install packages: %w", err)
			}
		}

		de.logger.Info("packages installed successfully", zap.String("container_id", resp.ID))
	}

	// Return actual Docker container ID
	return resp.ID, nil
}

// ensureContainerStarted ensures a container is running.
func (de *DockerExecutor) ensureContainerStarted(ctx context.Context, containerID string) error {
	de.logger.Debug("ensuring container is started", zap.String("container_id", containerID))

	// Inspect container to get current state
	inspect, err := de.dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		de.logger.Error("failed to inspect container",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		return fmt.Errorf("failed to inspect container: %w", err)
	}

	// If not running, start it
	if !inspect.State.Running {
		de.logger.Info("container not running, starting it", zap.String("container_id", containerID))
		if err := de.dockerClient.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
			de.logger.Error("failed to start container",
				zap.String("container_id", containerID),
				zap.Error(err),
			)
			return fmt.Errorf("failed to start container: %w", err)
		}
		de.logger.Info("container started successfully", zap.String("container_id", containerID))
	} else {
		de.logger.Debug("container already running", zap.String("container_id", containerID))
	}

	return nil
}

// executeCommand executes a command inside a running container.
func (de *DockerExecutor) executeCommand(ctx context.Context, containerID string, command []string, stdin []byte, workingDir string, env map[string]string) ([]byte, []byte, int, error) {
	if len(command) == 0 {
		de.logger.Error("empty command provided")
		return nil, nil, 0, fmt.Errorf("command is empty")
	}

	de.logger.Debug("executing command in container",
		zap.String("container_id", containerID),
		zap.Strings("command", command),
		zap.String("working_dir", workingDir),
	)

	// Start trace span for this execution (if tracing enabled)
	var span *observability.Span
	if de.tracer != nil {
		var spanCtx context.Context
		spanCtx, span = de.tracer.StartSpan(ctx, "docker.execute",
			observability.WithAttribute("container_id", containerID),
			observability.WithAttribute("command", strings.Join(command, " ")),
			observability.WithAttribute("working_dir", workingDir),
		)
		ctx = spanCtx // Use span-aware context
		defer func() {
			if span != nil {
				de.tracer.EndSpan(span)
			}
		}()
	}

	// Build environment variables (copy input env)
	if env == nil {
		env = make(map[string]string)
	}

	// Add /tmp to module search paths for trace libraries
	// (trace libraries are written to /tmp since /usr/local/lib may be read-only)
	env["PYTHONPATH"] = "/tmp"
	env["NODE_PATH"] = "/tmp"

	// Inject trace context into container environment (if tracing enabled)
	if span != nil {
		env["LOOM_TRACE_ID"] = span.TraceID
		env["LOOM_SPAN_ID"] = span.SpanID

		// Build W3C baggage string from span attributes
		// Format: key1=val1,key2=val2
		var baggagePairs []string
		if tenantID, ok := span.Attributes[BaggageKeyTenantID].(string); ok && tenantID != "" {
			baggagePairs = append(baggagePairs, fmt.Sprintf("%s=%s", BaggageKeyTenantID, tenantID))
		}
		if orgID, ok := span.Attributes[BaggageKeyOrgID].(string); ok && orgID != "" {
			baggagePairs = append(baggagePairs, fmt.Sprintf("%s=%s", BaggageKeyOrgID, orgID))
		}
		if len(baggagePairs) > 0 {
			env["LOOM_TRACE_BAGGAGE"] = strings.Join(baggagePairs, ",")
		}

		de.logger.Debug("injected trace context into container",
			zap.String("trace_id", span.TraceID),
			zap.String("span_id", span.SpanID),
			zap.String("baggage", env["LOOM_TRACE_BAGGAGE"]),
		)
	}

	// Convert env map to []string format
	var envVars []string
	for key, value := range env {
		envVars = append(envVars, fmt.Sprintf("%s=%s", key, value))
	}

	// Create exec config
	execConfig := container.ExecOptions{
		Cmd:          command,
		Env:          envVars,
		WorkingDir:   workingDir,
		AttachStdin:  len(stdin) > 0,
		AttachStdout: true,
		AttachStderr: true,
	}

	// Create exec instance
	execID, err := de.dockerClient.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		de.logger.Error("failed to create exec",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		return nil, nil, 0, fmt.Errorf("failed to create exec: %w", err)
	}

	de.logger.Debug("exec instance created", zap.String("exec_id", execID.ID))

	// Attach to exec instance
	attachResp, err := de.dockerClient.ContainerExecAttach(ctx, execID.ID, container.ExecAttachOptions{})
	if err != nil {
		de.logger.Error("failed to attach to exec",
			zap.String("exec_id", execID.ID),
			zap.Error(err),
		)
		return nil, nil, 0, fmt.Errorf("failed to attach to exec: %w", err)
	}
	defer attachResp.Close()

	// Write stdin if provided
	if len(stdin) > 0 {
		de.logger.Debug("writing stdin", zap.Int("bytes", len(stdin)))
		if _, err := attachResp.Conn.Write(stdin); err != nil {
			de.logger.Error("failed to write stdin", zap.Error(err))
			return nil, nil, 0, fmt.Errorf("failed to write stdin: %w", err)
		}
		if err := attachResp.CloseWrite(); err != nil {
			de.logger.Warn("failed to close stdin", zap.Error(err))
		}
	}

	// Read stdout and stderr
	var stdoutBuf, stderrBuf strings.Builder

	// If tracing is enabled, intercept stderr to collect traces
	var stderrWriter io.Writer = &stderrBuf
	var filteringWriter *FilteringWriter
	if de.traceCollector != nil {
		// Create a pipe to collect stderr for trace parsing
		stderrReader, stderrPipeWriter := io.Pipe()

		// Start goroutine to collect traces from stderr
		traceDone := make(chan error, 1)
		go func() {
			traceDone <- de.traceCollector.CollectFromReader(ctx, stderrReader, containerID)
		}()
		defer func() {
			// Flush any remaining buffered data in filtering writer
			if filteringWriter != nil {
				if err := filteringWriter.Flush(); err != nil {
					de.logger.Warn("failed to flush filtering writer", zap.Error(err))
				}
			}
			stderrPipeWriter.Close()
			// Wait for trace collection to complete and log errors
			if err := <-traceDone; err != nil && err != io.EOF {
				de.logger.Warn("trace collection ended with error",
					zap.String("container_id", containerID),
					zap.Error(err))
			}
		}()

		// Use FilteringWriter to remove trace lines from stderr
		filteringWriter = NewFilteringWriter(&stderrBuf)

		// Use MultiWriter to send stderr to both trace collector (raw) and filtering writer
		stderrWriter = io.MultiWriter(filteringWriter, stderrPipeWriter)
	}

	if _, err := stdcopy.StdCopy(&stdoutBuf, stderrWriter, attachResp.Reader); err != nil && err != io.EOF {
		de.logger.Error("failed to read output", zap.Error(err))
		return nil, nil, 0, fmt.Errorf("failed to read output: %w", err)
	}

	// Get exit code
	inspectResp, err := de.dockerClient.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		de.logger.Error("failed to inspect exec", zap.Error(err))
		return nil, nil, 0, fmt.Errorf("failed to inspect exec: %w", err)
	}

	de.logger.Debug("command execution completed",
		zap.String("container_id", containerID),
		zap.Int("exit_code", inspectResp.ExitCode),
	)

	return []byte(stdoutBuf.String()), []byte(stderrBuf.String()), inspectResp.ExitCode, nil
}

// checkRotation checks if a container needs rotation and removes it if necessary.
func (de *DockerExecutor) checkRotation(ctx context.Context, containerID string, config *loomv1.DockerBackendConfig) error {
	if config == nil || config.Lifecycle == nil {
		de.logger.Debug("no rotation config, skipping rotation check", zap.String("container_id", containerID))
		return nil // No rotation config
	}

	de.logger.Debug("checking if container needs rotation", zap.String("container_id", containerID))

	// Get container metadata from scheduler
	containers, err := de.scheduler.ListContainers(ctx, map[string]string{})
	if err != nil {
		de.logger.Error("failed to list containers for rotation check",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		return fmt.Errorf("failed to list containers: %w", err)
	}

	var targetContainer *loomv1.Container
	for _, c := range containers {
		if c.Id == containerID {
			targetContainer = c
			break
		}
	}

	if targetContainer == nil {
		de.logger.Error("container not found in scheduler",
			zap.String("container_id", containerID),
		)
		return fmt.Errorf("container not found in scheduler: %s", containerID)
	}

	// Check execution-based rotation
	maxExecutions := config.Lifecycle.MaxExecutions
	if maxExecutions == 0 {
		maxExecutions = 1000 // Default
	}
	if targetContainer.ExecutionCount >= maxExecutions {
		de.logger.Info("container needs rotation (max executions reached)",
			zap.String("container_id", containerID),
			zap.Int32("executions", targetContainer.ExecutionCount),
			zap.Int32("max_executions", maxExecutions),
		)
		return de.rotateContainer(ctx, containerID)
	}

	// Check time-based rotation
	rotationInterval := time.Duration(config.Lifecycle.RotationIntervalHours) * time.Hour
	if rotationInterval == 0 {
		rotationInterval = 4 * time.Hour // Default
	}
	if targetContainer.CreatedAt != nil {
		createdAt := targetContainer.CreatedAt.AsTime()
		age := time.Since(createdAt)
		if age >= rotationInterval {
			de.logger.Info("container needs rotation (time limit reached)",
				zap.String("container_id", containerID),
				zap.Duration("age", age),
				zap.Duration("rotation_interval", rotationInterval),
			)
			return de.rotateContainer(ctx, containerID)
		}
	}

	de.logger.Debug("container does not need rotation", zap.String("container_id", containerID))
	return nil
}

// rotateContainer removes a container to trigger creation of a fresh one.
func (de *DockerExecutor) rotateContainer(ctx context.Context, containerID string) error {
	de.logger.Info("rotating container", zap.String("container_id", containerID))

	// Stop container
	timeout := 10 // 10 seconds
	de.logger.Debug("stopping container", zap.String("container_id", containerID), zap.Int("timeout_seconds", timeout))
	if err := de.dockerClient.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout}); err != nil {
		de.logger.Warn("failed to stop container during rotation",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		// Continue with removal anyway
	}

	// Remove container
	de.logger.Debug("removing container", zap.String("container_id", containerID))
	if err := de.dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force: true,
	}); err != nil {
		de.logger.Error("failed to remove container",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		return fmt.Errorf("failed to remove container: %w", err)
	}

	// Remove from scheduler
	de.logger.Debug("removing container from scheduler", zap.String("container_id", containerID))
	if err := de.scheduler.RemoveContainer(ctx, containerID); err != nil {
		de.logger.Error("failed to remove from scheduler",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		return fmt.Errorf("failed to remove from scheduler: %w", err)
	}

	de.logger.Info("container rotated successfully", zap.String("container_id", containerID))
	return nil
}

// Health checks the health of the Docker executor and its dependencies.
func (de *DockerExecutor) Health(ctx context.Context) error {
	de.logger.Debug("checking executor health")

	// Check Docker daemon connectivity
	if de.dockerClient == nil {
		de.logger.Error("Docker client not initialized")
		return fmt.Errorf("Docker client not initialized")
	}

	de.logger.Debug("pinging Docker daemon")
	_, err := de.dockerClient.Ping(ctx)
	if err != nil {
		de.logger.Error("Docker daemon ping failed", zap.Error(err))
		return fmt.Errorf("Docker daemon not reachable: %w", err)
	}
	de.logger.Debug("Docker daemon ping successful")

	// Check scheduler accessibility
	if de.scheduler == nil {
		de.logger.Error("scheduler not initialized")
		return fmt.Errorf("scheduler not initialized")
	}

	de.logger.Debug("checking scheduler health")
	_, err = de.scheduler.GetNodeInfo(ctx, "localhost")
	if err != nil {
		de.logger.Error("scheduler health check failed", zap.Error(err))
		return fmt.Errorf("scheduler not accessible: %w", err)
	}
	de.logger.Debug("scheduler health check successful")

	de.logger.Debug("executor health check passed")
	return nil
}

// GetContainerLogs retrieves logs from a container.
func (de *DockerExecutor) GetContainerLogs(ctx context.Context, containerID string, tail int, timestamps bool) (string, error) {
	if containerID == "" {
		de.logger.Error("empty container ID provided")
		return "", fmt.Errorf("container ID is empty")
	}

	de.logger.Debug("getting container logs",
		zap.String("container_id", containerID),
		zap.Int("tail", tail),
		zap.Bool("timestamps", timestamps),
	)

	// Build log options
	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: timestamps,
		Follow:     false,
	}

	if tail > 0 {
		tailStr := fmt.Sprintf("%d", tail)
		options.Tail = tailStr
	}

	// Get logs
	reader, err := de.dockerClient.ContainerLogs(ctx, containerID, options)
	if err != nil {
		de.logger.Error("failed to get container logs",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		return "", fmt.Errorf("failed to get container logs: %w", err)
	}
	defer reader.Close()

	// Read logs into string
	var logBuf strings.Builder
	if _, err := stdcopy.StdCopy(&logBuf, &logBuf, reader); err != nil && err != io.EOF {
		de.logger.Error("failed to read container logs",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
		return "", fmt.Errorf("failed to read container logs: %w", err)
	}

	logs := logBuf.String()
	de.logger.Debug("container logs retrieved",
		zap.String("container_id", containerID),
		zap.Int("log_size", len(logs)),
	)

	return logs, nil
}

// Close releases executor resources.
func (de *DockerExecutor) Close() error {
	de.logger.Info("closing docker executor")

	if de.dockerClient != nil {
		if err := de.dockerClient.Close(); err != nil {
			de.logger.Error("failed to close Docker client", zap.Error(err))
			return err
		}
	}

	de.logger.Info("docker executor closed successfully")
	return nil
}
