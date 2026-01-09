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
package observability

import (
	"fmt"
	"os"

	"go.uber.org/zap"
)

// TracerMode specifies which tracer implementation to use
type TracerMode string

const (
	// TracerModeAuto automatically selects best tracer based on environment
	TracerModeAuto TracerMode = "auto"

	// TracerModeService uses Hawk service (gRPC)
	TracerModeService TracerMode = "service"

	// TracerModeEmbedded uses embedded Hawk storage (memory or SQLite)
	TracerModeEmbedded TracerMode = "embedded"

	// TracerModeNone disables tracing
	TracerModeNone TracerMode = "none"
)

// AutoSelectConfig provides configuration for automatic tracer selection
type AutoSelectConfig struct {
	// Mode: "auto", "service", "embedded", or "none"
	// If "auto", selects based on HawkURL presence and PreferEmbedded flag
	Mode TracerMode

	// PreferEmbedded: When Mode=auto, prefer embedded over service if both are available
	PreferEmbedded bool

	// HawkURL: Service endpoint (for service mode)
	HawkURL string

	// HawkAPIKey: Authentication for service mode
	HawkAPIKey string

	// EmbeddedStorageType: "memory" or "sqlite" (for embedded mode)
	EmbeddedStorageType string

	// EmbeddedSQLitePath: Path to SQLite database (required if EmbeddedStorageType="sqlite")
	EmbeddedSQLitePath string

	// Logger for tracer operations
	Logger *zap.Logger

	// Privacy settings (for service mode)
	Privacy *PrivacyConfig
}

// NewAutoSelectTracer creates a tracer based on auto-selection logic
//
// Selection Priority (when Mode=auto):
// 1. If PreferEmbedded=true: embedded → service → none
// 2. If PreferEmbedded=false: service → embedded → none
//
// Embedded is available when:
// - EmbeddedStorageType is set
// - If SQLite, EmbeddedSQLitePath is provided
//
// Service is available when:
// - HawkURL is set
// - HawkAPIKey is set (optional but recommended)
func NewAutoSelectTracer(config *AutoSelectConfig) (Tracer, error) {
	if config == nil {
		return nil, fmt.Errorf("config required")
	}

	// Handle explicit mode selection
	switch config.Mode {
	case TracerModeService:
		return newServiceTracer(config)
	case TracerModeEmbedded:
		return newEmbeddedTracer(config)
	case TracerModeNone:
		return NewNoOpTracer(), nil
	case TracerModeAuto:
		return autoSelectTracer(config)
	default:
		return nil, fmt.Errorf("unknown tracer mode: %s (supported: auto, service, embedded, none)", config.Mode)
	}
}

// autoSelectTracer implements the auto-selection logic
func autoSelectTracer(config *AutoSelectConfig) (Tracer, error) {
	serviceAvailable := isServiceAvailable(config)
	embeddedAvailable := isEmbeddedAvailable(config)

	logger := config.Logger
	if logger == nil {
		logger, _ = zap.NewProduction()
	}

	// No tracers available
	if !serviceAvailable && !embeddedAvailable {
		logger.Info("no tracer configured, using no-op tracer",
			zap.String("mode", "auto"),
		)
		return NewNoOpTracer(), nil
	}

	// Only embedded available
	if embeddedAvailable && !serviceAvailable {
		logger.Info("auto-selecting embedded tracer",
			zap.String("storage_type", config.EmbeddedStorageType),
			zap.String("reason", "embedded configured, service not available"),
		)
		return newEmbeddedTracer(config)
	}

	// Only service available
	if serviceAvailable && !embeddedAvailable {
		logger.Info("auto-selecting service tracer",
			zap.String("hawk_url", config.HawkURL),
			zap.String("reason", "service configured, embedded not available"),
		)
		return newServiceTracer(config)
	}

	// Both available - use preference
	if config.PreferEmbedded {
		logger.Info("auto-selecting embedded tracer",
			zap.String("storage_type", config.EmbeddedStorageType),
			zap.String("reason", "prefer_embedded=true, both available"),
		)
		return newEmbeddedTracer(config)
	}

	logger.Info("auto-selecting service tracer",
		zap.String("hawk_url", config.HawkURL),
		zap.String("reason", "prefer_embedded=false, both available"),
	)
	return newServiceTracer(config)
}

