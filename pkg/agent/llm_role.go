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

import "github.com/teradata-labs/loom/pkg/llm"

// Roles of the LLM calls the agent makes for itself (see llm.RoleAgent).
const (
	roleClassifier         = llm.RoleClassifier
	roleSkillsRouter       = llm.RoleSkillsRouter
	roleMemoryExtract      = llm.RoleMemoryExtract
	roleMemoryQuery        = llm.RoleMemoryQuery
	roleMemoryRerank       = llm.RoleMemoryRerank
	roleMemoryCompress     = llm.RoleMemoryCompress
	roleMemorySearchRerank = llm.RoleMemorySearchRerank
)

// llmForRole returns p tagged with role on its llm.completion spans: a copy of
// an instrumented provider with that role, or p unchanged when it is not
// instrumented (its calls are not traced).
func llmForRole(p LLMProvider, role string) LLMProvider {
	if ip, ok := p.(*llm.InstrumentedProvider); ok {
		return ip.ForRole(role)
	}
	return p
}
