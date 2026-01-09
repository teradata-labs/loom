// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package docker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/docker/docker/client"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ContainerScheduler decides where to schedule containers.
// This interface enables future distributed scheduling across multiple Docker daemons
// without refactoring the Docker backend.
//
// For v1.0: LocalScheduler schedules all containers to "localhost" (single daemon)
// For future: DistributedScheduler can schedule across nodes based on:
//   - Resource availability (CPU, memory)
//   - Data locality (Teradata node affinity)
//   - Tenant isolation (dedicated nodes per tenant)
//   - Cost optimization (spot instances, preemptible VMs)
type ContainerScheduler interface {
	// Schedule decides where to run a container based on requirements.
	// Returns scheduling decision with node_id, reason, and cost estimate.
	//
	// For LocalScheduler: Always returns node_id="localhost"
	// For DistributedScheduler: Evaluates multiple nodes and selects best
	Schedule(ctx context.Context, req *loomv1.ScheduleRequest) (*loomv1.ScheduleDecision, error)

	// GetOrCreateContainer retrieves an existing container from the pool or creates a new one.
	// Handles container lifecycle: creation, health checks, rotation.
	//
	// Returns container ID and whether it was newly created.
	GetOrCreateContainer(ctx context.Context, req *loomv1.ContainerRequest) (string, bool, error)

	// ListContainers returns all containers managed by this scheduler.
	// Supports filtering by tenant, runtime type, labels, status.
	ListContainers(ctx context.Context, filters map[string]string) ([]*loomv1.Container, error)

	// RemoveContainer removes a container from the pool and deletes it.
	// Used for explicit cleanup, rotation, or when container becomes unhealthy.
	RemoveContainer(ctx context.Context, containerID string) error

	// GetNodeInfo retrieves information about a specific node.
	// For LocalScheduler: Returns localhost capacity/availability
	// For DistributedScheduler: Queries node metrics
	GetNodeInfo(ctx context.Context, nodeID string) (*loomv1.NodeInfo, error)

	// Close releases scheduler resources (connection pools, goroutines, etc.)
	Close() error
}

