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

// Package store defines saved Lua scripts and the interface hosts implement
// to keep them. A saved script runs with run_lua by name and, once published
// and attached to an agent, as a tool named lua_<name>.
//
// Loom's implementation is the file store in package filestore. Other hosts
// (Tera) keep scripts in their own database behind the same interface.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teradata-labs/loom/pkg/luasandbox"
)

// Size and shape bounds for saved scripts.
const (
	// MaxDescriptionBytes bounds a script's description, which becomes the
	// description of its lua_<name> tool.
	MaxDescriptionBytes = 1024
	// MaxManifestParameters bounds the properties of manifest.parameters.
	MaxManifestParameters = 20
	// MaxManifestParametersBytes bounds manifest.parameters as JSON.
	MaxManifestParametersBytes = 4 << 10
	// MaxManifestRequires bounds manifest.requires.
	MaxManifestRequires = 100
	// MaxManifestReturnsBytes bounds manifest.returns.
	MaxManifestReturnsBytes = 200
)

// Errors returned by ScriptStore implementations. Wrap them; callers test
// with errors.Is.
var (
	ErrNotFound     = errors.New("lua script not found")
	ErrDuplicate    = errors.New("a lua script with this name already exists")
	ErrInvalid      = errors.New("invalid lua script")
	ErrNotPublished = errors.New("lua script is not published")
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,40}$`)

// Manifest describes a script's inputs and needs. It is required before a
// script can be published as a tool.
type Manifest struct {
	// Parameters is the JSON Schema (type object) for args when the script
	// runs as a tool.
	Parameters map[string]any `json:"parameters,omitempty"`
	// Requires lists the tools the script calls. nil means no restriction;
	// a non-nil empty list means the script may call no tool at all.
	Requires []string `json:"requires"`
	// Returns is one line describing the return value.
	Returns string `json:"returns,omitempty"`
}

// Script is a saved script.
type Script struct {
	Name        string
	Description string
	Source      string
	Manifest    *Manifest // nil when not provided
	Version     int
	Published   bool
	// Owner is who saved it: an agent name in loom ("server" for RPC saves),
	// a user id in Tera.
	Owner string
	// Trust is set by GetForRunner for the caller; it is not stored.
	Trust     luasandbox.Trust
	UpdatedAt time.Time
}

// ScriptStore keeps saved scripts and their per-agent attachments.
// Implementations must be safe for concurrent use and must never hold a lock
// while a script runs.
type ScriptStore interface {
	// Save validates s (ValidateScript) and stores it. With overwrite false,
	// an existing name is ErrDuplicate. It returns the stored script with
	// its new version, and whether the name was new.
	Save(ctx context.Context, s Script, overwrite bool) (Script, bool, error)
	// Get returns a script by name, or ErrNotFound.
	Get(ctx context.Context, name string) (Script, error)
	// GetForRunner returns a script the caller may run, with Trust set for
	// the caller, or ErrNotFound.
	GetForRunner(ctx context.Context, nameOrID string) (Script, error)
	// List returns every script, name-sorted.
	List(ctx context.Context) ([]Script, error)
	// Delete removes a script and every attachment of it, or ErrNotFound.
	Delete(ctx context.Context, name string) error
	// SetPublished sets the script-level published flag.
	SetPublished(ctx context.Context, name string, published bool) error
	// Attach adds lua_<name> to agent. The script must be published
	// (ErrNotPublished). Attaching twice is a no-op.
	Attach(ctx context.Context, agent, name string) error
	// Detach removes lua_<name> from agent only. Detaching what is not
	// attached is a no-op.
	Detach(ctx context.Context, agent, name string) error
	// Attachments returns the script names attached to agent, sorted.
	Attachments(ctx context.Context, agent string) ([]string, error)
	// AttachedAgents returns the agents name is attached to, sorted.
	AttachedAgents(ctx context.Context, name string) ([]string, error)
}

// ToolName is the tool a published script becomes.
func ToolName(scriptName string) string { return "lua_" + scriptName }

// ValidateName reports whether name is a valid script name.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%w: name %q must match %s", ErrInvalid, name, namePattern.String())
	}
	return nil
}

// ValidateScript checks everything Save requires: the name, a description,
// a source that compiles under lim (it is never run), and the manifest.
func ValidateScript(s Script, lim luasandbox.Limits) error {
	if err := ValidateName(s.Name); err != nil {
		return err
	}
	desc := strings.TrimSpace(s.Description)
	switch {
	case desc == "":
		return fmt.Errorf("%w: a description is required", ErrInvalid)
	case len(desc) > MaxDescriptionBytes:
		return fmt.Errorf("%w: the description is %d bytes; the limit is %d", ErrInvalid, len(desc), MaxDescriptionBytes)
	case !utf8.ValidString(s.Description):
		return fmt.Errorf("%w: the description is not valid UTF-8", ErrInvalid)
	}
	if strings.TrimSpace(s.Source) == "" {
		return fmt.Errorf("%w: the script source is empty", ErrInvalid)
	}
	if err := luasandbox.Check(s.Name, s.Source, lim); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if s.Manifest != nil {
		if err := s.Manifest.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks the manifest's shape.
func (m *Manifest) Validate() error {
	if m == nil {
		return nil
	}
	if m.Parameters != nil {
		if t, _ := m.Parameters["type"].(string); t != "object" {
			return fmt.Errorf("%w: manifest.parameters must be a JSON Schema with type \"object\"", ErrInvalid)
		}
		if props, ok := m.Parameters["properties"]; ok {
			pm, ok := props.(map[string]any)
			if !ok {
				return fmt.Errorf("%w: manifest.parameters.properties must be an object", ErrInvalid)
			}
			if len(pm) > MaxManifestParameters {
				return fmt.Errorf("%w: manifest.parameters has %d properties; the limit is %d", ErrInvalid, len(pm), MaxManifestParameters)
			}
		}
		b, err := json.Marshal(m.Parameters)
		if err != nil {
			return fmt.Errorf("%w: manifest.parameters is not JSON: %v", ErrInvalid, err)
		}
		if len(b) > MaxManifestParametersBytes {
			return fmt.Errorf("%w: manifest.parameters is %d bytes; the limit is %d", ErrInvalid, len(b), MaxManifestParametersBytes)
		}
		if hasRef(m.Parameters) {
			return fmt.Errorf("%w: manifest.parameters may not use $ref", ErrInvalid)
		}
	}
	if len(m.Requires) > MaxManifestRequires {
		return fmt.Errorf("%w: manifest.requires has %d entries; the limit is %d", ErrInvalid, len(m.Requires), MaxManifestRequires)
	}
	for _, r := range m.Requires {
		if strings.TrimSpace(r) == "" || strings.ContainsAny(r, " \t\n") {
			return fmt.Errorf("%w: manifest.requires entry %q is not a tool name", ErrInvalid, r)
		}
	}
	if len(m.Returns) > MaxManifestReturnsBytes || strings.Contains(m.Returns, "\n") {
		return fmt.Errorf("%w: manifest.returns must be one line of at most %d bytes", ErrInvalid, MaxManifestReturnsBytes)
	}
	return nil
}

// hasRef reports whether v contains a "$ref" key at any depth.
func hasRef(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if k == "$ref" || hasRef(child) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if hasRef(child) {
				return true
			}
		}
	}
	return false
}
