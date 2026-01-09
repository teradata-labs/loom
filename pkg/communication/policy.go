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
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// PolicyManager applies communication tier policies
type PolicyManager struct {
	// Default policy for messages without explicit policy
	defaultPolicy *loomv1.CommunicationPolicy

	// Message type to policy mapping
	policies map[string]*loomv1.CommunicationPolicy
}

// NewPolicyManager creates a policy manager with default configuration
func NewPolicyManager() *PolicyManager {
	return &PolicyManager{
		defaultPolicy: DefaultPolicy(),
		policies:      make(map[string]*loomv1.CommunicationPolicy),
	}
}

// DefaultPolicy returns the default communication policy
func DefaultPolicy() *loomv1.CommunicationPolicy {
	return &loomv1.CommunicationPolicy{
		Tier:        loomv1.CommunicationTier_COMMUNICATION_TIER_AUTO_PROMOTE,
		MessageType: "default",
		AutoPromote: &loomv1.AutoPromoteConfig{
			Enabled:        true,
			ThresholdBytes: 1024, // 1KB default threshold
		},
		Overrides: make(map[string]*loomv1.PolicyOverride),
	}
}

// SetPolicy registers a policy for a specific message type
func (pm *PolicyManager) SetPolicy(messageType string, policy *loomv1.CommunicationPolicy) {
	pm.policies[messageType] = policy
}

// GetPolicy retrieves the policy for a message type, or default if not found
func (pm *PolicyManager) GetPolicy(messageType string) *loomv1.CommunicationPolicy {
	if policy, ok := pm.policies[messageType]; ok {
		return policy
	}
	return pm.defaultPolicy
}

// ShouldUseReference determines if a message should use reference-based communication
func (pm *PolicyManager) ShouldUseReference(messageType string, payloadSize int64) bool {
	policy := pm.GetPolicy(messageType)

	// Check for policy override first
	if override, ok := policy.Overrides[messageType]; ok {
		return override.Type == loomv1.PolicyOverride_OVERRIDE_TYPE_FORCE_REFERENCE
	}

	// Apply tier-based logic
	switch policy.Tier {
	case loomv1.CommunicationTier_COMMUNICATION_TIER_ALWAYS_REFERENCE:
		// Tier 1: Always use reference (shared mutable state, workflow context)
		return true

	case loomv1.CommunicationTier_COMMUNICATION_TIER_AUTO_PROMOTE:
		// Tier 2: Auto-promote based on size
		if policy.AutoPromote != nil && policy.AutoPromote.Enabled {
			return payloadSize > policy.AutoPromote.ThresholdBytes
		}
		// Fallback: use reference if >1KB
		return payloadSize > 1024

	case loomv1.CommunicationTier_COMMUNICATION_TIER_ALWAYS_VALUE:
		// Tier 3: Always use value (control messages, pattern refs, config)
		return false

	default:
		// Unspecified tier: apply auto-promote logic
		if policy.AutoPromote != nil && policy.AutoPromote.Enabled {
			return payloadSize > policy.AutoPromote.ThresholdBytes
		}
		return payloadSize > 1024
	}
}

// NewSessionStatePolicy creates a policy for session state (Tier 1: Always Reference)
func NewSessionStatePolicy() *loomv1.CommunicationPolicy {
	return &loomv1.CommunicationPolicy{
		Tier:        loomv1.CommunicationTier_COMMUNICATION_TIER_ALWAYS_REFERENCE,
		MessageType: "session_state",
		AutoPromote: &loomv1.AutoPromoteConfig{
			Enabled:        false, // Not applicable for Tier 1
			ThresholdBytes: 0,
		},
		Overrides: make(map[string]*loomv1.PolicyOverride),
	}
}

// NewWorkflowContextPolicy creates a policy for workflow context (Tier 1: Always Reference)
func NewWorkflowContextPolicy() *loomv1.CommunicationPolicy {
	return &loomv1.CommunicationPolicy{
		Tier:        loomv1.CommunicationTier_COMMUNICATION_TIER_ALWAYS_REFERENCE,
		MessageType: "workflow_context",
		AutoPromote: &loomv1.AutoPromoteConfig{
			Enabled:        false,
			ThresholdBytes: 0,
		},
		Overrides: make(map[string]*loomv1.PolicyOverride),
	}
}

// NewControlMessagePolicy creates a policy for control messages (Tier 3: Always Value)
func NewControlMessagePolicy() *loomv1.CommunicationPolicy {
	return &loomv1.CommunicationPolicy{
		Tier:        loomv1.CommunicationTier_COMMUNICATION_TIER_ALWAYS_VALUE,
		MessageType: "control",
		AutoPromote: &loomv1.AutoPromoteConfig{
			Enabled:        false, // Not applicable for Tier 3
			ThresholdBytes: 0,
		},
		Overrides: make(map[string]*loomv1.PolicyOverride),
	}
}

// NewToolResultPolicy creates a policy for tool results (Tier 2: Auto-Promote)
func NewToolResultPolicy(thresholdBytes int64) *loomv1.CommunicationPolicy {
	if thresholdBytes == 0 {
		thresholdBytes = 1024 // 1KB default
	}

	return &loomv1.CommunicationPolicy{
		Tier:        loomv1.CommunicationTier_COMMUNICATION_TIER_AUTO_PROMOTE,
		MessageType: "tool_result",
		AutoPromote: &loomv1.AutoPromoteConfig{
			Enabled:        true,
			ThresholdBytes: thresholdBytes,
		},
		Overrides: make(map[string]*loomv1.PolicyOverride),
	}
}
