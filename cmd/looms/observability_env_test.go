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
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestApplyOTLPEnvOverrideRequiresExplicitOTLPMode(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://collector:4318/v1/traces")

	obs := ObservabilityConfig{Enabled: true, Mode: "service", HawkEndpoint: "http://hawk"}
	assert.Empty(t, applyOTLPEnvOverride(&obs, zap.NewNop()))
	assert.Equal(t, "service", obs.Mode)
	assert.Equal(t, "", obs.OTLPEndpoint)
}

func TestApplyOTLPEnvOverrideUsesEnvironmentForExplicitOTLPMode(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://collector:4318/v1/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "Authorization=Bearer token")

	obs := ObservabilityConfig{Enabled: true, Mode: "otel", OTLPEndpoint: "http://configured"}
	assert.Equal(t, "http://collector:4318/v1/traces", applyOTLPEnvOverride(&obs, zap.NewNop()))
	assert.Equal(t, "http://collector:4318/v1/traces", obs.OTLPEndpoint)
	assert.Equal(t, "Bearer token", obs.OTLPHeaders["Authorization"])
}

func TestOTLPServiceName(t *testing.T) {
	t.Run("uses command default when unset", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "")
		assert.Equal(t, "looms", otlpServiceName("looms"))
	})

	t.Run("uses OTEL environment override", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "tenant-looms")
		assert.Equal(t, "tenant-looms", otlpServiceName("looms"))
	})
}