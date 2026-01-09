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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestNewReferenceStoreFromConfig_Memory(t *testing.T) {
	cfg := FactoryConfig{
		Store: StoreConfig{
			Backend: "memory",
		},
		GC: GCConfig{
			Enabled:  true,
			Strategy: "ref_counting",
			Interval: 60,
		},
	}

	store, err := NewReferenceStoreFromConfig(cfg)
	require.NoError(t, err)
	assert.NotNil(t, store)
	defer store.Close()

	// Verify it's a memory store by checking it works
	ctx := context.Background()
	ref, err := store.Store(ctx, []byte("test"), StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_SESSION_STATE,
	})
	require.NoError(t, err)
	assert.NotNil(t, ref)
}

func TestNewReferenceStoreFromConfig_SQLite(t *testing.T) {
	cfg := FactoryConfig{
		Store: StoreConfig{
			Backend: "sqlite",
			Path:    t.TempDir() + "/test.db",
		},
		GC: GCConfig{
			Enabled:  true,
			Strategy: "ref_counting",
			Interval: 60,
		},
	}

	store, err := NewReferenceStoreFromConfig(cfg)
	require.NoError(t, err)
	assert.NotNil(t, store)
	defer store.Close()

	// Verify it's a SQLite store by checking persistence
	ctx := context.Background()
	ref, err := store.Store(ctx, []byte("test data"), StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_WORKFLOW_CONTEXT,
	})
	require.NoError(t, err)
	assert.NotNil(t, ref)

	// Resolve to verify
	data, err := store.Resolve(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, []byte("test data"), data)
}

func TestNewReferenceStoreFromConfig_UnknownBackend(t *testing.T) {
	cfg := FactoryConfig{
		Store: StoreConfig{
			Backend: "unknown",
		},
	}

	store, err := NewReferenceStoreFromConfig(cfg)
	assert.Error(t, err)
	assert.Nil(t, store)
	assert.Contains(t, err.Error(), "unknown store backend")
}

func TestNewReferenceStoreFromConfig_Redis(t *testing.T) {
	cfg := FactoryConfig{
		Store: StoreConfig{
			Backend:  "redis",
			RedisURL: "redis://localhost:6379/0",
		},
	}

	store, err := NewReferenceStoreFromConfig(cfg)
	assert.Error(t, err)
	assert.Nil(t, store)
	assert.Contains(t, err.Error(), "redis backend not yet implemented")
}

func TestNewReferenceStoreFromConfig_DefaultPath(t *testing.T) {
	cfg := FactoryConfig{
		Store: StoreConfig{
			Backend: "sqlite",
			Path:    "", // Empty path should use default
		},
		GC: GCConfig{
			Enabled:  true,
			Interval: 60,
		},
	}

	// This will create ./loom.db in current directory
	store, err := NewReferenceStoreFromConfig(cfg)
	require.NoError(t, err)
	assert.NotNil(t, store)
	defer store.Close()
}

func TestNewReferenceStoreFromConfig_DefaultGCInterval(t *testing.T) {
	cfg := FactoryConfig{
		Store: StoreConfig{
			Backend: "memory",
		},
		GC: GCConfig{
			Enabled:  true,
			Interval: 0, // Should default to 5 minutes
		},
	}

	store, err := NewReferenceStoreFromConfig(cfg)
	require.NoError(t, err)
	assert.NotNil(t, store)
	defer store.Close()

	// Verify default GC interval by checking it's a MemoryStore
	memStore, ok := store.(*MemoryStore)
	require.True(t, ok)
	assert.Equal(t, 5*time.Minute, memStore.gcInterval)
}

func TestNewPolicyManagerFromConfig(t *testing.T) {
	cfg := FactoryConfig{
		AutoPromote: AutoPromoteConfigParams{
			Enabled:   true,
			Threshold: 5000,
		},
		Policies: PoliciesConfig{
			AlwaysReference: []string{"session_state", "workflow_context"},
			AlwaysValue:     []string{"control"},
		},
	}

	pm := NewPolicyManagerFromConfig(cfg)
	require.NotNil(t, pm)

	// Test auto-promote threshold
	assert.False(t, pm.ShouldUseReference("unknown_type", 4000)) // Below threshold
	assert.True(t, pm.ShouldUseReference("unknown_type", 6000))  // Above threshold

	// Test always-reference policy (Tier 1)
	assert.True(t, pm.ShouldUseReference("session_state", 100))    // Small size, still uses reference
	assert.True(t, pm.ShouldUseReference("workflow_context", 100)) // Small size, still uses reference

	// Test always-value policy (Tier 3)
	assert.False(t, pm.ShouldUseReference("control", 100000)) // Large size, still uses value
}

