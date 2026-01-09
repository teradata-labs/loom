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

// PythonRuntime configures Python containers with pip package management.
//
// Features:
//   - Multiple Python versions (3.9, 3.10, 3.11, 3.12)
//   - Pip caching via Docker volume (/root/.cache/pip)
//   - Requirements.txt support
//   - Preinstalled packages (numpy, pandas, etc.)
//   - Virtual environment support (optional)
//
// Base Images (official Python):
//   - python:3.11-slim (default, ~45MB compressed, 120MB uncompressed)
//   - python:3.10-slim
//   - python:3.12-slim
//
// Package Installation:
//  1. Preinstalled packages: pip install <pkg1> <pkg2> ...
//  2. Requirements file: pip install -r requirements.txt
//  3. Pip cache persisted to volume for fast reinstalls
//
// Security:
//   - Read-only rootfs (except /tmp and /root/.cache/pip)
//   - Non-root user (future: create 'loom' user)
//   - Capability dropping
type PythonRuntime struct {
	BaseRuntime
}

// NewPythonRuntime creates a new Python runtime.
func NewPythonRuntime() *PythonRuntime {
	return &PythonRuntime{
		BaseRuntime: BaseRuntime{
			runtimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		},
	}
}

// BuildContainerConfig implements Runtime.BuildContainerConfig.
func (pr *PythonRuntime) BuildContainerConfig(ctx context.Context, config *loomv1.DockerBackendConfig) (*container.Config, error) {
	if config == nil {
		return nil, fmt.Errorf("docker backend config is nil")
	}

	// Get Python config
	pythonConfig := config.GetPython()
	if pythonConfig == nil {
		// Default config
		pythonConfig = &loomv1.PythonRuntimeConfig{
			PythonVersion: "3.11",
		}
	}

	// Determine image
	image := pr.getBaseImage(config, pythonConfig)

	// Build container config
	containerConfig := &container.Config{
		Image:        image,
		Cmd:          []string{"/bin/bash"}, // Default command (overridden by Execute)
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

	// Add Python-specific environment
	pythonEnv := map[string]string{
		"PYTHONUNBUFFERED": "1",                // No stdout buffering
		"PIP_NO_CACHE_DIR": "0",                // Enable pip cache
		"PIP_CACHE_DIR":    "/root/.cache/pip", // Pip cache location
	}
	ApplyEnvironment(containerConfig, pythonEnv)

	// Virtual environment support
	if pythonConfig.VirtualEnv != "" {
		venvEnv := map[string]string{
			"VIRTUAL_ENV": fmt.Sprintf("/workspace/.venv/%s", pythonConfig.VirtualEnv),
			"PATH":        fmt.Sprintf("/workspace/.venv/%s/bin:$PATH", pythonConfig.VirtualEnv),
		}
		ApplyEnvironment(containerConfig, venvEnv)
	}

	// Apply non-root user (UID 1000, GID 1000)
	// Note: Set to 0:0 to run as root for debugging
	ApplyNonRootUser(containerConfig, 1000, 1000)

	return containerConfig, nil
}

// BuildHostConfig implements Runtime.BuildHostConfig.
func (pr *PythonRuntime) BuildHostConfig(ctx context.Context, config *loomv1.DockerBackendConfig) (*container.HostConfig, error) {
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

	// Add pip cache volume (if enabled)
	pythonConfig := config.GetPython()
	if pythonConfig != nil && pythonConfig.UsePipCache {
		cacheMounts := pr.GetCacheMounts(ctx)
		hostConfig.Mounts = append(hostConfig.Mounts, cacheMounts...)
	}

	// Apply security options
	ApplySecurityOptions(hostConfig)

	return hostConfig, nil
}

// PrepareImage implements Runtime.PrepareImage.
func (pr *PythonRuntime) PrepareImage(ctx context.Context, config *loomv1.DockerBackendConfig) (string, error) {
	if config == nil {
		return "", fmt.Errorf("docker backend config is nil")
	}

	pythonConfig := config.GetPython()
	if pythonConfig == nil {
		pythonConfig = &loomv1.PythonRuntimeConfig{
			PythonVersion: "3.11",
		}
	}

	// Get image name
	image := pr.getBaseImage(config, pythonConfig)

	// For base images: Pull handled by Docker daemon (auto-pull on create)
	// For custom Dockerfiles: Build handled by executor.go
	// For ImageBuildConfig: Generate and build Dockerfile

	return image, nil
}

// InstallPackages implements Runtime.InstallPackages.
func (pr *PythonRuntime) InstallPackages(ctx context.Context, config *loomv1.DockerBackendConfig) ([][]string, error) {
	if config == nil {
		return nil, fmt.Errorf("docker backend config is nil")
	}

	var commands [][]string

	// Install Loom trace library (for distributed tracing)
	// This MUST always run, regardless of pythonConfig
	// Write to /tmp since /usr/local/lib may be read-only (security hardening)
	// PYTHONPATH=/tmp is set in executor environment variables
	traceLib := GetPythonTraceLibrary()
	traceLibB64 := base64.StdEncoding.EncodeToString([]byte(traceLib))
	installTraceCmd := fmt.Sprintf("python3 -c \"import base64; open('/tmp/loom_trace.py', 'w').write(base64.b64decode('%s').decode('utf-8'))\"", traceLibB64)
	commands = append(commands, []string{"sh", "-c", installTraceCmd})

	pythonConfig := config.GetPython()
	if pythonConfig == nil {
		return commands, nil // Return trace library installation only
	}

	// Create virtual environment (if specified)
	if pythonConfig.VirtualEnv != "" {
		commands = append(commands, []string{
			"python", "-m", "venv", fmt.Sprintf("/workspace/.venv/%s", pythonConfig.VirtualEnv),
		})
	}

	// Install from requirements.txt
	if pythonConfig.RequirementsFile != "" {
		commands = append(commands, []string{
			"pip", "install", "-r", pythonConfig.RequirementsFile,
		})
	}

	// Install preinstalled packages
	if len(pythonConfig.PreinstallPackages) > 0 {
		installCmd := []string{"pip", "install"}
		installCmd = append(installCmd, pythonConfig.PreinstallPackages...)
		commands = append(commands, installCmd)
	}

	return commands, nil
}

// GetCacheMounts implements Runtime.GetCacheMounts.
func (pr *PythonRuntime) GetCacheMounts(ctx context.Context) []mount.Mount {
	return []mount.Mount{
		{
			Type:   mount.TypeVolume,
			Source: "loom-pip-cache", // Named volume (shared across containers)
			Target: "/root/.cache/pip",
		},
	}
}

// getBaseImage returns the Docker image to use.
func (pr *PythonRuntime) getBaseImage(config *loomv1.DockerBackendConfig, pythonConfig *loomv1.PythonRuntimeConfig) string {
	// Check if user specified base_image
	if baseImage := config.GetBaseImage(); baseImage != "" {
		return baseImage
	}

	// Default: python:<version>-slim
	version := pythonConfig.PythonVersion
	if version == "" {
		version = "3.11"
	}

	// Normalize version (remove patch version if present)
	parts := strings.Split(version, ".")
	if len(parts) > 2 {
		version = parts[0] + "." + parts[1] // e.g., "3.11.5" -> "3.11"
	}

	return fmt.Sprintf("python:%s-slim", version)
}
