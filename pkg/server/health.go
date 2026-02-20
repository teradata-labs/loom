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
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/types"
)

// ValidateProviders performs a preflight health check on all configured LLM providers.
// Called during server startup to ensure all providers are reachable before serving requests.
// Returns an error if any provider fails the health check.
func ValidateProviders(ctx context.Context, ag *agent.Agent) error {
	roleLLMs := ag.GetAllRoleLLMs()
	if len(roleLLMs) == 0 {
		return fmt.Errorf("no LLM providers configured")
	}

	var failures []string
	for role, llmProvider := range roleLLMs {
		roleName := llmRoleToString(role)

		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := llmProvider.Chat(checkCtx, []types.Message{
			{Role: "user", Content: "ping"},
		}, nil)
		cancel()

		if err != nil {
			failures = append(failures, fmt.Sprintf("%s (%s/%s): %v",
				roleName, llmProvider.Name(), llmProvider.Model(), err))
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("LLM provider preflight check failed:\n  %s", strings.Join(failures, "\n  "))
	}
	return nil
}
