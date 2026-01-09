// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package runtime

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// NodeRuntime configures Node.js containers with npm package management.
//
// Features:
//   - Multiple Node versions (16 LTS, 18 LTS, 20 LTS, 21)
//   - npm caching via Docker volume (/root/.npm)
//   - package.json support
//   - Preinstalled packages (express, axios, etc.)
//
// Base Images (official Node):
//   - node:20-slim (default, ~60MB compressed, 180MB uncompressed)
//   - node:18-slim (LTS)
//   - node:16-slim (older LTS)
//
// Package Installation:
//  1. Preinstalled packages: npm install <pkg1> <pkg2> ...
//  2. package.json: npm install
//  3. npm cache persisted to volume for fast reinstalls
//
// Security:
//   - Read-only rootfs (except /tmp and /root/.npm)
//   - Non-root user (future: create 'loom' user)
//   - Capability dropping
type NodeRuntime struct {
	BaseRuntime
}

// NewNodeRuntime creates a new Node.js runtime.
func NewNodeRuntime() *NodeRuntime {
	return &NodeRuntime{
		BaseRuntime: BaseRuntime{
			runtimeType: loomv1.RuntimeType_RUNTIME_TYPE_NODE,
		},
	}
}

// BuildContainerConfig implements Runtime.BuildContainerConfig.
func (nr *NodeRuntime) BuildContainerConfig(ctx context.Context, config *loomv1.DockerBackendConfig) (*container.Config, error) {
	if config == nil {
		return nil, fmt.Errorf("docker backend config is nil")
	}

	// Get Node config
	nodeConfig := config.GetNode()
	if nodeConfig == nil {
		// Default config
		nodeConfig = &loomv1.NodeRuntimeConfig{
			NodeVersion: "20",
		}
	}

	// Determine image
	image := nr.getBaseImage(config, nodeConfig)

	// Build container config
	// Note: Node.js official images have ENTRYPOINT ["node"], so we don't set Cmd here
	// to avoid "node /bin/bash" errors. Commands will be set during Execute().
	containerConfig := &container.Config{
		Image:        image,
		Entrypoint:   []string{"/bin/sh"}, // Use sh as entrypoint (compatible with alpine)
		Cmd:          []string{},          // Empty cmd (will be set by Execute)
		Tty:          false,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		OpenStdin:    true,
		StdinOnce:    false,
	}

	// Set working directory
	if config.WorkingDir != "" {
		containerConfig.WorkingDir = config.WorkingDir
	} else {
		containerConfig.WorkingDir = "/workspace"
	}

	// Apply environment variables
	ApplyEnvironment(containerConfig, config.Environment)

	// Add Node-specific environment
	nodeEnv := map[string]string{
		"NODE_ENV":         "production", // Production mode
		"NPM_CONFIG_CACHE": "/root/.npm", // npm cache location
	}
	ApplyEnvironment(containerConfig, nodeEnv)

	// Apply non-root user (UID 1000, GID 1000)
	// Note: Set to 0:0 to run as root for debugging
	ApplyNonRootUser(containerConfig, 1000, 1000)

	return containerConfig, nil
}

// BuildHostConfig implements Runtime.BuildHostConfig.
func (nr *NodeRuntime) BuildHostConfig(ctx context.Context, config *loomv1.DockerBackendConfig) (*container.HostConfig, error) {
	if config == nil {
		return nil, fmt.Errorf("docker backend config is nil")
	}

	hostConfig := &container.HostConfig{
		NetworkMode: "bridge",
	}

	// Apply resource limits
	ApplyResourceLimits(hostConfig, config.ResourceLimits)

	// Apply user-defined volume mounts
	ApplyVolumeMounts(hostConfig, config.VolumeMounts)

	// Add npm cache volume (if enabled)
	nodeConfig := config.GetNode()
	if nodeConfig != nil && nodeConfig.UseNpmCache {
		cacheMounts := nr.GetCacheMounts(ctx)
		hostConfig.Mounts = append(hostConfig.Mounts, cacheMounts...)
	}

	// Apply security options
	ApplySecurityOptions(hostConfig)

	return hostConfig, nil
}

// PrepareImage implements Runtime.PrepareImage.
func (nr *NodeRuntime) PrepareImage(ctx context.Context, config *loomv1.DockerBackendConfig) (string, error) {
	if config == nil {
		return "", fmt.Errorf("docker backend config is nil")
	}

	nodeConfig := config.GetNode()
	if nodeConfig == nil {
		nodeConfig = &loomv1.NodeRuntimeConfig{
			NodeVersion: "20",
		}
	}

	// Get image name
	image := nr.getBaseImage(config, nodeConfig)

	// For base images: Pull handled by Docker daemon (auto-pull on create)
	// For custom Dockerfiles: Build handled by executor.go
	// For ImageBuildConfig: Generate and build Dockerfile

	return image, nil
}

// InstallPackages implements Runtime.InstallPackages.
func (nr *NodeRuntime) InstallPackages(ctx context.Context, config *loomv1.DockerBackendConfig) ([][]string, error) {
	if config == nil {
		return nil, fmt.Errorf("docker backend config is nil")
	}

	var commands [][]string

	// Install Loom trace library (for distributed tracing)
	// This MUST always run, regardless of nodeConfig
	// Write to /tmp since /usr/local/lib may be read-only (security hardening)
	// NODE_PATH=/tmp is set in executor environment variables
	traceLib := GetNodeTraceLibrary()
	traceLibB64 := base64.StdEncoding.EncodeToString([]byte(traceLib))
	installTraceCmd := fmt.Sprintf("node -e \"const fs = require('fs'); fs.writeFileSync('/tmp/loom-trace.js', Buffer.from('%s', 'base64').toString('utf-8'));\"", traceLibB64)
	commands = append(commands, []string{"sh", "-c", installTraceCmd})

	nodeConfig := config.GetNode()
	if nodeConfig == nil {
		return commands, nil // Return trace library installation only
	}

	// Install from package.json
	if nodeConfig.PackageJsonFile != "" {
		// Copy package.json to working directory (handled by executor)
		// Then run npm install
		commands = append(commands, []string{
			"npm", "install", "--production",
		})
	}

	// Install preinstalled packages
	if len(nodeConfig.PreinstallPackages) > 0 {
		installCmd := []string{"npm", "install", "--global"}
		installCmd = append(installCmd, nodeConfig.PreinstallPackages...)
		commands = append(commands, installCmd)
	}

	return commands, nil
}

// GetCacheMounts implements Runtime.GetCacheMounts.
func (nr *NodeRuntime) GetCacheMounts(ctx context.Context) []mount.Mount {
	return []mount.Mount{
		{
			Type:   mount.TypeVolume,
			Source: "loom-npm-cache", // Named volume (shared across containers)
			Target: "/root/.npm",
		},
	}
}

// getBaseImage returns the Docker image to use.
func (nr *NodeRuntime) getBaseImage(config *loomv1.DockerBackendConfig, nodeConfig *loomv1.NodeRuntimeConfig) string {
	// Check if user specified base_image
	if baseImage := config.GetBaseImage(); baseImage != "" {
		return baseImage
	}

	// Default: node:<version>-slim
	version := nodeConfig.NodeVersion
	if version == "" {
		version = "20"
	}

	// Normalize version (major version only)
	parts := strings.Split(version, ".")
	version = parts[0] // e.g., "20.1.0" -> "20"

	return fmt.Sprintf("node:%s-slim", version)
}
