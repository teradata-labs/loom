// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"strings"
	"testing"
)

func TestLoadROMContent_BaseOnly(t *testing.T) {
	// Test base ROM only (no domain ROM)
	content := LoadROMContent("", "")
	if len(content) == 0 {
		t.Fatal("Base ROM should not be empty")
	}
	if len(content) < 3000 {
		t.Fatalf("Base ROM should be ~8KB, got %d bytes", len(content))
	}
	// Should contain START_HERE content
	if !strings.Contains(content, "START HERE") {
		t.Fatal("Base ROM should contain START_HERE content")
	}
	if !strings.Contains(content, "Tool Discovery") {
		t.Fatal("Base ROM should contain Tool Discovery section")
	}
	// Should NOT contain domain-specific content
	if strings.Contains(content, "DOMAIN-SPECIFIC KNOWLEDGE") {
		t.Fatal("Base-only ROM should not contain domain separator")
	}
	t.Logf("Base ROM loaded: %d bytes", len(content))
}

func TestLoadROMContent_BaseAndTD(t *testing.T) {
	// Test base + Teradata ROM composition
	content := LoadROMContent("TD", "")
	if len(content) == 0 {
		t.Fatal("Composed ROM should not be empty")
	}
	// Should be larger than base alone (START_HERE ~8KB + TD ~11KB = ~19KB)
	if len(content) < 10000 {
		t.Fatalf("Base + TD ROM should be ~19KB, got %d bytes", len(content))
	}
	// Should contain base ROM
	if !strings.Contains(content, "START HERE") {
		t.Fatal("Composed ROM should contain base content")
	}
	// Should contain domain separator
	if !strings.Contains(content, "DOMAIN-SPECIFIC KNOWLEDGE") {
		t.Fatal("Composed ROM should contain domain separator")
	}
	// Should contain Teradata-specific content
	if !strings.Contains(content, "Teradata SQL") {
		t.Fatal("Composed ROM should contain Teradata content")
	}
	t.Logf("Composed ROM loaded: %d bytes", len(content))
}

func TestLoadROMContent_AutoDetect(t *testing.T) {
	// Test auto-detection from backend path
	content := LoadROMContent("auto", "teradata://localhost")
	if len(content) == 0 {
		t.Fatal("Auto-detected ROM should not be empty")
	}
	// Should detect Teradata and compose
	if len(content) < 10000 {
		t.Fatalf("Auto-detected should give base + TD ROM (~19KB), got %d bytes", len(content))
	}
	// Should contain both base and domain content
	if !strings.Contains(content, "START HERE") {
		t.Fatal("Auto-detected ROM should contain base content")
	}
	if !strings.Contains(content, "Teradata SQL") {
		t.Fatal("Auto-detected ROM should contain Teradata content")
	}
	t.Logf("Auto-detected ROM: %d bytes", len(content))
}

func TestLoadROMContent_AutoDetectNoMatch(t *testing.T) {
	// Test auto-detection with non-matching backend
	content := LoadROMContent("auto", "postgres://localhost")
	if len(content) == 0 {
		t.Fatal("Should still return base ROM")
	}
	// Should only have base ROM (no postgres ROM exists yet)
	if len(content) > 10000 {
		t.Fatalf("Base-only ROM should be ~8KB, got %d bytes (might have unexpected domain ROM)", len(content))
	}
	// Should contain base content
	if !strings.Contains(content, "START HERE") {
		t.Fatal("Should contain base content")
	}
	// Should NOT contain domain separator (no domain ROM matched)
	if strings.Contains(content, "DOMAIN-SPECIFIC KNOWLEDGE") {
		t.Fatal("Should not contain domain separator when no domain ROM matches")
	}
	t.Logf("Auto-detected (no match) ROM: %d bytes", len(content))
}

func TestLoadROMContent_OptOut(t *testing.T) {
	// Test explicit opt-out with "none"
	content := LoadROMContent("none", "")
	if len(content) != 0 {
		t.Fatalf("ROM with 'none' should be empty, got %d bytes", len(content))
	}
	t.Log("Opt-out successful: ROM is empty")
}

func TestGetAvailableROMs(t *testing.T) {
	roms := GetAvailableROMs()
	if len(roms) == 0 {
		t.Fatal("Should have at least one ROM available")
	}
	// Check for expected ROM options
	expectedROMs := []string{"", "TD", "teradata", "auto", "none"}
	for _, expected := range expectedROMs {
		found := false
		for _, rom := range roms {
			if rom == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected ROM '%s' not found in available ROMs", expected)
		}
	}
	t.Logf("Available ROMs: %v", roms)
}

func TestGetROMSize_BaseOnly(t *testing.T) {
	// Test size of base ROM only
	size := GetROMSize("")
	if size == 0 {
		t.Fatal("Base ROM size should not be 0")
	}
	// Base ROM now ~8KB (expanded from 5KB to include working memory guidance)
	if size < 4000 {
		t.Fatalf("Base ROM should be ~8KB, got %d bytes", size)
	}
	t.Logf("Base ROM size: %d bytes", size)
}

func TestGetROMSize_Composed(t *testing.T) {
	// Test size of composed ROM (base + TD)
	size := GetROMSize("TD")
	if size == 0 {
		t.Fatal("Composed ROM size should not be 0")
	}
	// Base ROM (8KB) + TD ROM (11KB) + separator = ~19KB
	if size < 10000 {
		t.Fatalf("Base + TD ROM should be ~19KB, got %d bytes", size)
	}
	t.Logf("Composed ROM size: %d bytes", size)
}

func TestGetROMSize_OptOut(t *testing.T) {
	// Test size when opted out
	size := GetROMSize("none")
	if size != 0 {
		t.Fatalf("ROM size with 'none' should be 0, got %d bytes", size)
	}
}

func TestGetBaseROMSize(t *testing.T) {
	size := GetBaseROMSize()
	if size == 0 {
		t.Fatal("Base ROM size should not be 0")
	}
	// Base ROM now ~8KB (expanded from 5KB to include working memory guidance)
	if size < 4000 {
		t.Fatalf("Base ROM should be ~8KB, got %d bytes", size)
	}
	t.Logf("Base ROM size: %d bytes", size)
}

func TestGetDomainROMSize(t *testing.T) {
	// Test Teradata domain ROM size
	size := GetDomainROMSize("TD")
	if size == 0 {
		t.Fatal("TD domain ROM size should not be 0")
	}
	// ROM was optimized from 31KB to ~11KB
	if size < 10000 {
		t.Fatalf("TD domain ROM should be ~11KB, got %d bytes", size)
	}
	if size > 15000 {
		t.Fatalf("TD domain ROM should be ~11KB (optimized), got %d bytes - did someone expand it?", size)
	}
	t.Logf("TD domain ROM size: %d bytes", size)
}

func TestROMSeparatorFormat(t *testing.T) {
	separator := formatROMSeparator("TEST SECTION")
	if !strings.Contains(separator, "TEST SECTION") {
		t.Fatal("Separator should contain the section title")
	}
	if !strings.Contains(separator, "=") {
		t.Fatal("Separator should contain separator line")
	}
	t.Logf("Separator format:\n%s", separator)
}
