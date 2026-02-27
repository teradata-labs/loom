// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

const (
	// MaxImageSize is the maximum image file size we'll read (20MB).
	// Most LLM providers have image size limits around 5-20MB.
	MaxImageSize = 20 * 1024 * 1024
)

// VisionTool provides image analysis capabilities for agents.
// Enables vision-based tasks like chart interpretation, OCR, screenshot analysis, etc.
type VisionTool struct {
	baseDir string // Optional base directory for safety
}

// NewVisionTool creates a new vision tool.
// If baseDir is empty, reads from current directory (with safety checks).
func NewVisionTool(baseDir string) *VisionTool {
	if baseDir == "" {
		baseDir, _ = os.Getwd()
	}
	return &VisionTool{
		baseDir: baseDir,
	}
}

func (t *VisionTool) Name() string {
	return "analyze_image"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/vision.yaml).
// This fallback is used only when prompts are not configured.
func (t *VisionTool) Description() string {
	return `Analyzes images using vision capabilities. Understands charts, screenshots, diagrams, photos, and scanned documents.
Supports JPEG, PNG, GIF, WebP (max 20MB). Requires a multi-modal LLM provider.`
}

func (t *VisionTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for analyzing images",
		map[string]*shuttle.JSONSchema{
			"image_path": shuttle.NewStringSchema("Path to the image file (required). Supports JPEG, PNG, GIF, WebP formats."),
			"question":   shuttle.NewStringSchema("Optional question or instruction about the image."),
		},
		[]string{"image_path"},
	)
}

func (t *VisionTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Extract parameters
	imagePath, ok := params["image_path"].(string)
	if !ok || imagePath == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "image_path is required",
				Suggestion: "Provide an image file path (e.g., 'charts/sales.png' or '/tmp/screenshot.png')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	question := ""
	if q, ok := params["question"].(string); ok {
		question = q
	}

	// Safety: Clean the path and make it absolute
	cleanPath := filepath.Clean(imagePath)

	// If relative, make it relative to baseDir
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(t.baseDir, cleanPath)
	}

	// Safety: Prevent reading sensitive locations
	if isSensitiveReadPath(cleanPath) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "UNSAFE_PATH",
				Message:    fmt.Sprintf("Cannot read from sensitive location: %s", cleanPath),
				Suggestion: "Read image files from your project directory or user data directories",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Check if file exists
	info, err := os.Stat(cleanPath)
	if os.IsNotExist(err) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_NOT_FOUND",
				Message:    fmt.Sprintf("Image file not found: %s", cleanPath),
				Suggestion: "Check the file path and ensure the image file exists",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "STAT_FAILED",
				Message: fmt.Sprintf("Failed to stat image file: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Check if it's a directory
	if info.IsDir() {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "IS_DIRECTORY",
				Message:    fmt.Sprintf("Path is a directory, not a file: %s", cleanPath),
				Suggestion: "Provide a path to an image file, not a directory",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Check file size
	if info.Size() > MaxImageSize {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "IMAGE_TOO_LARGE",
				Message:    fmt.Sprintf("Image too large: %d bytes (max: %d bytes)", info.Size(), MaxImageSize),
				Suggestion: "Resize or compress the image before analysis. Most LLM providers limit images to 5-20MB.",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Detect media type from extension
	mediaType := detectMediaType(cleanPath)
	if mediaType == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "UNSUPPORTED_FORMAT",
				Message:    fmt.Sprintf("Unsupported image format: %s", filepath.Ext(cleanPath)),
				Suggestion: "Use supported formats: JPEG (.jpg, .jpeg), PNG (.png), GIF (.gif), WebP (.webp)",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Read the image file
	imageData, err := os.ReadFile(cleanPath)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "READ_FAILED",
				Message: fmt.Sprintf("Failed to read image file: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Encode to base64
	base64Image := base64.StdEncoding.EncodeToString(imageData)

	// Return structured data for the agent to use
	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"image_path":      cleanPath,
			"media_type":      mediaType,
			"base64_data":     base64Image,
			"size_bytes":      info.Size(),
			"question":        question,
			"vision_ready":    true, // Flag to indicate this is vision data
			"analysis_prompt": buildAnalysisPrompt(question),
		},
		Metadata: map[string]interface{}{
			"image_path": cleanPath,
			"media_type": mediaType,
			"size":       info.Size(),
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *VisionTool) Backend() string {
	return "" // Backend-agnostic
}

// detectMediaType detects the MIME type from file extension.
func detectMediaType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

// buildAnalysisPrompt builds a prompt for the LLM based on the question.
func buildAnalysisPrompt(question string) string {
	if question != "" {
		return question
	}
	return "Analyze this image and describe what you see. Include any relevant details, text, data, or insights."
}
