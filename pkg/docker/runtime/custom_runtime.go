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

// CustomRuntime configures containers with arbitrary images and entrypoints.
//
// Use Cases:
//   - Ruby, Rust, Go, Java containers
//   - Custom-built images with specific toolchains
//   - Legacy applications with complex setups
//   - Multi-language environments
//
// Configuration:
//   - base_image: Any Docker image (e.g., "rust:1.75", "ruby:3.2", custom registry)
//   - entrypoint_cmd: Custom entrypoint (e.g., ["./my-binary", "--flag"])
//   - labels: Container labels for organization
//
// Features:
//   - No package management (handled in base image or Dockerfile)
//   - Flexible entrypoint configuration
//   - Full resource limit support
//   - Security hardening (read-only rootfs, capability drops)
//
// Example Custom Configurations:
//
//  1. Rust container:
//     base_image: "rust:1.75-slim"
//     entrypoint_cmd: ["cargo", "run"]
//
//  2. Go container:
//     base_image: "golang:1.21-alpine"
//     entrypoint_cmd: ["./app"]
//
//  3. Custom toolchain:
//     base_image: "gcr.io/my-project/custom-toolchain:latest"
//     entrypoint_cmd: ["./tool", "--config", "/config.yaml"]
type CustomRuntime struct {
	BaseRuntime
}

// NewCustomRuntime creates a new Custom runtime.
func NewCustomRuntime() *CustomRuntime {
	return &CustomRuntime{
		BaseRuntime: BaseRuntime{
			runtimeType: loomv1.RuntimeType_RUNTIME_TYPE_CUSTOM,
		},
	}
}

// BuildContainerConfig implements Runtime.BuildContainerConfig.
func (cr *CustomRuntime) BuildContainerConfig(ctx context.Context, config *loomv1.DockerBackendConfig) (*container.Config, error) {
	if config == nil {
		return nil, fmt.Errorf("docker backend config is nil")
	}

	// Get Custom config
	customConfig := config.GetCustom()
	if customConfig == nil {
		return nil, fmt.Errorf("custom runtime config is required for RUNTIME_TYPE_CUSTOM")
	}

	// Get base image
	image := config.GetBaseImage()
	if image == "" {
		return nil, fmt.Errorf("base_image is required for RUNTIME_TYPE_CUSTOM")
	}

	// Build container config
	containerConfig := &container.Config{
		Image:        image,
		Tty:          false,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		OpenStdin:    true,
		StdinOnce:    false,
	}

	// Set entrypoint (if specified)
	if len(customConfig.EntrypointCmd) > 0 {
		containerConfig.Entrypoint = customConfig.EntrypointCmd
	} else {
		// Default: /bin/bash (overridden by Execute)
		containerConfig.Cmd = []string{"/bin/bash"}
	}

	// Set working directory
	if config.WorkingDir != "" {
		containerConfig.WorkingDir = config.WorkingDir
	} else {
		containerConfig.WorkingDir = "/workspace"
	}

	// Apply environment variables
	ApplyEnvironment(containerConfig, config.Environment)

	// Apply custom labels
	if len(customConfig.Labels) > 0 {
		containerConfig.Labels = customConfig.Labels
	}

	// Apply non-root user (UID 1000, GID 1000)
	// Note: Set to 0:0 to run as root for debugging
	ApplyNonRootUser(containerConfig, 1000, 1000)

	return containerConfig, nil
}

// BuildHostConfig implements Runtime.BuildHostConfig.
func (cr *CustomRuntime) BuildHostConfig(ctx context.Context, config *loomv1.DockerBackendConfig) (*container.HostConfig, error) {
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

	// Apply security options
	ApplySecurityOptions(hostConfig)

	return hostConfig, nil
}

// PrepareImage implements Runtime.PrepareImage.
func (cr *CustomRuntime) PrepareImage(ctx context.Context, config *loomv1.DockerBackendConfig) (string, error) {
	if config == nil {
		return "", fmt.Errorf("docker backend config is nil")
	}

	// Get base image
	image := config.GetBaseImage()
	if image == "" {
		return "", fmt.Errorf("base_image is required for RUNTIME_TYPE_CUSTOM")
	}

	// For custom images: Pull handled by Docker daemon (auto-pull on create)
	// User is responsible for ensuring image is accessible (public registry or private with auth)

	return image, nil
}

// InstallPackages implements Runtime.InstallPackages.
// For custom runtime: No standard package management.
// User is responsible for baking dependencies into base image or Dockerfile.
func (cr *CustomRuntime) InstallPackages(ctx context.Context, config *loomv1.DockerBackendConfig) ([][]string, error) {
	// No standard package installation for custom runtime
	// All dependencies should be in base image
	return nil, nil
}

// GetCacheMounts implements Runtime.GetCacheMounts.
// For custom runtime: No standard cache mounts.
// User can specify cache mounts via volume_mounts in config.
func (cr *CustomRuntime) GetCacheMounts(ctx context.Context) []mount.Mount {
	// No default cache mounts for custom runtime
	return nil
}
