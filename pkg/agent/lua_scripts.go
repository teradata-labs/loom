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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/luasandbox/store"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// LuaCodeDuplicate is manage_lua_scripts' code for a save over an existing
// name without overwrite.
const LuaCodeDuplicate = "DUPLICATE"

// luaListMax bounds manage_lua_scripts list.
const luaListMax = 200

// manageLuaScriptsDescription is verbatim from the design (02-lua-api.md §2).
const manageLuaScriptsDescription = "Saves, lists, reads, deletes and publishes Lua scripts for reuse with run_lua. " +
	"Save a script when the user asks to keep it or when the same logic will run again. " +
	"A saved script runs with `run_lua name=...`. " +
	"Publishing a saved script turns it into a tool named `lua_<name>` whose inputs come from the script's manifest; " +
	"publish only when the user asks for a reusable tool. " +
	"Use run_lua to execute; use this tool only to manage saved scripts."

// LuaScriptsOptions wires saved scripts into an agent that has run_lua.
type LuaScriptsOptions struct {
	// Store keeps the scripts. Required.
	Store store.ScriptStore
	// SaveEnabled lets manage_lua_scripts register (tools.lua.scripts.save_enabled).
	SaveEnabled bool
	// PublishEnabled lets manage_lua_scripts publish scripts as tools
	// (tools.lua.scripts.publish_as_tool_enabled).
	PublishEnabled bool
}

// StoreScriptResolver resolves run_lua's `name` against s.
func StoreScriptResolver(s store.ScriptStore) ScriptResolver {
	return func(ctx context.Context, nameOrID string) (ResolvedScript, error) {
		sc, err := s.GetForRunner(ctx, nameOrID)
		if errors.Is(err, store.ErrNotFound) {
			return ResolvedScript{}, fmt.Errorf("no saved script named %s: %w", nameOrID, ErrScriptNotFound)
		}
		if err != nil {
			return ResolvedScript{}, err
		}
		return resolvedFrom(sc), nil
	}
}

func resolvedFrom(sc store.Script) ResolvedScript {
	var requires []string
	if sc.Manifest != nil {
		requires = sc.Manifest.Requires
	}
	return ResolvedScript{Name: sc.Name, Source: sc.Source, Trust: sc.Trust, Requires: requires}
}

// LuaScriptsReport says what RegisterLuaScriptTools registered and what it
// skipped, with the reason, for the caller to log.
type LuaScriptsReport struct {
	Registered []string
	Skipped    map[string]string
}

func (r *LuaScriptsReport) skip(name, reason string) {
	if r.Skipped == nil {
		r.Skipped = make(map[string]string)
	}
	r.Skipped[name] = reason
}

// runLuaTool returns the run_lua registered on this agent, or nil.
func (a *Agent) runLuaTool() *RunLuaTool {
	t, ok := a.tools.Get(RunLuaToolName)
	if !ok {
		return nil
	}
	run, _ := t.(*RunLuaTool)
	return run
}

// RegisterLuaScriptTools wires saved scripts into an agent that already has
// run_lua: manage_lua_scripts when wantManage (the agent lists it) and saves
// are enabled, and a lua_<name> tool for each published script attached to
// this agent or named by a lua:// custom tool (customScripts). Scripts that
// are missing, unpublished or have no manifest parameters are skipped.
func (a *Agent) RegisterLuaScriptTools(ctx context.Context, opts LuaScriptsOptions, wantManage bool, customScripts []string) LuaScriptsReport {
	var rep LuaScriptsReport
	run := a.runLuaTool()
	reason := ""
	switch {
	case run == nil:
		reason = "run_lua is not registered on this agent"
	case opts.Store == nil:
		reason = "no script store is configured"
	}
	if reason != "" {
		if wantManage {
			rep.skip(ManageLuaScriptsToolName, reason)
		}
		for _, n := range customScripts {
			rep.skip(store.ToolName(n), reason)
		}
		return rep
	}

	if wantManage {
		switch {
		case !opts.SaveEnabled:
			rep.skip(ManageLuaScriptsToolName, "tools.lua.scripts.save_enabled is false")
		case a.isBuiltinToolSuppressed(ManageLuaScriptsToolName):
			rep.skip(ManageLuaScriptsToolName, "suppressed on this agent")
		default:
			a.RegisterTool(&ManageLuaScriptsTool{agent: a, run: run, opts: opts})
			rep.Registered = append(rep.Registered, ManageLuaScriptsToolName)
		}
	}

	attached, err := opts.Store.Attachments(ctx, a.config.Name)
	if err != nil {
		rep.skip("attachments", err.Error())
	}
	seen := make(map[string]bool)
	for _, name := range append(attached, customScripts...) {
		if seen[name] {
			continue
		}
		seen[name] = true
		sc, err := opts.Store.Get(ctx, name)
		switch {
		case err != nil:
			rep.skip(store.ToolName(name), err.Error())
			continue
		case !sc.Published:
			rep.skip(store.ToolName(name), "the script is not published")
			continue
		}
		tool, err := newScriptTool(run, opts.Store, sc)
		if err != nil {
			rep.skip(store.ToolName(name), err.Error())
			continue
		}
		a.RegisterTool(tool)
		rep.Registered = append(rep.Registered, tool.Name())
	}
	return rep
}

