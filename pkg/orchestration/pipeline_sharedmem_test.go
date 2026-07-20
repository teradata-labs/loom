// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"strings"
	"testing"
)

// TestPipelineSharedMemoryHeader verifies the plain pipeline's SharedMemory
// context header lists each prior stage's key and tells the agent how to fetch
// full upstream output (the recovery path when {{previous}} is truncated).
func TestPipelineSharedMemoryHeader(t *testing.T) {
	h := (&PipelineExecutor{}).buildSharedMemoryContextHeader(3)
	for _, key := range []string{"stage-1-output", "stage-2-output", "stage-3-output"} {
		if !strings.Contains(h, key) {
			t.Errorf("header missing key %q:\n%s", key, h)
		}
	}
	if !strings.Contains(h, "shared_memory_read") {
		t.Errorf("header should tell the agent to use shared_memory_read:\n%s", h)
	}
}

// TestPipelineStageTruncationReferencesKey verifies the hybrid hand-off: small
// outputs pass through inline; large ones are truncated and reference the
// SharedMemory key so the next stage can fetch the full text.
func TestPipelineStageTruncationReferencesKey(t *testing.T) {
	small := "a concise 4-claim brief"
	out, truncated := truncateStageOutput(small, MaxStageOutputBytes, "stage-1-output")
	if truncated || out != small {
		t.Errorf("small output must pass through unchanged; got truncated=%v", truncated)
	}

	big := strings.Repeat("x\n", MaxStageOutputBytes) // > MaxStageOutputBytes
	out2, truncated2 := truncateStageOutput(big, MaxStageOutputBytes, "stage-1-output")
	if !truncated2 {
		t.Fatal("large output must be truncated")
	}
	if !strings.Contains(out2, "stage-1-output") || !strings.Contains(out2, "shared_memory_read") {
		t.Errorf("truncated output must reference the SharedMemory key + fetch call")
	}
}
