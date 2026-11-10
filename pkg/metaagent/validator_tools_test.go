// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package metaagent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate_IntentionalDisabledToolsDoesNotWarn(t *testing.T) {
	for _, tt := range []struct {
		name     string
		tools    string
		wantWarn bool
	}{
		{"omitted", "", true},
		{"disabled", "  tools:\n    disabled: true\n", false},
		{"enabled without tools", "  tools:\n    disabled: false\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			yaml := "agent:\n  name: test-agent\n  system_prompt: Analyze the input and return a concise answer with supporting evidence.\n  llm:\n    provider: anthropic\n    model: test-model\n" + tt.tools
			result, err := NewValidator(nil).Validate(context.Background(), yaml)
			require.NoError(t, err)
			require.True(t, result.Valid, "errors: %v", result.Errors)
			warned := false
			for _, warning := range result.Warnings {
				if warning.Field == "agent.tools" {
					warned = true
				}
			}
			assert.Equal(t, tt.wantWarn, warned)
		})
	}
}
