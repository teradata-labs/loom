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
package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/types"
	"golang.org/x/sync/errgroup"
)

// providerEntry tracks a unique LLM provider and which agents reference it.
type providerEntry struct {
	provider types.LLMProvider
	agents   []string // agent IDs that use this provider (for diagnostics)
}

// collectUniqueProviders deduplicates providers across all agents by name+model.
// With large deployments (100+ agents sharing the same Bedrock model) this avoids
// sending N identical pings when only one is needed.
func collectUniqueProviders(agents map[string]*agent.Agent) map[string]*providerEntry {
	seen := make(map[string]*providerEntry)
	for agentID, ag := range agents {
		for _, llmProvider := range ag.GetAllRoleLLMs() {
			key := llmProvider.Name() + "/" + llmProvider.Model()
			if seen[key] == nil {
				seen[key] = &providerEntry{provider: llmProvider}
			}
			seen[key].agents = append(seen[key].agents, agentID)
		}
	}
	return seen
}

// ValidateProviders performs a preflight health check on all configured LLM providers.
// It deduplicates providers across agents (many agents often share one provider) and
// runs all checks concurrently, so startup time is bounded by the slowest provider
// rather than O(agents × latency).
//
// Called during server startup to ensure all providers are reachable before serving
// requests. Returns an error if any provider fails the health check.
func ValidateProviders(ctx context.Context, agents map[string]*agent.Agent) error {
	unique := collectUniqueProviders(agents)
	if len(unique) == 0 {
		return fmt.Errorf("no LLM providers configured")
	}

	var (
		mu       sync.Mutex
		failures []string
	)

	g, gCtx := errgroup.WithContext(ctx)
	for key, entry := range unique {
		g.Go(func() error {
			checkCtx, cancel := context.WithTimeout(gCtx, 10*time.Second)
			defer cancel()

			_, err := entry.provider.Chat(checkCtx, []types.Message{
				{Role: "user", Content: "ping"},
			}, nil)
			if err != nil {
				agentList := entry.agents
				sort.Strings(agentList)
				mu.Lock()
				failures = append(failures, fmt.Sprintf("%s (used by: %s): %v",
					key, strings.Join(agentList, ", "), err))
				mu.Unlock()
			}
			return nil // don't abort other checks on individual failure
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}

	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("LLM provider preflight check failed:\n  %s",
			strings.Join(failures, "\n  "))
	}
	return nil
}