func TestNewPolicyManagerFromConfig_DefaultThreshold(t *testing.T) {
	cfg := DefaultFactoryConfig()

	pm := NewPolicyManagerFromConfig(cfg)
	require.NotNil(t, pm)

	// Default threshold is 10KB
	assert.False(t, pm.ShouldUseReference("unknown_type", 10000)) // Just below
	assert.True(t, pm.ShouldUseReference("unknown_type", 11000))  // Just above
}

func TestNewPolicyManagerFromConfig_GetPolicy(t *testing.T) {
	cfg := FactoryConfig{
		AutoPromote: AutoPromoteConfigParams{
			Enabled:   true,
			Threshold: 10240,
		},
		Policies: PoliciesConfig{
			AlwaysReference: []string{"session_state"},
			AlwaysValue:     []string{"control"},
		},
	}

	pm := NewPolicyManagerFromConfig(cfg)

	// Test always-reference policy
	policy := pm.GetPolicy("session_state")
	assert.Equal(t, loomv1.CommunicationTier_COMMUNICATION_TIER_ALWAYS_REFERENCE, policy.Tier)
	assert.Equal(t, "session_state", policy.MessageType)

	// Test always-value policy
	policy = pm.GetPolicy("control")
	assert.Equal(t, loomv1.CommunicationTier_COMMUNICATION_TIER_ALWAYS_VALUE, policy.Tier)
	assert.Equal(t, "control", policy.MessageType)

	// Test default policy (auto-promote)
	policy = pm.GetPolicy("unknown_type")
	assert.Equal(t, loomv1.CommunicationTier_COMMUNICATION_TIER_AUTO_PROMOTE, policy.Tier)
	assert.Equal(t, "default", policy.MessageType)
	assert.True(t, policy.AutoPromote.Enabled)
	assert.Equal(t, int64(10240), policy.AutoPromote.ThresholdBytes)
}

func TestDefaultFactoryConfig(t *testing.T) {
	cfg := DefaultFactoryConfig()

	// Verify default values
	assert.Equal(t, "sqlite", cfg.Store.Backend)
	assert.Equal(t, "./loom.db", cfg.Store.Path)
	assert.True(t, cfg.GC.Enabled)
	assert.Equal(t, "ref_counting", cfg.GC.Strategy)
	assert.Equal(t, 300, cfg.GC.Interval)
	assert.True(t, cfg.AutoPromote.Enabled)
	assert.Equal(t, int64(10240), cfg.AutoPromote.Threshold)
	assert.Contains(t, cfg.Policies.AlwaysReference, "session_state")
	assert.Contains(t, cfg.Policies.AlwaysReference, "workflow_context")
	assert.Contains(t, cfg.Policies.AlwaysValue, "control")
}

func TestFactoryConfig_EndToEnd(t *testing.T) {
	cfg := DefaultFactoryConfig()
	cfg.Store.Path = t.TempDir() + "/test.db"

	// Create store
	store, err := NewReferenceStoreFromConfig(cfg)
	require.NoError(t, err)
	defer store.Close()

	// Create policy manager
	pm := NewPolicyManagerFromConfig(cfg)

	// Test integration: Store a session state (Tier 1 - always reference)
	ctx := context.Background()
	sessionData := []byte("session state data")

	// Policy should force reference even for small data
	assert.True(t, pm.ShouldUseReference("session_state", int64(len(sessionData))))

	// Store it
	ref, err := store.Store(ctx, sessionData, StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_SESSION_STATE,
	})
	require.NoError(t, err)

	// Resolve it
	resolved, err := store.Resolve(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, sessionData, resolved)

	// Test auto-promote: Large payload should use reference
	largeData := make([]byte, 20000) // 20KB > 10KB threshold
	assert.True(t, pm.ShouldUseReference("tool_result", int64(len(largeData))))
}
