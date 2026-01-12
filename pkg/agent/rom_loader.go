// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	_ "embed"
	"fmt"
	"strings"
)

// Base ROM - operational guidance for all agents
// Single source of truth: pkg/agent/roms/START_HERE.md
// Embedded into binary at compile time and deployed to ~/.loom/START_HERE.md
//
//go:embed roms/START_HERE.md
var baseROM string

// Backend-specific ROM files (embedded at compile time)
// Note: go:embed doesn't support paths with .. so we embed from the current package
// The TD.rom file is symlinked or copied to this package's roms directory
//
//go:embed roms/TD.rom
var teradataROM string

// LoadROMContent loads ROM (Read-Only Memory) content based on configuration.
// ROM provides operational guidance and optional domain-specific knowledge.
//
// Architecture:
//   - Base ROM (START_HERE.md): Always included for all agents (5KB)
//     Provides: tool discovery, communication patterns, artifacts, memory usage
//   - Domain ROMs: Optional specialized knowledge (e.g., TD.rom for Teradata SQL)
//     Automatically composed with base ROM using clear separators
//
// Parameters:
//   - romID: ROM identifier from agent config ("TD", "teradata", "auto", "none", or "")
//   - backendPath: Backend path from agent metadata (for auto-detection)
//
// Returns composed ROM content (markdown format).
//
// ROM Composition Rules:
//  1. Base ROM is ALWAYS included (operational guidance)
//  2. Domain ROM is added if specified (with separator)
//  3. Use romID="none" to opt-out of ALL ROMs (rare)
//  4. Empty romID="" = base ROM only (no domain knowledge)
//
// Examples:
//
//	romID=""         → Base ROM only (5KB)
//	romID="TD"       → Base + Teradata ROM (5KB + 11KB = 16KB)
//	romID="auto"     → Base + auto-detected domain ROM
//	romID="none"     → No ROM at all (explicit opt-out)
func LoadROMContent(romID string, backendPath string) string {
	// Normalize ROM ID
	romLower := strings.ToLower(strings.TrimSpace(romID))

	// Special opt-out: "none" returns empty string
	if romLower == "none" {
		return ""
	}

	// Start with base ROM (operational guidance for all agents)
	content := baseROM

	// Determine domain ROM
	var domainROM string

	// Explicit domain ROM selection
	switch romLower {
	case "td", "teradata":
		domainROM = teradataROM
	case "auto":
		// Auto-detect from backend path
		domainROM = detectDomainROM(backendPath)
	case "":
		// Empty means base ROM only, try auto-detection
		if backendPath != "" {
			domainROM = detectDomainROM(backendPath)
		}
	}

	// Compose base + domain ROM with separator
	if domainROM != "" {
		content += "\n\n" + formatROMSeparator("DOMAIN-SPECIFIC KNOWLEDGE") + "\n\n" + domainROM
	}

	return content
}

// detectDomainROM auto-detects domain ROM from backend path.
func detectDomainROM(backendPath string) string {
	if backendPath == "" {
		return ""
	}

	backendLower := strings.ToLower(backendPath)

	// Teradata detection
	if strings.Contains(backendLower, "teradata") || strings.Contains(backendLower, "vantage") {
		return teradataROM
	}

	// Add more backend detections here as new ROMs are added
	// if strings.Contains(backendLower, "postgres") {
	//     return postgresROM
	// }

	return ""
}

// formatROMSeparator creates a clear visual separator between ROM sections.
func formatROMSeparator(title string) string {
	line := strings.Repeat("=", 70)
	return fmt.Sprintf("%s\n# %s\n%s", line, title, line)
}

// GetAvailableROMs returns a list of available ROM identifiers.
// Useful for documentation and validation.
func GetAvailableROMs() []string {
	return []string{
		"",         // Base ROM only (~5KB operational guidance)
		"TD",       // Base + Teradata SQL guidance (~16KB total)
		"teradata", // Alias for TD
		"auto",     // Base + auto-detected domain ROM
		"none",     // No ROM at all (explicit opt-out)
	}
}

// GetROMSize returns the total size of composed ROM in bytes.
// Includes base ROM + domain ROM if applicable.
func GetROMSize(romID string) int {
	romLower := strings.ToLower(strings.TrimSpace(romID))

	// Special case: "none" means no ROM
	if romLower == "none" {
		return 0
	}

	// Always include base ROM
	totalSize := len(baseROM)

	// Add domain ROM size if applicable
	switch romLower {
	case "td", "teradata":
		totalSize += len(teradataROM)
		// Add separator overhead (~150 bytes)
		totalSize += 150
	}

	return totalSize
}

// GetBaseROMSize returns the size of the base ROM (START_HERE.md).
func GetBaseROMSize() int {
	return len(baseROM)
}

// GetDomainROMSize returns the size of a specific domain ROM.
// Returns 0 if ROM doesn't exist.
func GetDomainROMSize(romID string) int {
	romLower := strings.ToLower(strings.TrimSpace(romID))
	switch romLower {
	case "td", "teradata":
		return len(teradataROM)
	default:
		return 0
	}
}

// GetBaseROM returns the raw base ROM content (START_HERE.md).
// This is the single source of truth for the base ROM, used by both:
// - Agent ROM loading (via LoadROMContent)
// - Deployment to ~/.loom/START_HERE.md (via embedded package)
func GetBaseROM() []byte {
	return []byte(baseROM)
}
