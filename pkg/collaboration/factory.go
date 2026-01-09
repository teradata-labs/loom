// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"

	"github.com/teradata-labs/loom/pkg/agent"
)

// AgentFactory creates ephemeral agents on-demand for collaboration patterns.
// This enables runtime agent creation for roles like judges, moderators, specialists.
type AgentFactory interface {
	// CreateEphemeralAgent creates a new agent with the specified role/capabilities.
	// The returned agent should be closed after use to release resources.
	CreateEphemeralAgent(ctx context.Context, role string) (*agent.Agent, error)
}

// AgentProviderWithFactory combines AgentProvider with optional factory capabilities.
// If the provider implements AgentFactory, orchestrators can create ephemeral agents.
// Otherwise, they fall back to requiring pre-registered agents.
type AgentProviderWithFactory interface {
	AgentProvider
	AgentFactory
}

// SupportsEphemeralAgents checks if the provider can create ephemeral agents.
func SupportsEphemeralAgents(provider AgentProvider) bool {
	_, ok := provider.(AgentFactory)
	return ok
}
