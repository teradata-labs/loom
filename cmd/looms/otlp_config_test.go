// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
)

func loadOTLPTestConfig(t *testing.T, extra string) *Config {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "looms.yaml")
	// A provider that needs no credentials, so Validate reaches the observability checks.
	yaml := "llm:\n  provider: ollama\n  ollama_endpoint: http://127.0.0.1:11434\n  ollama_model: llama3\n" +
		"observability:\n  enabled: true\n  mode: otel\n  otlp_endpoint: http://127.0.0.1:6006/v1/traces\n" + extra
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))
	cfg, err := LoadConfig(cfgPath)
	require.NoError(t, err)
	return cfg
}

func TestLoadConfig_OTLPMaxContentLength(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		cfg := loadOTLPTestConfig(t, "")
		assert.Nil(t, cfg.Observability.OTLPMaxContentLength)
	})
	t.Run("research value", func(t *testing.T) {
		cfg := loadOTLPTestConfig(t, "  otlp_max_content_length: 32768\n")
		require.NotNil(t, cfg.Observability.OTLPMaxContentLength)
		assert.Equal(t, 32768, *cfg.Observability.OTLPMaxContentLength)
	})
	t.Run("zero is unlimited, not unset", func(t *testing.T) {
		cfg := loadOTLPTestConfig(t, "  otlp_max_content_length: 0\n")
		require.NotNil(t, cfg.Observability.OTLPMaxContentLength)
		assert.Equal(t, 0, *cfg.Observability.OTLPMaxContentLength)
	})
	t.Run("valid values pass validation", func(t *testing.T) {
		cfg := loadOTLPTestConfig(t, "  otlp_max_content_length: 32768\n")
		require.NoError(t, cfg.Validate())
	})
	t.Run("negative is rejected", func(t *testing.T) {
		cfg := loadOTLPTestConfig(t, "  otlp_max_content_length: -1\n")
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "otlp_max_content_length")
	})
}

func TestNewOTLPTracer_AppliesMaxContentLength(t *testing.T) {
	t.Cleanup(func() { observability.SetMaxContentLength(observability.DefaultMaxContentLength) })

	shutdown := func(tr *observability.OTelTracer) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tr.Shutdown(ctx)
	}

	limit := 32768
	tr, err := newOTLPTracer(ObservabilityConfig{
		OTLPEndpoint:         "http://127.0.0.1:6006/v1/traces",
		OTLPInsecure:         true,
		OTLPMaxContentLength: &limit,
	}, "looms", "test")
	require.NoError(t, err)
	shutdown(tr)
	assert.Equal(t, 32768, observability.MaxContentLength())

	observability.SetMaxContentLength(observability.DefaultMaxContentLength)
	tr, err = newOTLPTracer(ObservabilityConfig{
		OTLPEndpoint: "http://127.0.0.1:6006/v1/traces",
		OTLPInsecure: true,
	}, "looms", "test")
	require.NoError(t, err)
	shutdown(tr)
	assert.Equal(t, observability.DefaultMaxContentLength, observability.MaxContentLength(), "unset key keeps the default")
}
