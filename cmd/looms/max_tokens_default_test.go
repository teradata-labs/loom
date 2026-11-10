// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMaxTokens_DefaultsRemain4096(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want int
	}{
		{
			name: "unset",
			yaml: "llm:\n  provider: anthropic\n  anthropic_model: claude-sonnet-4-5-20250929\n",
			want: 4096,
		},
		{
			name: "explicit override",
			yaml: "llm:\n  provider: anthropic\n  max_tokens: 8192\n",
			want: 8192,
		},
		{
			name: "generated example",
			yaml: GenerateExampleConfig(),
			want: 4096,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)
			t.Setenv("LOOM_LLM_MAX_TOKENS", "")
			path := filepath.Join(t.TempDir(), "looms.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tt.yaml), 0600))
			cfg, err := LoadConfig(path)
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.LLM.MaxTokens)
		})
	}
}
