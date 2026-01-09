// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package runtime

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Runtime defines how to configure and manage containers for a specific runtime type.
// Each runtime (Python, Node, Custom) has different requirements for:
//   - Base images
//   - Package management (pip, npm, etc.)
//   - Environment configuration
//   - Volume mounts for caching
//
// This interface enables pluggable runtime strategies without changing the executor.
type Runtime interface {
	// Type returns the runtime type (PYTHON, NODE, CUSTOM, etc.)
	Type() loomv1.RuntimeType

	// BuildContainerConfig creates Docker container configuration.
	// Includes:
	//   - Image selection (base image or custom Dockerfile)
	//   - Environment variables
	//   - Working directory
	//   - Entrypoint/command
	//   - Volume mounts for package caching
	BuildContainerConfig(ctx context.Context, config *loomv1.DockerBackendConfig) (*container.Config, error)

	// BuildHostConfig creates Docker host configuration (resource limits, mounts).
	// Includes:
	//   - CPU/memory limits
	//   - Volume mounts (cache volumes, user volumes)
	//   - Security options (read-only rootfs, capability drops)
	BuildHostConfig(ctx context.Context, config *loomv1.DockerBackendConfig) (*container.HostConfig, error)

	// PrepareImage ensures the required image is available.
	// For base images: Pull if not present
	// For Dockerfiles: Build custom image
	// For ImageBuildConfig: Generate and build Dockerfile
	PrepareImage(ctx context.Context, config *loomv1.DockerBackendConfig) (string, error)

	// InstallPackages installs runtime-specific packages inside container.
	// For Python: pip install -r requirements.txt or pip install <packages>
	// For Node: npm install or npm install <packages>
	// For Custom: Runs custom_runtime_config.entrypoint_cmd
	//
	// Returns commands to execute inside container.
	InstallPackages(ctx context.Context, config *loomv1.DockerBackendConfig) ([][]string, error)

	// GetCacheMounts returns volume mounts for package caching.
	// For Python: /root/.cache/pip
	// For Node: /root/.npm
	// Enables fast package reinstallation across container rotations.
	GetCacheMounts(ctx context.Context) []mount.Mount
}

// BaseRuntime provides common functionality for all runtimes.
// Individual runtime implementations can embed this struct and override methods.
type BaseRuntime struct {
	runtimeType loomv1.RuntimeType
}

// Type implements Runtime.Type.
func (br *BaseRuntime) Type() loomv1.RuntimeType {
	return br.runtimeType
}

// ApplyResourceLimits applies CPU/memory limits to HostConfig.
func ApplyResourceLimits(hostConfig *container.HostConfig, limits *loomv1.ResourceLimits) {
	if limits == nil {
		return
	}

	// CPU limits
	if limits.CpuCores > 0 {
		// Docker uses NanoCPUs (1 CPU = 1e9 NanoCPUs)
		hostConfig.NanoCPUs = int64(limits.CpuCores * 1e9)
	}

	// Memory limits
	if limits.MemoryMb > 0 {
		hostConfig.Memory = limits.MemoryMb * 1024 * 1024 // MB to bytes
	}

	// PID limits
	if limits.PidsLimit > 0 {
		pidsLimit := int64(limits.PidsLimit)
		hostConfig.PidsLimit = &pidsLimit
	}

	// Storage limits (future: use --storage-opt)
	// Note: Requires devicemapper or overlay2 with quota support
}

// ApplyVolumeMounts applies user-defined volume mounts to HostConfig.
func ApplyVolumeMounts(hostConfig *container.HostConfig, volumeMounts []*loomv1.VolumeMount) {
	for _, vm := range volumeMounts {
		mount := mount.Mount{
			Type:     mount.TypeBind,
			Source:   vm.Source,
			Target:   vm.Target,
			ReadOnly: vm.ReadOnly,
		}
		hostConfig.Mounts = append(hostConfig.Mounts, mount)
	}
}

// ApplyEnvironment applies environment variables to ContainerConfig.
func ApplyEnvironment(containerConfig *container.Config, environment map[string]string) {
	if containerConfig.Env == nil {
		containerConfig.Env = []string{}
	}

	for key, value := range environment {
		containerConfig.Env = append(containerConfig.Env, key+"="+value)
	}
}

// ApplySecurityOptions applies security settings to HostConfig.
// - Read-only rootfs (except /tmp)
// - Capability dropping (all caps except NET_BIND_SERVICE)
// - No privileged mode
func ApplySecurityOptions(hostConfig *container.HostConfig) {
	// Read-only rootfs (write to /tmp via tmpfs)
	hostConfig.ReadonlyRootfs = true
	hostConfig.Tmpfs = map[string]string{
		"/tmp": "rw,size=1g,mode=1777", // 1GB writable /tmp
	}

	// Drop all capabilities except NET_BIND_SERVICE
	hostConfig.CapDrop = []string{"ALL"}
	hostConfig.CapAdd = []string{"NET_BIND_SERVICE"}

	// No privileged mode
	hostConfig.Privileged = false

	// No new privileges (prevent privilege escalation)
	hostConfig.SecurityOpt = []string{"no-new-privileges"}
}

// ApplyNonRootUser configures container to run as non-root user.
// - Sets User to UID:GID format (default: 1000:1000)
// - Creates /workspace and /tmp with proper ownership
// - Prevents privilege escalation
//
// Security Benefits:
//   - Limits damage from container escape
//   - Prevents unauthorized file access on host
//   - Compliance with security best practices
func ApplyNonRootUser(containerConfig *container.Config, uid, gid int) {
	if uid == 0 || gid == 0 {
		// Don't set user if UID/GID is 0 (root) - allow root mode for debugging
		return
	}

	// Set user as UID:GID
	// Note: Using numeric ID avoids dependency on /etc/passwd in container
	containerConfig.User = fmt.Sprintf("%d:%d", uid, gid)
}