// isServiceAvailable checks if service mode can be used
func isServiceAvailable(config *AutoSelectConfig) bool {
	return config.HawkURL != ""
}

// isEmbeddedAvailable checks if embedded mode can be used
func isEmbeddedAvailable(config *AutoSelectConfig) bool {
	if config.EmbeddedStorageType == "" {
		return false
	}

	// Memory storage always available
	if config.EmbeddedStorageType == "memory" {
		return true
	}

	// SQLite requires path
	if config.EmbeddedStorageType == "sqlite" {
		return config.EmbeddedSQLitePath != ""
	}

	return false
}

// newServiceTracer creates a Hawk service tracer
func newServiceTracer(config *AutoSelectConfig) (Tracer, error) {
	if config.HawkURL == "" {
		return nil, fmt.Errorf("hawk_url required for service mode")
	}

	hawkConfig := HawkConfig{
		Endpoint: config.HawkURL,
		APIKey:   config.HawkAPIKey,
	}

	if config.Privacy != nil {
		hawkConfig.Privacy = *config.Privacy
	}

	return NewHawkTracer(hawkConfig)
}

// newEmbeddedTracer creates an embedded Hawk tracer
func newEmbeddedTracer(config *AutoSelectConfig) (Tracer, error) {
	if config.EmbeddedStorageType == "" {
		return nil, fmt.Errorf("embedded_storage_type required for embedded mode")
	}

	embeddedConfig := &EmbeddedConfig{
		StorageType: config.EmbeddedStorageType,
		SQLitePath:  config.EmbeddedSQLitePath,
		Logger:      config.Logger,
	}

	if config.EmbeddedStorageType == "sqlite" && config.EmbeddedSQLitePath == "" {
		return nil, fmt.Errorf("embedded_sqlite_path required when storage_type=sqlite")
	}

	return NewEmbeddedHawkTracer(embeddedConfig)
}

// NewAutoSelectTracerFromEnv creates a tracer using environment variables
//
// Environment Variables:
// - LOOM_TRACER_MODE: "auto", "service", "embedded", or "none" (default: "auto")
// - LOOM_TRACER_PREFER_EMBEDDED: "true" or "false" (default: "true")
// - HAWK_URL: Service endpoint (for service mode)
// - HAWK_API_KEY: Service authentication (for service mode)
// - LOOM_EMBEDDED_STORAGE: "memory" or "sqlite" (default: "memory")
// - LOOM_EMBEDDED_SQLITE_PATH: Path to SQLite database (required if storage=sqlite)
//
// Example:
//
//	# Use embedded memory storage (fast, non-persistent)
//	export LOOM_TRACER_MODE=embedded
//	export LOOM_EMBEDDED_STORAGE=memory
//
//	# Use embedded SQLite storage (persistent)
//	export LOOM_TRACER_MODE=embedded
//	export LOOM_EMBEDDED_STORAGE=sqlite
//	export LOOM_EMBEDDED_SQLITE_PATH=/tmp/loom-traces.db
//
//	# Use Hawk service
//	export LOOM_TRACER_MODE=service
//	export HAWK_URL=http://localhost:8090
//	export HAWK_API_KEY=your-key
//
//	# Auto-select (prefer embedded if both available)
//	export LOOM_TRACER_MODE=auto
//	export LOOM_TRACER_PREFER_EMBEDDED=true
//	export LOOM_EMBEDDED_STORAGE=memory
//	export HAWK_URL=http://localhost:8090
func NewAutoSelectTracerFromEnv(logger *zap.Logger) (Tracer, error) {
	config := &AutoSelectConfig{
		Mode:                TracerMode(getEnv("LOOM_TRACER_MODE", "auto")),
		PreferEmbedded:      getEnv("LOOM_TRACER_PREFER_EMBEDDED", "true") == "true",
		HawkURL:             os.Getenv("HAWK_URL"),
		HawkAPIKey:          os.Getenv("HAWK_API_KEY"),
		EmbeddedStorageType: getEnv("LOOM_EMBEDDED_STORAGE", "memory"),
		EmbeddedSQLitePath:  os.Getenv("LOOM_EMBEDDED_SQLITE_PATH"),
		Logger:              logger,
	}

	return NewAutoSelectTracer(config)
}

// getEnv returns environment variable value or default
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
