// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	_ "embed"
	"strings"
)

// Backend-specific ROM files (embedded at compile time)
// Note: go:embed doesn't support paths with .. so we embed from the current package
// The TD.rom file is symlinked or copied to this package's roms directory
//
//go:embed roms/TD.rom
var teradataROM string

// LoadROMContent loads ROM (Read-Only Memory) content based on configuration.
// ROM provides domain-specific knowledge that enhances the agent's system prompt.
//
// Parameters:
//   - romID: ROM identifier from agent config ("TD", "teradata", "auto", or "")
//   - backendPath: Backend path from agent metadata (for auto-detection)
//
// Returns ROM content (markdown format) or empty string if no ROM is configured.
//
// ROM Resolution Order:
//  1. If romID == "" (explicitly empty): No ROM
//  2. If romID == "TD" or "teradata": Load Teradata ROM
//  3. If romID == "auto": Auto-detect from backend_path
//     - backendPath contains "teradata" → Teradata ROM
//     - backendPath contains "vantage" → Teradata ROM (Vantage is Teradata)
//  4. Default: No ROM
func LoadROMContent(romID string, backendPath string) string {
	// Explicitly empty ROM
	if romID == "" && backendPath == "" {
		return ""
	}

	// Normalize ROM ID
	romLower := strings.ToLower(strings.TrimSpace(romID))

	// Explicit ROM selection
	switch romLower {
	case "td", "teradata":
		return teradataROM
	case "":
		// Empty but have backend_path, try auto-detection
		if backendPath != "" {
			romLower = "auto"
		} else {
			return ""
		}
	}

	// Auto-detection based on backend path
	if romLower == "auto" || romLower == "" {
		backendLower := strings.ToLower(backendPath)
		if strings.Contains(backendLower, "teradata") || strings.Contains(backendLower, "vantage") {
			return teradataROM
		}
		// Add more backend detections here as new ROMs are added
		// if strings.Contains(backendLower, "postgres") {
		//     return postgresROM
		// }
	}

	// No ROM found
	return ""
}

// GetAvailableROMs returns a list of available ROM identifiers.
// Useful for documentation and validation.
func GetAvailableROMs() []string {
	return []string{
		"TD",       // Teradata SQL guidance (31KB)
		"teradata", // Alias for TD
		"auto",     // Auto-detect from backend (default)
		"",         // No ROM (empty)
	}
}

// GetROMSize returns the size of a ROM in bytes.
// Returns 0 if ROM doesn't exist.
func GetROMSize(romID string) int {
	romLower := strings.ToLower(strings.TrimSpace(romID))
	switch romLower {
	case "td", "teradata":
		return len(teradataROM)
	default:
		return 0
	}
}
