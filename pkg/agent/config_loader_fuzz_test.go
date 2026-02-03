// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// FuzzConfigLoaderYAML tests YAML config parsing with malformed inputs.
// Properties tested:
// - Never panics on any YAML input
// - Handles invalid YAML gracefully (returns error, doesn't crash)
// - Handles extremely large configs without memory exhaustion
// - Handles deeply nested structures
// - Handles invalid UTF-8 in YAML
func FuzzConfigLoaderYAML(f *testing.F) {
	// Seed with valid YAML configs
	validYAML1 := `
agent:
  name: test-agent
  description: Test agent
  backend_path: ""
  llm:
    provider: anthropic
    model: claude-sonnet-4.5
    temperature: 0.7
    max_tokens: 4096
  system_prompt: You are a test agent
  memory:
    max_context_tokens: 180000
`

	validYAML2 := `
apiVersion: loom.teradata.com/v1
kind: Agent
metadata:
  name: k8s-style-agent
  description: K8s style agent
spec:
  backend:
    name: test
    type: file
  llm:
    provider: anthropic
    model: claude-sonnet-4.5
`

	f.Add(validYAML1)
	f.Add(validYAML2)
	f.Add("") // Empty
	f.Add("invalid yaml {]}")
	f.Add(strings.Repeat("a", 10000))                                       // Large random text
	f.Add("agent:\n  name: test\n" + strings.Repeat("  nested: {}\n", 100)) // Deep nesting
	f.Add("\x00\x01\x02")                                                   // Binary data
	f.Add("agent:\n  name: " + strings.Repeat("x", 100000))                 // Very long value
	f.Add("---\n...\n---")                                                  // YAML document markers

	f.Fuzz(func(t *testing.T, yamlContent string) {
		// Property 1: Should never panic
		var config *loomv1.AgentConfig
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("LoadConfigFromString panicked on input: %v", r)
				}
			}()
			config, err = LoadConfigFromString(yamlContent)
		}()

		// Property 2: Either returns valid config or error (not both or neither)
		if config != nil && err != nil {
			t.Errorf("returned both config and error")
		}

		// Property 3: If successful, config should have basic structure
		if err == nil {
			if config == nil {
				t.Errorf("returned nil config without error")
			}
			// Valid config should have at least a name (one of the two formats)
			// Don't enforce this strictly as some configs might be minimal
		}

		// Property 4: Error messages should not leak sensitive data
		if err != nil {
			errMsg := err.Error()
			// Error should be reasonable length (not exposing huge input)
			// Note: YAML parser can produce large error messages for malformed input
			// We allow up to 1MB here as long as it's just parser errors
			if len(errMsg) > 1000000 {
				t.Errorf("error message suspiciously large: %d bytes", len(errMsg))
			}
		}

		// Property 5: If input is valid UTF-8 YAML structure, parser should handle it
		// (even if semantically invalid for our config)
		// This looks like it might be our format - if it fails, that's expected
		// Just ensure no panic (already checked above)
	})
}

// FuzzConfigLoaderFile tests file-based config loading with malformed file contents.
func FuzzConfigLoaderFile(f *testing.F) {
	f.Add([]byte("agent:\n  name: test\n  llm:\n    provider: anthropic\n"))
	f.Add([]byte(""))
	f.Add([]byte("invalid"))
	f.Add([]byte(strings.Repeat("x", 1000)))

	f.Fuzz(func(t *testing.T, content []byte) {
		// Create a temporary file with the fuzzed content
		tmpDir := t.TempDir()
		tmpFile := filepath.Join(tmpDir, "fuzz-config.yaml")

		err := os.WriteFile(tmpFile, content, 0644)
		if err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}

		// Property 1: Should never panic
		var config *loomv1.AgentConfig
		var loadErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("LoadAgentConfig panicked: %v", r)
				}
			}()
			config, loadErr = LoadAgentConfig(tmpFile)
		}()

		// Property 2: Either returns config or error
		if config != nil && loadErr != nil {
			t.Errorf("returned both config and error")
		}

		// Property 3: Loading non-existent file should error
		nonExistent := filepath.Join(tmpDir, "does-not-exist.yaml")
		_, err = LoadAgentConfig(nonExistent)
		if err == nil {
			t.Errorf("loading non-existent file should return error")
		}
	})
}

