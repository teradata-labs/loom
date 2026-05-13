// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package templates is the OSS-side registry of curated agent presets and
// workflow templates. The registries are hardcoded Go literals — no database
// tables, no on-disk YAML, no hot-reload — because the contents are part of
// the binary's shipped surface, not user data.
//
// Two registries:
//
//   - presetRegistry: a slice of AgentPresetInfo, one entry per AgentPreset
//     enum value. Each entry packages defaults the server merges into a
//     user-supplied CreateAgentRequest (only zero-value fields are filled,
//     so the caller's preferences always win).
//
//   - workflowTemplateRegistry: a slice of WorkflowTemplateInfo, one entry
//     per WorkflowTemplate enum value. Each template lists the agent slots
//     it creates (each referencing a preset for baseline config plus a
//     curated system prompt) and a fully-realized WorkflowPattern with
//     stage prompts pre-filled. Agent ids are slotted in by the server's
//     CreateWorkflowFromTemplate handler at instantiation time.
//
// The registries are mirrored from the Loom Cloud scaffolding system but
// augmented with OSS-only tools — research_analyst gets parse_document,
// task_automator gets shell_execute + file_read + file_write, etc.
// Cloud's tool-name skew (no web_browse, no shell access) is corrected
// where the OSS catalog provides equivalents.
package templates
