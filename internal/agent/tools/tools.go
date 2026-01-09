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
// Package tools provides tool types compatible with Crush's interface.
package tools

import "github.com/teradata-labs/loom/internal/session"

// Tool name constants.
const (
	AgenticFetchToolName = "agentic_fetch"
	EditToolName         = "edit"
	WriteToolName        = "write"
	MultiEditToolName    = "multi_edit"
	BashToolName         = "bash"
	DownloadToolName     = "download"
	ReadToolName         = "read"
	FetchToolName        = "fetch"
	ViewToolName         = "view"
	LSToolName           = "ls"
	JobOutputToolName    = "job_output"
	JobKillToolName      = "job_kill"
	WebFetchToolName     = "web_fetch"
	WebSearchToolName    = "web_search"
	GlobToolName         = "glob"
	GrepToolName         = "grep"
	SourcegraphToolName  = "sourcegraph"
	DiagnosticsToolName  = "diagnostics"
	TodosToolName        = "todos"
)

// BashParams contains bash input parameters.
type BashParams struct {
	Command         string `json:"command"`
	Description     string `json:"description,omitempty"`
	Timeout         int    `json:"timeout,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

// BashResponseMetadata contains bash response metadata.
type BashResponseMetadata struct {
	ExitCode    int    `json:"exit_code"`
	Stdout      string `json:"stdout,omitempty"`
	Stderr      string `json:"stderr,omitempty"`
	Output      string `json:"output,omitempty"`
	Background  bool   `json:"background,omitempty"`
	ShellID     string `json:"shell_id,omitempty"`
	Description string `json:"description,omitempty"`
}

// BashNoOutput is the message shown when there's no output.
const BashNoOutput = "(no output)"

// JobOutputParams contains job output parameters.
type JobOutputParams struct {
	ShellID string `json:"shell_id"`
}

// JobOutputResponseMetadata contains job output response metadata.
type JobOutputResponseMetadata struct {
	Output      string `json:"output"`
	Status      string `json:"status"`
	ShellID     string `json:"shell_id"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command,omitempty"`
}

// JobKillParams contains job kill parameters.
type JobKillParams struct {
	ShellID string `json:"shell_id"`
}

// JobKillResponseMetadata contains job kill response metadata.
type JobKillResponseMetadata struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command,omitempty"`
}

// ViewParams contains view parameters.
type ViewParams struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// ViewResponseMetadata contains view response metadata.
type ViewResponseMetadata struct {
	FilePath   string `json:"file_path"`
	TotalLines int    `json:"total_lines"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
	Content    string `json:"content,omitempty"`
}

// EditResponseMetadata contains edit response metadata.
type EditResponseMetadata struct {
	Success    bool   `json:"success"`
	Message    string `json:"message,omitempty"`
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
}

// MultiEditParams contains multi-edit parameters.
type MultiEditParams struct {
	FilePath string       `json:"file_path"`
	Edits    []EditParams `json:"edits"`
}

// MultiEditResponseMetadata contains multi-edit response metadata.
type MultiEditResponseMetadata struct {
	Success      bool   `json:"success"`
	Count        int    `json:"count"`
	Message      string `json:"message,omitempty"`
	OldContent   string `json:"old_content,omitempty"`
	NewContent   string `json:"new_content,omitempty"`
	EditsApplied int    `json:"edits_applied,omitempty"`
	EditsFailed  int    `json:"edits_failed,omitempty"`
}

// FetchParams contains fetch parameters.
type FetchParams struct {
	URL     string `json:"url"`
	Format  string `json:"format,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// DownloadParams contains download parameters.
type DownloadParams struct {
	URL      string `json:"url"`
	FilePath string `json:"file_path"`
	Timeout  int    `json:"timeout,omitempty"`
}

// AgenticFetchParams contains agentic fetch parameters.
type AgenticFetchParams struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt,omitempty"`
}

// WebFetchParams contains web fetch parameters.
type WebFetchParams struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt,omitempty"`
}

// WebSearchParams contains web search parameters.
type WebSearchParams struct {
	Query string `json:"query"`
}

// GlobParams contains glob parameters.
type GlobParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

// GrepParams contains grep parameters.
type GrepParams struct {
	Pattern     string `json:"pattern"`
	Path        string `json:"path,omitempty"`
	Include     string `json:"include,omitempty"`
	LiteralText bool   `json:"literal_text,omitempty"`
}

// LSParams contains ls parameters.
type LSParams struct {
	Path   string   `json:"path"`
	Ignore []string `json:"ignore,omitempty"`
}

// SourcegraphParams contains sourcegraph parameters.
type SourcegraphParams struct {
	Query         string `json:"query"`
	Count         int    `json:"count,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
}

// TodosParams contains todos parameters.
type TodosParams struct {
	Todos []session.Todo `json:"todos"`
}

// TodosResponseMetadata contains todos response metadata.
type TodosResponseMetadata struct {
	Todos         []session.Todo `json:"todos"`
	IsNew         bool           `json:"is_new,omitempty"`
	JustStarted   string         `json:"just_started,omitempty"`   // ID of just-started todo
	JustCompleted []string       `json:"just_completed,omitempty"` // IDs of just-completed todos
	Total         int            `json:"total"`
	Completed     int            `json:"completed"`
}

// EditParams contains edit parameters.
type EditParams struct {
	FilePath  string `json:"file_path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// WriteParams contains write parameters.
type WriteParams struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// BashPermissionsParams contains bash tool parameters.
type BashPermissionsParams struct {
	Command     string
	Description string
	Timeout     int
}

// DownloadPermissionsParams contains download tool parameters.
type DownloadPermissionsParams struct {
	URL      string
	FilePath string
	Timeout  int
}

// EditPermissionsParams contains edit tool parameters.
type EditPermissionsParams struct {
	FilePath   string
	OldString  string
	NewString  string
	OldContent string
	NewContent string
}

// WritePermissionsParams contains write tool parameters.
type WritePermissionsParams struct {
	FilePath   string
	Content    string
	OldContent string
	NewContent string
}

// MultiEditPermissionsParams contains multi-edit tool parameters.
type MultiEditPermissionsParams struct {
	FilePath   string
	Edits      []EditPermissionsParams
	OldContent string
	NewContent string
}

// ViewPermissionsParams contains view tool parameters.
type ViewPermissionsParams struct {
	FilePath string
	Offset   int
	Limit    int
}

// LSPermissionsParams contains ls tool parameters.
type LSPermissionsParams struct {
	Path   string
	Ignore []string
}

// FetchPermissionsParams contains fetch tool parameters.
type FetchPermissionsParams struct {
	URL string
}

// AgenticFetchPermissionsParams contains agentic fetch tool parameters.
type AgenticFetchPermissionsParams struct {
	URL    string
	Query  string
	Prompt string
}
