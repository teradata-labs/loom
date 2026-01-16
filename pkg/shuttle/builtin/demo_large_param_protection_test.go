// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
)

// TestDemo_AgentGeneratingLargeSummary simulates an agent trying to write
// a large data quality analysis, demonstrating both prevention and optimization.
func TestDemo_AgentGeneratingLargeSummary(t *testing.T) {
	tmpDir := t.TempDir()

	// Setup executor with shared memory (as used in production)
	sharedMem := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       10 * 1024 * 1024,
		CompressionThreshold: 1 * 1024 * 1024,
		TTLSeconds:           3600,
	})

	reg := shuttle.NewRegistry()
	fileWriteTool := NewFileWriteTool(tmpDir)
	reg.Register(fileWriteTool)

	exec := shuttle.NewExecutor(reg)
	exec.SetSharedMemory(sharedMem, 2560) // 2.5KB threshold

	t.Log("=== Scenario 1: Agent tries to generate 60KB summary (PREVENTED) ===")

	// Simulate agent generating a very large data quality report
	largeReport := generateDataQualityReport(120) // 60KB+ (each row ~458 bytes, 120*458 = ~55KB)
	t.Logf("Generated report size: %d bytes (%.1f KB)", len(largeReport), float64(len(largeReport))/1024)

	// Ensure we're actually over the limit for the test
	if len(largeReport) <= MaxSafeContentSize {
		t.Fatalf("Test setup error: report size %d is not over %d byte limit", len(largeReport), MaxSafeContentSize)
	}

	params := map[string]interface{}{
		"path":    "data_quality_analysis.md",
		"content": largeReport,
		"mode":    "create",
	}

	result, err := exec.Execute(context.Background(), "file_write", params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Should be rejected by schema validation
	if result.Success {
		t.Errorf("Expected failure for %d byte content (50KB limit)", len(largeReport))
		t.Fatalf("Tool incorrectly accepted oversized content")
	}

	if result.Error == nil {
		t.Fatal("Expected CONTENT_TOO_LARGE error, got nil error")
	}

	if result.Error.Code != "CONTENT_TOO_LARGE" {
		t.Errorf("Expected CONTENT_TOO_LARGE error, got: %s", result.Error.Code)
	}

	t.Logf("✓ Schema limit prevented %d byte generation", len(largeReport))
	t.Logf("  Error: %s", result.Error.Message)
	if len(result.Error.Suggestion) > 100 {
		t.Logf("  Suggestion: %s...", result.Error.Suggestion[:100])
	} else {
		t.Logf("  Suggestion: %s", result.Error.Suggestion)
	}

	t.Log("\n=== Scenario 2: Agent writes incrementally (ACCEPTED) ===")

	// Agent follows suggestion and writes in sections
	sections := []string{
		generateDataQualityReport(20), // ~12KB per section
		generateDataQualityReport(20),
		generateDataQualityReport(20),
	}

	// Create with first section
	result1, err := exec.Execute(context.Background(), "file_write", map[string]interface{}{
		"path":    "data_quality_incremental.md",
		"content": sections[0],
		"mode":    "create",
	})
	if err != nil || !result1.Success {
		t.Fatalf("Section 1 failed: %v", result1.Error)
	}
	t.Logf("✓ Section 1 written (12KB)")

	// Append remaining sections
	for i, section := range sections[1:] {
		result, err := exec.Execute(context.Background(), "file_write", map[string]interface{}{
			"path":    "data_quality_incremental.md",
			"content": section,
			"mode":    "append",
		})
		if err != nil || !result.Success {
			t.Fatalf("Section %d failed: %v", i+2, result.Error)
		}
		t.Logf("✓ Section %d appended (12KB)", i+2)
	}

	t.Log("\n=== Scenario 3: Large parameter optimization (TRANSPARENT) ===")

	// Simulate a 40KB parameter (under schema limit, but over shared memory threshold)
	mediumReport := generateDataQualityReport(25) // ~15KB

	statsBefore := exec.Stats()

	result3, err := exec.Execute(context.Background(), "file_write", map[string]interface{}{
		"path":    "data_quality_medium.md",
		"content": mediumReport,
		"mode":    "create",
	})
	if err != nil || !result3.Success {
		t.Fatalf("Medium report failed: %v", result3.Error)
	}

	statsAfter := exec.Stats()

	t.Logf("✓ 15KB parameter handled transparently")
	t.Logf("  Stored in shared memory: %d times", statsAfter.LargeParamStores-statsBefore.LargeParamStores)
	t.Logf("  Dereferenced: %d times", statsAfter.LargeParamDerefs-statsBefore.LargeParamDerefs)
	t.Logf("  Bytes optimized: %d", statsAfter.LargeParamBytesStored-statsBefore.LargeParamBytesStored)

	// Verify at least one store happened (content is 15KB > 2.5KB threshold)
	if statsAfter.LargeParamStores <= statsBefore.LargeParamStores {
		t.Error("Expected large parameter to be stored in shared memory")
	}

	t.Log("\n=== Summary ===")
	t.Logf("1. Schema limit (50KB) prevents massive LLM generation attempts")
	t.Logf("2. Agent guided to incremental writing patterns (append mode)")
	t.Logf("3. Large parameters (>2.5KB) automatically optimized via shared memory")
	t.Logf("4. Total operations tracked: stores=%d, derefs=%d, bytes=%d",
		statsAfter.LargeParamStores, statsAfter.LargeParamDerefs, statsAfter.LargeParamBytesStored)
}

// generateDataQualityReport creates a realistic data quality report
func generateDataQualityReport(rows int) string {
	var report strings.Builder

	report.WriteString("# Data Quality Analysis: vantage_sites\n\n")
	report.WriteString("## Executive Summary\n")
	report.WriteString("Analysis of vantage_sites table revealed data quality issues requiring attention.\n\n")
	report.WriteString("## Column Analysis\n\n")

	for i := 0; i < rows; i++ {
		report.WriteString(fmt.Sprintf("### Row %d Analysis\n", i+1))
		report.WriteString("- **Completeness**: 98.5%% (15 null values detected)\n")
		report.WriteString("- **Uniqueness**: 100%% (no duplicates found)\n")
		report.WriteString("- **Validity**: 95.2%% (12 invalid format entries)\n")
		report.WriteString("- **Consistency**: 99.1%% (3 cross-reference mismatches)\n")
		report.WriteString("- **Timeliness**: Current (updated within 24 hours)\n\n")
		report.WriteString("**Recommendations**:\n")
		report.WriteString("1. Investigate null values in critical fields\n")
		report.WriteString("2. Implement format validation rules\n")
		report.WriteString("3. Add foreign key constraints\n")
		report.WriteString("4. Set up automated quality monitoring\n\n")
	}

	report.WriteString("## Conclusion\n")
	report.WriteString("The vantage_sites table shows good overall data quality with minor issues.\n")
	report.WriteString("Implementing the recommendations will improve data reliability.\n")

	return report.String()
}
