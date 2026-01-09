// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"testing"
)

func TestLoadROMContent_TD(t *testing.T) {
	// Test explicit TD ROM loading
	content := LoadROMContent("TD", "")
	if len(content) == 0 {
		t.Fatal("TD ROM should not be empty")
	}
	if len(content) < 30000 {
		t.Fatalf("TD ROM should be ~31KB, got %d bytes", len(content))
	}
	t.Logf("TD ROM loaded: %d bytes", len(content))
}

func TestLoadROMContent_AutoDetect(t *testing.T) {
	// Test auto-detection from backend path
	content := LoadROMContent("auto", "teradata://localhost")
	if len(content) == 0 {
		t.Fatal("Auto-detected Teradata ROM should not be empty")
	}
	if len(content) < 30000 {
		t.Fatalf("TD ROM should be ~31KB, got %d bytes", len(content))
	}
	t.Logf("Auto-detected TD ROM: %d bytes", len(content))
}

func TestLoadROMContent_Empty(t *testing.T) {
	// Test explicit empty ROM
	content := LoadROMContent("", "")
	if len(content) != 0 {
		t.Fatalf("Empty ROM should be empty, got %d bytes", len(content))
	}
}

func TestGetAvailableROMs(t *testing.T) {
	roms := GetAvailableROMs()
	if len(roms) == 0 {
		t.Fatal("Should have at least one ROM available")
	}
	t.Logf("Available ROMs: %v", roms)
}

func TestGetROMSize(t *testing.T) {
	size := GetROMSize("TD")
	if size == 0 {
		t.Fatal("TD ROM size should not be 0")
	}
	if size < 30000 {
		t.Fatalf("TD ROM should be ~31KB, got %d bytes", size)
	}
	t.Logf("TD ROM size: %d bytes", size)
}
