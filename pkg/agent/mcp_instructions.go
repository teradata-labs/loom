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
	"go.uber.org/zap"
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

	// The string is server-controlled and lands in the ROM prompt slot,
	// which pressure relief never sheds: unbounded input would let one
	// hostile or buggy server starve every session's context permanently.
	// The cap is generous — real-world instructions are well under 2 KiB.
	trimmed := strings.TrimSpace(instructions)
	if len(trimmed) > maxMCPInstructionsBytes {
		zap.L().Warn("MCP server instructions truncated",
			zap.String("server", serverName),
			zap.Int("bytes", len(trimmed)),
			zap.Int("cap", maxMCPInstructionsBytes))
		trimmed = trimmed[:maxMCPInstructionsBytes] + "\n[instructions truncated by loom: size cap]"
	}
	// Escape line-start markdown headers so one server cannot fabricate a
	// section attributed to another server (or to loom's own supplements);
	// the rendered header below is the only unescaped one per server.
	trimmed = escapeLineStartHeaders(trimmed)

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.mcpServerInstructions == nil {
		a.mcpServerInstructions = make(map[string]string)
	}
	if _, exists := a.mcpServerInstructions[serverName]; exists {
		return
	}
	a.mcpServerInstructions[serverName] = trimmed
}

// maxMCPInstructionsBytes bounds one server's instructions contribution to
// the system prompt (ROM is unsheddable; see attachMCPServerInstructions).
const maxMCPInstructionsBytes = 8 * 1024

// escapeLineStartHeaders backslash-escapes markdown ATX headers at line
// starts so server-supplied text cannot spoof section headers. The body text
// stays readable to the LLM; only the header syntax is neutralized.
func escapeLineStartHeaders(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		t := strings.TrimLeft(line, " ")
		if strings.HasPrefix(t, "#") {
			lines[i] = "\\" + strings.TrimLeft(line, " ")
		}
	}
	return strings.Join(lines, "\n")
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