// ScriptTool is a published saved script exposed as a tool, lua_<name>. Its
// call runs the script through run_lua's path, with the caller's tools and
// the script's requires; the executor's admission chain applies to the tool
// call itself and to every nested call.
type ScriptTool struct {
	run         *RunLuaTool
	store       store.ScriptStore
	script      string
	description string
	schema      *shuttle.JSONSchema
}

var _ shuttle.Tool = (*ScriptTool)(nil)

// newScriptTool builds lua_<name> for a published script with manifest
// parameters.
func newScriptTool(run *RunLuaTool, st store.ScriptStore, sc store.Script) (*ScriptTool, error) {
	if sc.Manifest == nil || sc.Manifest.Parameters == nil {
		return nil, errors.New("the script has no manifest parameters")
	}
	b, err := json.Marshal(sc.Manifest.Parameters)
	if err != nil {
		return nil, fmt.Errorf("manifest parameters: %w", err)
	}
	var schema shuttle.JSONSchema
	if err := json.Unmarshal(b, &schema); err != nil {
		return nil, fmt.Errorf("manifest parameters: %w", err)
	}
	desc := "Saved Lua script. " + strings.TrimSpace(sc.Description)
	if sc.Manifest.Returns != "" {
		desc += " Returns: " + sc.Manifest.Returns
	}
	return &ScriptTool{run: run, store: st, script: sc.Name, description: desc, schema: &schema}, nil
}

func (t *ScriptTool) Name() string                     { return store.ToolName(t.script) }
func (t *ScriptTool) Description() string              { return t.description }
func (t *ScriptTool) InputSchema() *shuttle.JSONSchema { return t.schema }

// Backend is empty (backend-agnostic) so backend-filtered tool listings keep it.
func (t *ScriptTool) Backend() string { return "" }

// Execute re-reads the script on every call, so a new version runs at once
// and an unpublished or deleted script stops at once, on every agent.
func (t *ScriptTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()
	ctx, span := t.run.agent.tracer.StartSpan(ctx, "lua.run")
	defer t.run.agent.tracer.EndSpan(span)

	sc, err := t.store.GetForRunner(ctx, t.script)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return luaFailure(span, start, LuaCodeScriptNotFound, t.Name()+": the script was deleted", "", false), nil
	case err != nil:
		return luaFailure(span, start, LuaCodePolicyDenied, t.Name()+": the script could not be loaded", "", false), nil
	case !sc.Published:
		return luaFailure(span, start, LuaCodePolicyDenied, t.Name()+" is no longer published", "", false), nil
	}
	return t.run.runResolved(ctx, span, start, resolvedFrom(sc), params, 0), nil
}

// ManageLuaScriptsTool is the manage_lua_scripts builtin: save, get, list,
// delete, publish, unpublish and detach saved scripts. On a loom server every
// agent manages the same server-wide store.
type ManageLuaScriptsTool struct {
	agent *Agent
	run   *RunLuaTool
	opts  LuaScriptsOptions
}

var _ shuttle.Tool = (*ManageLuaScriptsTool)(nil)

func (t *ManageLuaScriptsTool) Name() string        { return ManageLuaScriptsToolName }
func (t *ManageLuaScriptsTool) Description() string { return manageLuaScriptsDescription }
func (t *ManageLuaScriptsTool) Backend() string     { return "" }

