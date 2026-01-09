// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVisionTool(t *testing.T) {
	// Create a temporary test image
	tmpDir := t.TempDir()
	imagePath := filepath.Join(tmpDir, "test.png")

	// Create a small 1x1 PNG image (minimal valid PNG)
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}

	err := os.WriteFile(imagePath, pngData, 0644)
	require.NoError(t, err)

	tool := NewVisionTool(tmpDir)

	t.Run("name", func(t *testing.T) {
		assert.Equal(t, "analyze_image", tool.Name())
	})

	t.Run("description", func(t *testing.T) {
		desc := tool.Description()
		assert.Contains(t, desc, "vision")
		assert.Contains(t, desc, "image")
	})

	t.Run("successful image read", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), map[string]interface{}{
			"image_path": "test.png",
			"question":   "What's in this image?",
		})

		require.NoError(t, err)
		assert.True(t, result.Success)

		// Check returned data
		data, ok := result.Data.(map[string]interface{})
		require.True(t, ok, "result.Data should be map[string]interface{}")
		assert.Equal(t, imagePath, data["image_path"])
		assert.Equal(t, "image/png", data["media_type"])
		assert.True(t, data["vision_ready"].(bool))

		// Verify base64 data is valid
		base64Data := data["base64_data"].(string)
		decodedData, err := base64.StdEncoding.DecodeString(base64Data)
		require.NoError(t, err)
		assert.Equal(t, pngData, decodedData)
	})

	t.Run("missing image_path parameter", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), map[string]interface{}{})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
	})

	t.Run("non-existent file", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), map[string]interface{}{
			"image_path": "nonexistent.png",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Equal(t, "FILE_NOT_FOUND", result.Error.Code)
	})

	t.Run("unsupported format", func(t *testing.T) {
		txtPath := filepath.Join(tmpDir, "test.txt")
		err := os.WriteFile(txtPath, []byte("not an image"), 0644)
		require.NoError(t, err)

		result, err := tool.Execute(context.Background(), map[string]interface{}{
			"image_path": "test.txt",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Equal(t, "UNSUPPORTED_FORMAT", result.Error.Code)
	})

	t.Run("jpeg image", func(t *testing.T) {
		jpegPath := filepath.Join(tmpDir, "test.jpg")
		// Minimal valid JPEG
		jpegData := []byte{
			0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46,
			0x49, 0x46, 0x00, 0x01, 0x01, 0x00, 0x00, 0x01,
			0x00, 0x01, 0x00, 0x00, 0xFF, 0xD9,
		}
		err := os.WriteFile(jpegPath, jpegData, 0644)
		require.NoError(t, err)

		result, err := tool.Execute(context.Background(), map[string]interface{}{
			"image_path": "test.jpg",
		})

		require.NoError(t, err)
		assert.True(t, result.Success)

		data, ok := result.Data.(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "image/jpeg", data["media_type"])
	})
}
