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
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// The agent-LLM resolution in runServe is reached only from a running server
// (the cold-start loop, the hot-reload callback, the registry's runtime
// create), so no unit test can call it. These source checks pin the wiring:
// each failure they catch is silent at runtime, an agent quietly running on
// the server default LLM or on env-only credentials.
func readServeSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(".", "cmd_serve.go"))
	require.NoError(t, err)
	return string(src)
}

// The hot-reload callback must resolve the reloaded agent's LLMs through
// resolveReloadLLMs and build the agent on that result, never on the captured
// server default.
func TestServe_HotReloadResolvesTheAgentsOwnLLMs(t *testing.T) {
	text := readServeSource(t)
	cb := regexp.MustCompile(`(?s)registry\.SetReloadCallback\(func\(.*?\n\t\t\}\)\n`).FindString(text)
	require.NotEmpty(t, cb, "expected serve to set a reload callback")
	require.Regexp(t, `resolveAgentLLMs\(agentConfig, llmProvider, config, logger\)`, cb,
		"the reload callback must resolve the agent's own llm and role LLMs")
	require.Regexp(t, `reloadLLMs\.instrumented\(tracer\)`, cb)
	require.Regexp(t, `agentOpts = append\(agentOpts, roleOpts\.\.\.\)`, cb, "role LLMs must be installed on the reloaded agent")
	require.Regexp(t, `agent\.NewAgent\(backend, reloadLLM, `, cb,
		"the reloaded agent must run on the resolved LLM")
	require.NotRegexp(t, `agent\.NewAgent\(backend, llmProvider, `, cb,
		"building a reloaded agent on the server default drops its own llm block")
	require.NotRegexp(t, `\n\t+llmProvider = llm\.NewInstrumentedProvider\(llmProvider`, cb,
		"re-wrapping the captured default adds a tracing layer on every reload")
	require.Regexp(t, `backend\.Close\(\)`, cb, "a refused reload must close the backend it opened")
}

// The cold-start loop resolves each agent's LLMs through the same function as
// reload and installs the role LLMs, judge and orchestrator included.
func TestServe_ColdStartResolvesTheAgentsOwnLLMs(t *testing.T) {
	text := readServeSource(t)
	require.Regexp(t, `agentLLMs, err := resolveAgentLLMs\(cfg, llmProvider, config, logger\)`, text)
	require.Regexp(t, `agentLLMProvider, staticClassifierLLM, roleOpts := agentLLMs\.instrumented\(tracer\)`, text)
	require.Regexp(t, `ag := agent\.NewAgent\(backend, agentLLMProvider, agentOpts\.\.\.\)`, text,
		"the cold-start agent must run on its resolved LLM, not the server default")
	require.NotRegexp(t, `ag := agent\.NewAgent\(backend, llmProvider, `, text)
}

// The registry builds runtime-created agents with serve's builder, so server
// credentials, endpoints and gateway routing apply to them.
func TestServe_RegistryUsesServesLLMBuilder(t *testing.T) {
	text := readServeSource(t)
	reg := regexp.MustCompile(`(?s)agent\.NewRegistry\(agent\.RegistryConfig\{.*?\n\t\}\)`).FindString(text)
	require.NotEmpty(t, reg)
	require.Regexp(t, `(?s)LLMProviderBuilder: func\(c \*loomv1\.LLMConfig\) \(agent\.LLMProvider, error\) \{\s*return createLLMProviderFromProtoConfig\(c, runtimeAgentLLMConfig\(config\), logger\)`, reg)
}