func (t *ManageLuaScriptsTool) InputSchema() *shuttle.JSONSchema {
	actions := []interface{}{"save", "get", "list", "delete", "publish", "unpublish", "detach"}
	return shuttle.NewObjectSchema("", map[string]*shuttle.JSONSchema{
		"action":      {Type: "string", Enum: actions},
		"name":        shuttle.NewStringSchema("Script name: ^[a-z][a-z0-9_]{2,40}$. Required except for list."),
		"script":      shuttle.NewStringSchema("Lua 5.4 source. Required for save."),
		"description": shuttle.NewStringSchema("One or two sentences on what the script does. Required for save."),
		"manifest": shuttle.NewObjectSchema("Optional for save. Needed before publish.", map[string]*shuttle.JSONSchema{
			"parameters": {Type: "object", Description: "JSON Schema (type object) for args when published as a tool. Max 20 properties, 4 KiB."},
			"requires":   shuttle.NewArraySchema("Tool names the script calls. Enforced at run time when set.", shuttle.NewStringSchema("")),
			"returns":    shuttle.NewStringSchema("One line describing the return value."),
		}, nil),
		"overwrite": shuttle.NewBooleanSchema("Replace an existing script with the same name. Default false."),
	}, []string{"action"})
}

var manageParamNames = map[string]bool{"action": true, "name": true, "script": true, "description": true, "manifest": true, "overwrite": true}

func luaManageFail(code, msg string) *shuttle.Result {
	return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: code, Message: msg}}
}

// Execute dispatches one action. Every failure is a Result; the error is
// always nil.
func (t *ManageLuaScriptsTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	for k := range params {
		if !manageParamNames[k] {
			return luaManageFail(LuaCodeInvalidParams, "unknown parameter: "+k), nil
		}
	}
	action, _ := params["action"].(string)
	name, _ := params["name"].(string)
	if action != "list" && action != "" {
		if err := store.ValidateName(name); err != nil {
			return luaManageFail(LuaCodeInvalidParams, err.Error()), nil
		}
	}
	switch action {
	case "save":
		return t.save(ctx, name, params), nil
	case "get":
		return t.get(ctx, name), nil
	case "list":
		return t.list(ctx), nil
	case "delete":
		return t.delete(ctx, name), nil
	case "publish":
		return t.publish(ctx, name), nil
	case "unpublish":
		return t.unpublish(ctx, name), nil
	case "detach":
		return t.detach(ctx, name), nil
	}
	return luaManageFail(LuaCodeInvalidParams, fmt.Sprintf("unknown action %q", action)), nil
}

// storeFail maps a store error.
func storeFail(err error) *shuttle.Result {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return luaManageFail(LuaCodeScriptNotFound, err.Error())
	case errors.Is(err, store.ErrDuplicate):
		return luaManageFail(LuaCodeDuplicate, err.Error()+"; pass overwrite to replace it")
	case errors.Is(err, store.ErrInvalid):
		return luaManageFail(LuaCodeInvalidParams, err.Error())
	case errors.Is(err, store.ErrNotPublished):
		return luaManageFail(LuaCodePolicyDenied, err.Error())
	}
	return luaManageFail(LuaCodeHostError, "the script store failed: "+err.Error())
}

func (t *ManageLuaScriptsTool) save(ctx context.Context, name string, params map[string]interface{}) *shuttle.Result {
	source, _ := params["script"].(string)
	desc, _ := params["description"].(string)
	overwrite, _ := params["overwrite"].(bool)
	manifest, err := manifestParam(params["manifest"])
	if err != nil {
		return luaManageFail(LuaCodeInvalidParams, err.Error())
	}
	sc, _, err := t.opts.Store.Save(ctx, store.Script{
		Name: name, Description: desc, Source: source, Manifest: manifest, Owner: t.agent.config.Name,
	}, overwrite)
	if err != nil {
		return storeFail(err)
	}
	return &shuttle.Result{Success: true, Data: map[string]interface{}{
		"name": sc.Name, "version": sc.Version, "tool_name": store.ToolName(sc.Name), "published": sc.Published,
	}}
}

// manifestParam converts the tool's manifest argument.
func manifestParam(v interface{}) (*store.Manifest, error) {
	if v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("manifest must be an object, got %T", v)
	}
	out := &store.Manifest{}
	for k, val := range m {
		switch k {
		case "parameters":
			p, ok := val.(map[string]interface{})
			if !ok {
				return nil, errors.New("manifest.parameters must be an object")
			}
			out.Parameters = p
		case "requires":
			list, ok := val.([]interface{})
			if !ok {
				return nil, errors.New("manifest.requires must be a list of tool names")
			}
			out.Requires = make([]string, 0, len(list))
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					return nil, errors.New("manifest.requires must be a list of tool names")
				}
				out.Requires = append(out.Requires, s)
			}
		case "returns":
			s, ok := val.(string)
			if !ok {
				return nil, errors.New("manifest.returns must be a string")
			}
			out.Returns = s
		default:
			return nil, fmt.Errorf("unknown manifest field: %s", k)
		}
	}
	return out, nil
}