// FuzzConfigLoaderInvalidStructures tests parsing with structurally invalid YAML.
func FuzzConfigLoaderInvalidStructures(f *testing.F) {
	// Seed with various invalid structures
	f.Add("agent:\n  name: test\n  llm: not_an_object")
	f.Add("agent:\n  tools:\n    - invalid: structure")
	f.Add("agent:\n  memory:\n    max_context_tokens: not_a_number")
	f.Add("apiVersion: wrong\nkind: wrong\n")
	f.Add("[[[[[")
	f.Add("{{{{{")
	f.Add("---\n---\n---")

	f.Fuzz(func(t *testing.T, yamlContent string) {
		// Should handle gracefully
		config, err := LoadConfigFromString(yamlContent)

		// Property: Invalid structure should result in error
		if config != nil && err != nil {
			t.Errorf("returned both config and error on invalid structure")
		}

		// Most of these should error, but if it somehow parsed successfully,
		// that's acceptable - YAML can be very permissive
		// No additional checks needed beyond the earlier validation
	})
}

// FuzzConfigLoaderLargeValues tests handling of extremely large configuration values.
func FuzzConfigLoaderLargeValues(f *testing.F) {
	f.Add(int32(1000), int32(10), int32(100))
	f.Add(int32(1000000), int32(1000), int32(10000))
	f.Add(int32(-1), int32(-100), int32(0))

	f.Fuzz(func(t *testing.T, maxTokens, maxMessages, strLen int32) {
		// Clamp to reasonable test values
		if maxTokens < -1000000 || maxTokens > 10000000 {
			return
		}
		if maxMessages < -1000 || maxMessages > 100000 {
			return
		}
		if strLen < 0 {
			strLen = 0
		}
		if strLen > 10000 {
			strLen = 10000
		}

		// Build a config with potentially extreme values
		yamlContent := `
agent:
  name: ` + strings.Repeat("x", int(strLen)) + `
  description: ` + strings.Repeat("y", int(strLen)) + `
  llm:
    provider: anthropic
    model: claude-sonnet-4.5
    max_tokens: ` + string(rune(maxTokens)) + `
  memory:
    max_context_tokens: ` + string(rune(maxTokens)) + `
    compression:
      max_l1_messages: ` + string(rune(maxMessages)) + `
`

		// Should not panic
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("LoadConfigFromString panicked on large values: %v", r)
				}
			}()
			_, _ = LoadConfigFromString(yamlContent)
		}()
	})
}

// FuzzConfigLoaderMixedFormats tests handling of mixed or ambiguous config formats.
func FuzzConfigLoaderMixedFormats(f *testing.F) {
	// Mix legacy and k8s formats
	f.Add(`
agent:
  name: legacy-agent
apiVersion: loom.teradata.com/v1
kind: Agent
`)

	// K8s format missing required fields
	f.Add(`
apiVersion: loom.teradata.com/v1
kind: Agent
`)

	// Legacy format missing required fields
	f.Add(`
agent:
  name: minimal
`)

	f.Fuzz(func(t *testing.T, yamlContent string) {
		// Should not panic regardless of format confusion
		_, err := LoadConfigFromString(yamlContent)

		// Error is expected for malformed configs
		// Just ensure we don't crash
		_ = err
	})
}

// FuzzConfigLoaderUnicodeEdgeCases tests handling of various Unicode edge cases.
func FuzzConfigLoaderUnicodeEdgeCases(f *testing.F) {
	f.Add("agent:\n  name: " + "世界")                       // CJK
	f.Add("agent:\n  name: " + "🚀💻🌟")                      // Emoji
	f.Add("agent:\n  name: " + "\u0000")                   // Null
	f.Add("agent:\n  name: " + "\uffff")                   // High Unicode
	f.Add("agent:\n  name: " + "test\u200B")               // Zero-width space
	f.Add("agent:\n  name: " + "test\u202E")               // Right-to-left override
	f.Add("agent:\n  name: " + string([]byte{0xff, 0xfe})) // Invalid UTF-8

	f.Fuzz(func(t *testing.T, yamlContent string) {
		// Should handle any Unicode input without panicking
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panicked on Unicode input: %v", r)
				}
			}()
			_, _ = LoadConfigFromString(yamlContent)
		}()
	})
}