// LocalScheduler schedules containers to a single local Docker daemon.
// All containers run on "localhost" (node_id="localhost").
//
// Thread Safety: All methods are thread-safe (can be called from multiple goroutines).
//
// Container Lifecycle:
//  1. GetOrCreateContainer() -> Container created or retrieved from pool
//  2. Container used for 0-1000 executions over 0-4 hours
//  3. Rotation triggered by max_executions OR rotation_interval_hours
//  4. Old container removed, new container created for next execution
//
// Multi-Tenancy (Future):
//   - Tenant-scoped container pools (tenant_id -> []containerID)
//   - Resource accounting per tenant (CPU seconds, memory GB-hours)
//   - Quota enforcement (reject if tenant exceeds quota)
//
// Currently: All containers share a global pool (no tenant isolation)
type LocalScheduler struct {
	// dockerClient is the Docker API client
	dockerClient *client.Client

	// mu protects containerPool and resourceAccounting
	mu sync.RWMutex

	// containerPool maps containerID -> Container metadata
	// Used for tracking container lifecycle, executions, health status
	containerPool map[string]*loomv1.Container

	// tenantContainers maps tenant_id -> []containerID (for future multi-tenancy)
	// Currently unused (all containers in global pool)
	tenantContainers map[string][]string

	// resourceAccounting tracks per-tenant resource usage (for future quota enforcement)
	// Maps tenant_id -> ResourceUsage
	// Currently unused (no quota enforcement)
	resourceAccounting map[string]*loomv1.ResourceUsage

	// nodeID is the identifier for this Docker daemon
	// For LocalScheduler: always "localhost"
	nodeID string

	// capacity is the total resource capacity of this node
	// For future: Query from Docker daemon (inspect system)
	capacity *loomv1.ResourceCapacity

	// logger for structured logging
	logger *zap.Logger

	// backgroundCleanup runs periodic health checks and rotation
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// LocalSchedulerConfig configures LocalScheduler.
type LocalSchedulerConfig struct {
	// DockerHost is the Docker daemon endpoint (default: "unix:///var/run/docker.sock")
	DockerHost string

	// SocketPaths is a list of Docker socket paths to try in order (default: platform-specific)
	// This allows configuration via LOOM_DOCKER_SOCKET_PATHS environment variable (comma-separated)
	SocketPaths []string

	// NodeID is the identifier for this node (default: "localhost")
	NodeID string

	// CleanupInterval is how often to run health checks and rotation (default: 5 minutes)
	CleanupInterval time.Duration

	// DefaultRotationInterval is the default rotation interval if not specified in config (default: 4 hours)
	DefaultRotationInterval time.Duration

	// DefaultMaxExecutions is the default max executions before rotation (default: 1000)
	DefaultMaxExecutions int32

	// Logger is the zap logger (optional, created if nil)
	Logger *zap.Logger
}

// DefaultDockerSocketPaths returns the default Docker socket paths for the current platform.
// Can be overridden via LOOM_DOCKER_SOCKET_PATHS environment variable (comma-separated).
// Default: OrbStack on macOS (recommended for performance and resource efficiency).
func DefaultDockerSocketPaths() []string {
	// Check for custom socket paths via environment variable
	if paths := os.Getenv("LOOM_DOCKER_SOCKET_PATHS"); paths != "" {
		return splitPaths(paths)
	}

	// Use $HOME for user directory (works on macOS and Linux)
	home := os.Getenv("HOME")
	if home == "" {
		// Fallback to /Users/$USER on macOS
		if user := os.Getenv("USER"); user != "" {
			home = "/Users/" + user
		}
	}

	// OrbStack-first for macOS (recommended: lightweight, fast, native ARM support)
	// Fallback to standard Linux socket location
	return []string{
		home + "/.orbstack/run/docker.sock", // OrbStack (macOS) - preferred
		"/var/run/docker.sock",              // Standard location (Linux)
	}
}

// splitPaths splits a comma-separated string into a slice of paths.
func splitPaths(paths string) []string {
	result := []string{}
	for _, p := range stringsSplit(paths, ",") {
		trimmed := stringsTrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// stringsSplit is a simple split function to avoid importing strings package.
func stringsSplit(s, sep string) []string {
	var result []string
	for len(s) > 0 {
		i := indexOf(s, sep)
		if i < 0 {
			result = append(result, s)
			break
		}
		result = append(result, s[:i])
		s = s[i+len(sep):]
	}
	return result
}

// indexOf finds the index of substr in s, returns -1 if not found.
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// stringsTrimSpace trims leading and trailing whitespace.
func stringsTrimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// detectDockerHost attempts to detect the Docker daemon socket location.
// It tries in order: DOCKER_HOST env var, configured socket paths, default socket paths.
func detectDockerHost() string {
	return detectDockerHostWithPaths(nil)
}

// detectDockerHostWithPaths attempts to detect the Docker daemon socket location using provided paths.
// If paths is nil or empty, uses default paths.
func detectDockerHostWithPaths(socketPaths []string) string {
	// Check DOCKER_HOST environment variable first
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return host
	}

	// Use provided paths or fall back to defaults
	paths := socketPaths
	if len(paths) == 0 {
		paths = DefaultDockerSocketPaths()
	}

	// Check socket locations in order
	for _, sock := range paths {
		if _, err := os.Stat(sock); err == nil {
			return "unix://" + sock
		}
	}

	// Fallback to standard location
	return "unix:///var/run/docker.sock"
}

// NewLocalScheduler creates a new LocalScheduler instance.
func NewLocalScheduler(ctx context.Context, config LocalSchedulerConfig) (*LocalScheduler, error) {
	// Set defaults
	if config.NodeID == "" {
		config.NodeID = "localhost"
	}
	if config.CleanupInterval == 0 {
		config.CleanupInterval = 5 * time.Minute
	}
	if config.DefaultRotationInterval == 0 {
		config.DefaultRotationInterval = 4 * time.Hour
	}
	if config.DefaultMaxExecutions == 0 {
		config.DefaultMaxExecutions = 1000
	}

	// Create logger if not provided
	logger := config.Logger
	if logger == nil {
		var err error
		logger, err = zap.NewProduction()
		if err != nil {
			return nil, fmt.Errorf("failed to create logger: %w", err)
		}
	}

	// Detect Docker host if not explicitly provided
	if config.DockerHost == "" {
		config.DockerHost = detectDockerHostWithPaths(config.SocketPaths)
	}

	logger.Info("creating local scheduler",
		zap.String("docker_host", config.DockerHost),
		zap.String("node_id", config.NodeID),
		zap.Duration("cleanup_interval", config.CleanupInterval),
	)

	// Create Docker client
	dockerClient, err := client.NewClientWithOpts(
		client.WithHost(config.DockerHost),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		logger.Error("failed to create Docker client", zap.Error(err))
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	// Verify Docker daemon is reachable
	logger.Debug("pinging Docker daemon")
	if _, err := dockerClient.Ping(ctx); err != nil {
		dockerClient.Close()
		logger.Error("failed to ping Docker daemon", zap.Error(err))
		return nil, fmt.Errorf("failed to ping Docker daemon: %w", err)
	}

	logger.Info("Docker daemon is reachable")

	ls := &LocalScheduler{
		dockerClient:       dockerClient,
		containerPool:      make(map[string]*loomv1.Container),
		tenantContainers:   make(map[string][]string),
		resourceAccounting: make(map[string]*loomv1.ResourceUsage),
		nodeID:             config.NodeID,
		logger:             logger,
		stopCh:             make(chan struct{}),
	}

	// Query node capacity (future: from Docker system info)
	// For now: Hardcode reasonable defaults
	ls.capacity = &loomv1.ResourceCapacity{
		CpuCores:  16.0,  // 16 CPU cores
		MemoryMb:  65536, // 64GB RAM
		StorageGb: 1024,  // 1TB storage
	}

	logger.Info("node capacity configured",
		zap.Float64("cpu_cores", ls.capacity.CpuCores),
		zap.Int64("memory_mb", ls.capacity.MemoryMb),
		zap.Int64("storage_gb", ls.capacity.StorageGb),
	)

	// Start background cleanup goroutine
	ls.wg.Add(1)
	go ls.backgroundCleanup(config.CleanupInterval)

	return ls, nil
}

// Schedule implements ContainerScheduler.Schedule.
// For LocalScheduler: Always schedules to "localhost" with zero cost.
func (ls *LocalScheduler) Schedule(ctx context.Context, req *loomv1.ScheduleRequest) (*loomv1.ScheduleDecision, error) {
	if req == nil {
		return nil, fmt.Errorf("schedule request is nil")
	}

	// For LocalScheduler: No scheduling decision needed, always return localhost
	// Future DistributedScheduler would evaluate multiple nodes here

	return &loomv1.ScheduleDecision{
		NodeId:           ls.nodeID,
		Reason:           "local_scheduler_single_node",
		EstimatedCostUsd: 0.0,                    // Local execution has no cloud cost
		NodeInfo:         ls.getNodeInfoLocked(), // Current node capacity/availability
	}, nil
}

// GetOrCreateContainer implements ContainerScheduler.GetOrCreateContainer.
func (ls *LocalScheduler) GetOrCreateContainer(ctx context.Context, req *loomv1.ContainerRequest) (string, bool, error) {
	if req == nil {
		return "", false, fmt.Errorf("container request is nil")
	}

	ls.mu.Lock()
	defer ls.mu.Unlock()

	// Check if we have a healthy container that matches requirements
	// For v1.0: Simple matching by runtime_type
	// For future: Match by tenant_id, resource requirements, labels
	containerID := ls.findHealthyContainerLocked(req)
	if containerID != "" {
		// Increment execution count
		if container, ok := ls.containerPool[containerID]; ok {
			container.ExecutionCount++
			// Note: Caller (executor) will check if rotation is needed
		}
		return containerID, false, nil // Reused existing container
	}

	// No healthy container found, create a new one
	// Note: Actual container creation happens in executor.go
	// Scheduler just allocates a slot in the pool and returns placeholder

	// Generate container ID (future: from Docker API after creation)
	containerID = fmt.Sprintf("container-%d", time.Now().UnixNano())

	// Create container metadata
	now := time.Now()
	container := &loomv1.Container{
		Id:             containerID,
		TenantId:       req.TenantId, // For future multi-tenancy
		NodeId:         ls.nodeID,
		RuntimeType:    req.RuntimeType,
		Status:         loomv1.ContainerStatus_CONTAINER_STATUS_CREATING,
		ExecutionCount: 0,
		CreatedAt:      timestamppb.New(now),
		LastUsedAt:     timestamppb.New(now),
		Labels:         req.Labels,
	}

	// Add to pool
	ls.containerPool[containerID] = container

	// Add to tenant pool (for future multi-tenancy)
	if req.TenantId != "" {
		ls.tenantContainers[req.TenantId] = append(ls.tenantContainers[req.TenantId], containerID)
	}

	return containerID, true, nil // New container created
}

// ListContainers implements ContainerScheduler.ListContainers.
func (ls *LocalScheduler) ListContainers(ctx context.Context, filters map[string]string) ([]*loomv1.Container, error) {
	ls.mu.RLock()
	defer ls.mu.RUnlock()

	var containers []*loomv1.Container

	// Apply filters
	tenantID := filters["tenant_id"]
	runtimeType := filters["runtime_type"]
	status := filters["status"]

	for _, container := range ls.containerPool {
		// Filter by tenant
		if tenantID != "" && container.TenantId != tenantID {
			continue
		}

		// Filter by runtime type
		if runtimeType != "" {
			expectedType := loomv1.RuntimeType(loomv1.RuntimeType_value[runtimeType])
			if container.RuntimeType != expectedType {
				continue
			}
		}

		// Filter by status
		if status != "" {
			expectedStatus := loomv1.ContainerStatus(loomv1.ContainerStatus_value[status])
			if container.Status != expectedStatus {
				continue
			}
		}

		containers = append(containers, container)
	}

	return containers, nil
}

// RemoveContainer implements ContainerScheduler.RemoveContainer.
func (ls *LocalScheduler) RemoveContainer(ctx context.Context, containerID string) error {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	container, ok := ls.containerPool[containerID]
	if !ok {
		return fmt.Errorf("container not found: %s", containerID)
	}

	// Remove from tenant pool
	if container.TenantId != "" {
		tenantContainers := ls.tenantContainers[container.TenantId]
		for i, id := range tenantContainers {
			if id == containerID {
				ls.tenantContainers[container.TenantId] = append(tenantContainers[:i], tenantContainers[i+1:]...)
				break
			}
		}
	}

	// Remove from main pool
	delete(ls.containerPool, containerID)

	// Note: Actual Docker container removal happens in executor.go
	// Scheduler just removes from tracking

	return nil
}

// GetNodeInfo implements ContainerScheduler.GetNodeInfo.
func (ls *LocalScheduler) GetNodeInfo(ctx context.Context, nodeID string) (*loomv1.NodeInfo, error) {
	if nodeID != ls.nodeID {
		return nil, fmt.Errorf("node not found: %s (this scheduler manages %s)", nodeID, ls.nodeID)
	}

	ls.mu.RLock()
	defer ls.mu.RUnlock()

	return ls.getNodeInfoLocked(), nil
}

// Close implements ContainerScheduler.Close.
func (ls *LocalScheduler) Close() error {
	// Signal background cleanup to stop
	close(ls.stopCh)

	// Wait for background goroutine to exit
	ls.wg.Wait()

	// Close Docker client
	if ls.dockerClient != nil {
		return ls.dockerClient.Close()
	}

	return nil
}

// findHealthyContainerLocked finds a healthy container that matches requirements.
// Must be called with ls.mu held.
func (ls *LocalScheduler) findHealthyContainerLocked(req *loomv1.ContainerRequest) string {
	for containerID, container := range ls.containerPool {
		// Check status
		if container.Status != loomv1.ContainerStatus_CONTAINER_STATUS_RUNNING {
			continue
		}

		// Check runtime type match
		if container.RuntimeType != req.RuntimeType {
			continue
		}

		// Check tenant match (for future multi-tenancy)
		if req.TenantId != "" && container.TenantId != req.TenantId {
			continue
		}

		// Check if rotation is needed
		// Note: Detailed rotation logic in executor.go
		// Scheduler just checks basic criteria

		// Found a healthy match
		return containerID
	}

	return "" // No healthy container found
}

// getNodeInfoLocked returns current node info.
// Must be called with ls.mu held (at least RLock).
func (ls *LocalScheduler) getNodeInfoLocked() *loomv1.NodeInfo {
	// Count running containers
	runningCount := 0
	for _, container := range ls.containerPool {
		if container.Status == loomv1.ContainerStatus_CONTAINER_STATUS_RUNNING {
			runningCount++
		}
	}

	// Calculate available resources (future: track actual usage)
	// For now: Assume each container uses 1 CPU core and 2GB RAM
	usedCpu := float64(runningCount) * 1.0
	usedMemory := int64(runningCount) * 2048

	return &loomv1.NodeInfo{
		NodeId:           ls.nodeID,
		Region:           "local", // For distributed: "us-west-1", "eu-central-1", etc.
		AvailabilityZone: "local", // For distributed: "us-west-1a", etc.
		Capacity:         ls.capacity,
		Available: &loomv1.ResourceCapacity{
			CpuCores:  ls.capacity.CpuCores - usedCpu,
			MemoryMb:  ls.capacity.MemoryMb - usedMemory,
			StorageGb: ls.capacity.StorageGb, // Storage not tracked yet
		},
		ContainerCount: int32(len(ls.containerPool)),
		Status:         loomv1.NodeStatus_NODE_STATUS_HEALTHY,
	}
}

// backgroundCleanup runs periodic health checks and rotation.
func (ls *LocalScheduler) backgroundCleanup(interval time.Duration) {
	defer ls.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ls.runCleanup()
		case <-ls.stopCh:
			return
		}
	}
}

// runCleanup performs health checks and marks unhealthy containers for removal.
func (ls *LocalScheduler) runCleanup() {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	now := time.Now()
	containersToRemove := []string{}

	ls.logger.Debug("starting cleanup", zap.Int("container_count", len(ls.containerPool)))

	// Check each container's health
	for containerID, container := range ls.containerPool {
		// Check if container is stuck in CREATING state
		if container.Status == loomv1.ContainerStatus_CONTAINER_STATUS_CREATING {
			if container.CreatedAt != nil {
				createdAt := container.CreatedAt.AsTime()
				age := now.Sub(createdAt)

				// Mark as FAILED if stuck in CREATING for > 5 minutes
				if age > 5*time.Minute {
					ls.logger.Warn("container stuck in CREATING state, marking as FAILED",
						zap.String("container_id", containerID),
						zap.Duration("age", age),
					)
					container.Status = loomv1.ContainerStatus_CONTAINER_STATUS_FAILED
				}
			}
		}

		// Check if container is in FAILED state
		if container.Status == loomv1.ContainerStatus_CONTAINER_STATUS_FAILED {
			if container.CreatedAt != nil {
				createdAt := container.CreatedAt.AsTime()
				age := now.Sub(createdAt)

				// Remove failed containers after 10 minute grace period
				if age > 10*time.Minute {
					ls.logger.Info("removing failed container after grace period",
						zap.String("container_id", containerID),
						zap.Duration("age", age),
					)
					containersToRemove = append(containersToRemove, containerID)
				}
			}
		}

		// Check if container needs rotation (time-based)
		// Note: Execution-based rotation handled in executor.go
		// This is a backup check at the scheduler level
		if container.Status == loomv1.ContainerStatus_CONTAINER_STATUS_RUNNING {
			if container.CreatedAt != nil {
				createdAt := container.CreatedAt.AsTime()
				age := now.Sub(createdAt)

				// Default rotation interval: 4 hours
				// Note: Actual rotation config is in DockerBackendConfig.Lifecycle
				// This is a conservative default check
				rotationInterval := 4 * time.Hour

				if age > rotationInterval {
					ls.logger.Info("container needs rotation (time-based)",
						zap.String("container_id", containerID),
						zap.Duration("age", age),
						zap.Duration("rotation_interval", rotationInterval),
					)
					// Mark for removal - executor will create new container on next execution
					containersToRemove = append(containersToRemove, containerID)
				}
			}
		}
	}

	// Remove containers marked for cleanup
	for _, containerID := range containersToRemove {
		container, ok := ls.containerPool[containerID]
		if !ok {
			continue
		}

		// Remove from tenant pool
		if container.TenantId != "" {
			tenantContainers := ls.tenantContainers[container.TenantId]
			for i, id := range tenantContainers {
				if id == containerID {
					ls.tenantContainers[container.TenantId] = append(tenantContainers[:i], tenantContainers[i+1:]...)
					break
				}
			}
		}

		// Remove from main pool
		delete(ls.containerPool, containerID)

		ls.logger.Info("container removed from pool",
			zap.String("container_id", containerID),
		)
	}

	if len(containersToRemove) > 0 {
		ls.logger.Info("cleanup completed",
			zap.Int("removed_count", len(containersToRemove)),
			zap.Int("remaining_count", len(ls.containerPool)),
		)
	}

	// Future: Implement resource usage tracking and quota enforcement
	// For each tenant:
	//   - Sum CPU seconds, memory GB-hours across all their containers
	//   - Check against tenant quota
	//   - Reject new containers if quota exceeded
}
