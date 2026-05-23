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

package plugins

// Plugin is the runtime representation of a LoomPlugin loaded from YAML or the registry.
// Field names mirror the proto definition; the loader converts YAML → Plugin.
type Plugin struct {
	Name        string            `json:"name"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	Author      string            `json:"author,omitempty"`
	Domains     []string          `json:"domains,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`

	Trigger PluginTrigger `json:"trigger"`

	WorkflowRefs []PluginWorkflowRef `json:"workflows,omitempty"`
	SkillRefs    []PluginSkillRef    `json:"skills,omitempty"`
	AgentRefs    []PluginAgentRef    `json:"agents,omitempty"`
	MCPToolRefs  []PluginMCPToolRef  `json:"mcp_tools,omitempty"`

	// Names of skills the plugin requires but does not bundle.
	RequiredSkillNames []string `json:"required_skill_names,omitempty"`
	// Names of MCP tools the plugin requires but does not bundle.
	RequiredToolNames []string `json:"required_tool_names,omitempty"`

	Install            PluginInstallPolicy    `json:"install,omitempty"`
	DefaultBindingMode string                 `json:"default_binding_mode,omitempty"`
	Resolution         PluginResolutionPolicy `json:"resolution,omitempty"`

	// SourcePath is the file path this plugin was loaded from (not serialized).
	SourcePath string `json:"-"`
}

// PluginTrigger is the unified invocation surface. A single slash command or
// keyword match activates all components in the plugin.
type PluginTrigger struct {
	SlashCommands []string `json:"slash_commands,omitempty"`
	Keywords      []string `json:"keywords,omitempty"`
	MinConfidence float64  `json:"min_confidence,omitempty"`
	Description   string   `json:"description,omitempty"`
}

// PluginWorkflowRef references a workflow by name.
type PluginWorkflowRef struct {
	Name        string `json:"name"`
	MinVersion  string `json:"min_version,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
}

// PluginSkillRef references a skill by name. When Synthesize is true and the
// skill is missing, the runtime generates it from Description.
type PluginSkillRef struct {
	Name        string `json:"name"`
	MinVersion  string `json:"min_version,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
	Synthesize  bool   `json:"synthesize,omitempty"`
}

// PluginAgentRef references an agent by ID or name. When Synthesize is true and
// the agent is missing, the runtime creates a minimal agent from Description.
type PluginAgentRef struct {
	ID          string `json:"id"`
	Role        string `json:"role,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
	Synthesize  bool   `json:"synthesize,omitempty"`
}

// PluginMCPToolRef references an MCP tool. Server discovery is automatic when
// ServerName is empty.
type PluginMCPToolRef struct {
	ToolName    string `json:"tool_name"`
	ServerName  string `json:"server_name,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
}

// PluginInstallPolicy controls what gets auto-configured when a plugin is installed.
type PluginInstallPolicy struct {
	AutoRegisterSkills bool   `json:"auto_register_skills,omitempty"`
	AutoConfigureMCP   bool   `json:"auto_configure_mcp,omitempty"`
	CreateDefaultAgent bool   `json:"create_default_agent,omitempty"`
	DefaultAgentName   string `json:"default_agent_name,omitempty"`
}

// PluginResolutionPolicy governs activation-time failures when a referenced
// component cannot be resolved.
type PluginResolutionPolicy struct {
	// OnRequiredMissing controls what happens when a required=true ref is gone.
	// Valid values: "FAIL" (default), "SKIP_WARN", "SKIP_SILENT".
	OnRequiredMissing string `json:"on_required_missing,omitempty"`

	// OnOptionalMissing controls what happens when a required=false ref is gone.
	// Valid values: "FAIL", "SKIP_WARN" (default), "SKIP_SILENT".
	OnOptionalMissing string `json:"on_optional_missing,omitempty"`

	// When true, re-attempt synthesis for synthesize=true refs that were
	// generated at install time but have since been deleted.
	ResynthesizeOnActivation bool `json:"resynthesize_on_activation,omitempty"`
}