func (t *ManageLuaScriptsTool) get(ctx context.Context, name string) *shuttle.Result {
	sc, err := t.opts.Store.GetForRunner(ctx, name)
	if err != nil {
		return storeFail(err)
	}
	data := map[string]interface{}{
		"name": sc.Name, "description": sc.Description, "version": sc.Version, "published": sc.Published,
		"script": sc.Source, "updated_at": sc.UpdatedAt.Format(time.RFC3339), "owner": sc.Owner, "trust": sc.Trust.String(),
	}
	if sc.Manifest != nil {
		data["manifest"] = sc.Manifest
	}
	return &shuttle.Result{Success: true, Data: data}
}

func (t *ManageLuaScriptsTool) list(ctx context.Context) *shuttle.Result {
	all, err := t.opts.Store.List(ctx)
	if err != nil {
		return storeFail(err)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	if len(all) > luaListMax {
		all = all[:luaListMax]
	}
	rows := make([]map[string]interface{}, 0, len(all))
	for _, sc := range all {
		rows = append(rows, map[string]interface{}{
			"name": sc.Name, "description": sc.Description, "version": sc.Version, "published": sc.Published,
			"trust": luasandbox.TrustOwn.String(), "updated_at": sc.UpdatedAt.Format(time.RFC3339),
		})
	}
	return &shuttle.Result{Success: true, Data: rows}
}

func (t *ManageLuaScriptsTool) delete(ctx context.Context, name string) *shuttle.Result {
	if err := t.opts.Store.Delete(ctx, name); err != nil {
		return storeFail(err)
	}
	t.agent.UnregisterTool(store.ToolName(name))
	return &shuttle.Result{Success: true, Data: map[string]interface{}{"name": name, "deleted": true}}
}

func (t *ManageLuaScriptsTool) publish(ctx context.Context, name string) *shuttle.Result {
	if !t.opts.PublishEnabled {
		return luaManageFail(LuaCodePolicyDenied, "publishing scripts as tools is disabled (tools.lua.scripts.publish_as_tool_enabled)")
	}
	sc, err := t.opts.Store.Get(ctx, name)
	if err != nil {
		return storeFail(err)
	}
	if sc.Manifest == nil || sc.Manifest.Parameters == nil {
		return luaManageFail(LuaCodeInvalidParams, "publish needs a manifest with parameters; save the script again with one")
	}
	if sc.Manifest.Requires != nil {
		projection, ok := advertisedProjectionFromContext(ctx)
		if !ok {
			return luaManageFail(LuaCodePolicyDenied, "this call carries no record of the agent's tools")
		}
		pol, err := t.run.opts.Policy(ctx)
		if err != nil {
			return luaManageFail(LuaCodePolicyDenied, "the Lua policy could not be loaded")
		}
		visible := make(map[string]bool)
		for _, n := range withLuaReservedNames(pol).Visible(projection, luasandbox.TrustOwn) {
			visible[n] = true
		}
		for _, r := range sc.Manifest.Requires {
			if !visible[r] {
				return luaManageFail(luasandbox.CodeToolNotVisible,
					fmt.Sprintf("the script requires %s, which scripts on this agent cannot call", r))
			}
		}
	}
	if err := t.opts.Store.SetPublished(ctx, name, true); err != nil {
		return storeFail(err)
	}
	if err := t.opts.Store.Attach(ctx, t.agent.config.Name, name); err != nil {
		return storeFail(err)
	}
	sc.Published = true
	tool, err := newScriptTool(t.run, t.opts.Store, sc)
	if err != nil {
		return luaManageFail(LuaCodeInvalidParams, err.Error())
	}
	t.agent.RegisterTool(tool) // advertised from the next model call, to every session of this agent
	return &shuttle.Result{Success: true, Data: map[string]interface{}{
		"name": name, "tool_name": tool.Name(), "published": true, "attached": true,
	}}
}

func (t *ManageLuaScriptsTool) unpublish(ctx context.Context, name string) *shuttle.Result {
	if err := t.opts.Store.SetPublished(ctx, name, false); err != nil {
		return storeFail(err)
	}
	t.agent.UnregisterTool(store.ToolName(name)) // other agents' copies refuse at their next call
	return &shuttle.Result{Success: true, Data: map[string]interface{}{"name": name, "published": false}}
}

func (t *ManageLuaScriptsTool) detach(ctx context.Context, name string) *shuttle.Result {
	if err := t.opts.Store.Detach(ctx, t.agent.config.Name, name); err != nil {
		return storeFail(err)
	}
	t.agent.UnregisterTool(store.ToolName(name))
	return &shuttle.Result{Success: true, Data: map[string]interface{}{"name": name, "attached": false}}
}
