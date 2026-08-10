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
package shuttle

import "sync"

// customHooks holds the compiled-in custom hook constructors keyed by the name a
// `kind:"custom"` binding references. It is populated only by RegisterCustomHook
// from a deployment's own init/startup — never from config, which merely names
// an already-registered constructor. This is the C-004/C-008 boundary: adding a
// custom decision needs a deploy build, config binds a name and never Go.
var (
	customHooksMu sync.RWMutex
	customHooks   = map[string]func(HookBinding) (Hook, error){}
)

// RegisterCustomHook records a compiled-in custom hook constructor under name,
// the entrypoint a deployment calls from its init/startup to contribute a
// clearance/DLP hook. It takes a Go function value, not a script, path, or
// plugin file — nothing is interpreted at runtime and no sandbox is used. A
// later registration under the same name replaces the earlier constructor.
func RegisterCustomHook(name string, ctor func(b HookBinding) (Hook, error)) {
	customHooksMu.Lock()
	defer customHooksMu.Unlock()
	customHooks[name] = ctor
}

// processCustomRegistry resolves custom hook names against the package-level
// registry populated by RegisterCustomHook. It is the concrete CustomHookRegistry
// wired into serve as ChainDeps.Custom.
type processCustomRegistry struct{}

// lookup returns the constructor registered under name, or false when none is.
// An unregistered name lets customHook abort serve (fail-closed on an unknown
// name), so config can never bind a custom decision that was not compiled in.
func (processCustomRegistry) lookup(name string) (func(HookBinding) (Hook, error), bool) {
	customHooksMu.RLock()
	defer customHooksMu.RUnlock()
	ctor, ok := customHooks[name]
	return ctor, ok
}

// ProcessCustomHookRegistry returns the process-wide custom hook registry to pass
// as ChainDeps.Custom at serve, so a `kind:"custom"` binding resolves against the
// constructors RegisterCustomHook compiled in.
func ProcessCustomHookRegistry() CustomHookRegistry {
	return processCustomRegistry{}
}
