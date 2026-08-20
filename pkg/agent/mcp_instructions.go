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
package agent

import (
	"fmt"
	"sort"
	"strings"
)

// attachMCPServerInstructions records the server-level usage guidance an MCP
// server sent at initialize (InitializeResult.instructions), so it can be
// rendered into the agent's system prompt. The server owns its usage contract;
// surfacing it here means agents follow server guidance without any per-agent
// prompt engineering, and guidance updates ship with the server (issue #336).
//
// Set-once per server: the guidance is part of the initialize handshake and
// does not change for the life of the connection. Empty instructions are a
// no-op. Safe for concurrent use.
func (a *Agent) attachMCPServerInstructions(serverName, instructions string) {
	if serverName == "" || strings.TrimSpace(instructions) == "" {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.mcpServerInstructions == nil {
		a.mcpServerInstructions = make(map[string]string)
	}
	if _, exists := a.mcpServerInstructions[serverName]; exists {
		return
	}
	a.mcpServerInstructions[serverName] = strings.TrimSpace(instructions)
}

// mcpInstructionsPromptSupplement renders the usage guidance of every MCP
// server whose tools this agent registered, one block per server in
// deterministic (sorted) order so the rendered prompt is byte-stable for a
// fixed server set. Empty when no registered server sent instructions.
func (a *Agent) mcpInstructionsPromptSupplement() string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if len(a.mcpServerInstructions) == 0 {
		return ""
	}

	servers := make([]string, 0, len(a.mcpServerInstructions))
	for name := range a.mcpServerInstructions {
		servers = append(servers, name)
	}
	sort.Strings(servers)

	var b strings.Builder
	for _, name := range servers {
		b.WriteString(fmt.Sprintf("\n\n## Instructions from MCP server %q\n\n", name))
		b.WriteString(a.mcpServerInstructions[name])
	}
	return b.String()
}
