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
package communication

import (
	"fmt"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// StoreConfig holds reference store configuration (mirrors cmd/looms config).
type StoreConfig struct {
	Backend  string // memory | sqlite | redis
	Path     string // For sqlite
	RedisURL string // For redis
}

// GCConfig holds garbage collection configuration.
type GCConfig struct {
	Enabled  bool
	Strategy string // ref_counting | ttl | manual
	Interval int    // seconds
}

// AutoPromoteConfigParams holds auto-promotion configuration.
type AutoPromoteConfigParams struct {
	Enabled   bool
	Threshold int64 // bytes
}

// PoliciesConfig holds policy overrides.
type PoliciesConfig struct {
	AlwaysReference []string // Force Tier 1 (always reference)
	AlwaysValue     []string // Force Tier 3 (always value)
}

// FactoryConfig holds all communication configuration for factory initialization.
type FactoryConfig struct {
	Store       StoreConfig
	GC          GCConfig
	AutoPromote AutoPromoteConfigParams
	Policies    PoliciesConfig
}

// NewReferenceStoreFromConfig creates a ReferenceStore based on configuration.
func NewReferenceStoreFromConfig(cfg FactoryConfig) (ReferenceStore, error) {
	gcInterval := time.Duration(cfg.GC.Interval) * time.Second
	if gcInterval == 0 {
		gcInterval = 5 * time.Minute // Default
	}

	switch cfg.Store.Backend {
	case "memory":
		return NewMemoryStore(gcInterval), nil

	case "sqlite":
		path := cfg.Store.Path
		if path == "" {
			path = "./loom.db" // Default
		}
		return NewSQLiteStore(path, gcInterval)

	case "redis":
		// Redis support placeholder (not tested)
		return nil, fmt.Errorf("redis backend not yet implemented")

	default:
		return nil, fmt.Errorf("unknown store backend: %s (supported: memory, sqlite, redis)", cfg.Store.Backend)
	}
}

// NewPolicyManagerFromConfig creates a PolicyManager based on configuration.
func NewPolicyManagerFromConfig(cfg FactoryConfig) *PolicyManager {
	pm := NewPolicyManager()

	// Configure auto-promote threshold
	pm.defaultPolicy.AutoPromote = &loomv1.AutoPromoteConfig{
		Enabled:        cfg.AutoPromote.Enabled,
		ThresholdBytes: cfg.AutoPromote.Threshold,
	}

	// Register always-reference policies (Tier 1)
	for _, msgType := range cfg.Policies.AlwaysReference {
		pm.SetPolicy(msgType, &loomv1.CommunicationPolicy{
			Tier:        loomv1.CommunicationTier_COMMUNICATION_TIER_ALWAYS_REFERENCE,
			MessageType: msgType,
			AutoPromote: &loomv1.AutoPromoteConfig{
				Enabled:        false,
				ThresholdBytes: 0,
			},
			Overrides: make(map[string]*loomv1.PolicyOverride),
		})
	}

	// Register always-value policies (Tier 3)
	for _, msgType := range cfg.Policies.AlwaysValue {
		pm.SetPolicy(msgType, &loomv1.CommunicationPolicy{
			Tier:        loomv1.CommunicationTier_COMMUNICATION_TIER_ALWAYS_VALUE,
			MessageType: msgType,
			AutoPromote: &loomv1.AutoPromoteConfig{
				Enabled:        false,
				ThresholdBytes: 0,
			},
			Overrides: make(map[string]*loomv1.PolicyOverride),
		})
	}

	return pm
}

// DefaultFactoryConfig returns default configuration for testing/development.
func DefaultFactoryConfig() FactoryConfig {
	return FactoryConfig{
		Store: StoreConfig{
			Backend: "sqlite",
			Path:    "./loom.db",
		},
		GC: GCConfig{
			Enabled:  true,
			Strategy: "ref_counting",
			Interval: 300, // 5 minutes
		},
		AutoPromote: AutoPromoteConfigParams{
			Enabled:   true,
			Threshold: 10240, // 10KB
		},
		Policies: PoliciesConfig{
			AlwaysReference: []string{"session_state", "workflow_context", "collaboration_state"},
			AlwaysValue:     []string{"control", "pattern_ref"},
		},
	}
}
