// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package docker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestLocalScheduler_Schedule(t *testing.T) {
	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		DockerHost: "", // Auto-detect (OrbStack, Docker Desktop, or standard socket)
		NodeID:     "test-node",
		Logger:     logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	req := &loomv1.ScheduleRequest{
		Resources: &loomv1.ResourceRequest{
			CpuCores: 2.0,
			MemoryMb: 2048,
		},
	}

	decision, err := scheduler.Schedule(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "test-node", decision.NodeId)
	assert.Equal(t, "local_scheduler_single_node", decision.Reason)
	assert.Equal(t, 0.0, decision.EstimatedCostUsd)
}

func TestLocalScheduler_GetOrCreateContainer(t *testing.T) {
	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		DockerHost: "", // Auto-detect (OrbStack, Docker Desktop, or standard socket)
		NodeID:     "test-node",
		Logger:     logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	req := &loomv1.ContainerRequest{
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		Config: &loomv1.DockerBackendConfig{
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		},
	}

	// First call should create new container
	containerID1, wasCreated1, err := scheduler.GetOrCreateContainer(ctx, req)
	require.NoError(t, err)
	assert.True(t, wasCreated1)
	assert.NotEmpty(t, containerID1)

	// Mark as running (simulating executor)
	scheduler.mu.Lock()
	scheduler.containerPool[containerID1].Status = loomv1.ContainerStatus_CONTAINER_STATUS_RUNNING
	scheduler.mu.Unlock()

	// Second call with same runtime should reuse container
	containerID2, wasCreated2, err := scheduler.GetOrCreateContainer(ctx, req)
	require.NoError(t, err)
	assert.False(t, wasCreated2)
	assert.Equal(t, containerID1, containerID2)
}

func TestLocalScheduler_ListContainers(t *testing.T) {
	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		DockerHost: "", // Auto-detect (OrbStack, Docker Desktop, or standard socket)
		NodeID:     "test-node",
		Logger:     logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create some containers
	req1 := &loomv1.ContainerRequest{
		TenantId:    "tenant-1",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		Config: &loomv1.DockerBackendConfig{
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		},
	}
	_, _, err = scheduler.GetOrCreateContainer(ctx, req1)
	require.NoError(t, err)

	req2 := &loomv1.ContainerRequest{
		TenantId:    "tenant-2",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_NODE,
		Config: &loomv1.DockerBackendConfig{
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_NODE,
		},
	}
	_, _, err = scheduler.GetOrCreateContainer(ctx, req2)
	require.NoError(t, err)

	// List all containers
	containers, err := scheduler.ListContainers(ctx, map[string]string{})
	require.NoError(t, err)
	assert.Len(t, containers, 2)

	// Filter by tenant
	containers, err = scheduler.ListContainers(ctx, map[string]string{"tenant_id": "tenant-1"})
	require.NoError(t, err)
	assert.Len(t, containers, 1)
	assert.Equal(t, "tenant-1", containers[0].TenantId)
}

func TestLocalScheduler_RemoveContainer(t *testing.T) {
	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		DockerHost: "", // Auto-detect (OrbStack, Docker Desktop, or standard socket)
		NodeID:     "test-node",
		Logger:     logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create container
	req := &loomv1.ContainerRequest{
		TenantId:    "tenant-1",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		Config: &loomv1.DockerBackendConfig{
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		},
	}
	containerID, _, err := scheduler.GetOrCreateContainer(ctx, req)
	require.NoError(t, err)

	// Remove container
	err = scheduler.RemoveContainer(ctx, containerID)
	require.NoError(t, err)

	// Verify it's removed
	containers, err := scheduler.ListContainers(ctx, map[string]string{})
	require.NoError(t, err)
	assert.Len(t, containers, 0)
}

func TestLocalScheduler_runCleanup_StuckCreating(t *testing.T) {
	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		DockerHost: "", // Auto-detect (OrbStack, Docker Desktop, or standard socket)
		NodeID:     "test-node",
		Logger:     logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create container stuck in CREATING state for >5 minutes
	oldTime := time.Now().Add(-6 * time.Minute)
	containerID := "test-container-stuck"
	scheduler.mu.Lock()
	scheduler.containerPool[containerID] = &loomv1.Container{
		Id:        containerID,
		Status:    loomv1.ContainerStatus_CONTAINER_STATUS_CREATING,
		CreatedAt: timestamppb.New(oldTime),
	}
	scheduler.mu.Unlock()

	// Run cleanup
	scheduler.runCleanup()

	// Verify container marked as FAILED
	scheduler.mu.RLock()
	container := scheduler.containerPool[containerID]
	scheduler.mu.RUnlock()
	assert.Equal(t, loomv1.ContainerStatus_CONTAINER_STATUS_FAILED, container.Status)
}

func TestLocalScheduler_runCleanup_FailedContainerRemoval(t *testing.T) {
	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		DockerHost: "", // Auto-detect (OrbStack, Docker Desktop, or standard socket)
		NodeID:     "test-node",
		Logger:     logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create failed container that's been failed for >10 minutes
	oldTime := time.Now().Add(-11 * time.Minute)
	containerID := "test-container-failed"
	scheduler.mu.Lock()
	scheduler.containerPool[containerID] = &loomv1.Container{
		Id:        containerID,
		Status:    loomv1.ContainerStatus_CONTAINER_STATUS_FAILED,
		CreatedAt: timestamppb.New(oldTime),
	}
	scheduler.mu.Unlock()

	// Run cleanup
	scheduler.runCleanup()

	// Verify container removed
	scheduler.mu.RLock()
	_, exists := scheduler.containerPool[containerID]
	scheduler.mu.RUnlock()
	assert.False(t, exists)
}

func TestLocalScheduler_runCleanup_TimeBasedRotation(t *testing.T) {
	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		DockerHost: "", // Auto-detect (OrbStack, Docker Desktop, or standard socket)
		NodeID:     "test-node",
		Logger:     logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create running container that's been running for >4 hours
	oldTime := time.Now().Add(-5 * time.Hour)
	containerID := "test-container-old"
	scheduler.mu.Lock()
	scheduler.containerPool[containerID] = &loomv1.Container{
		Id:        containerID,
		Status:    loomv1.ContainerStatus_CONTAINER_STATUS_RUNNING,
		CreatedAt: timestamppb.New(oldTime),
	}
	scheduler.mu.Unlock()

	// Run cleanup
	scheduler.runCleanup()

	// Verify container removed (needs rotation)
	scheduler.mu.RLock()
	_, exists := scheduler.containerPool[containerID]
	scheduler.mu.RUnlock()
	assert.False(t, exists)
}

func TestLocalScheduler_GetNodeInfo(t *testing.T) {
	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		DockerHost: "", // Auto-detect (OrbStack, Docker Desktop, or standard socket)
		NodeID:     "test-node",
		Logger:     logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	nodeInfo, err := scheduler.GetNodeInfo(ctx, "test-node")
	require.NoError(t, err)
	assert.Equal(t, "test-node", nodeInfo.NodeId)
	assert.Equal(t, loomv1.NodeStatus_NODE_STATUS_HEALTHY, nodeInfo.Status)
	assert.NotNil(t, nodeInfo.Capacity)
	assert.NotNil(t, nodeInfo.Available)

	// Test with wrong node ID
	_, err = scheduler.GetNodeInfo(ctx, "wrong-node")
	assert.Error(t, err)
}
