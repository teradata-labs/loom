// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// The engine keeps its own notes beside the run's variables. A state's output
// writes the run's variables only: it cannot reach a note the engine keeps,
// such as what budget a child was granted.
func TestModelVars_AStatesOutputCannotWriteTheEnginesOwnNotes(t *testing.T) {
	t.Parallel()

	state := &machineState{vars: map[string]string{"__budget:FAN": "2.5"}}
	applyVars(state, `{"signal": "ok", "vars": {"__budget:FAN": "0", "__kept:FAN": "x", "__message": "y", "found": "a"}}`, true, nil)

	assert.Equal(t, "2.5", state.vars["__budget:FAN"], "a child's budget is the engine's, not the model's")
	assert.NotContains(t, state.vars, "__kept:FAN")
	assert.NotContains(t, state.vars, "__message")
	assert.Equal(t, "a", state.vars["found"], "an ordinary variable is still taken")
}

// A seat's conversation is named by the spec or the host that started the
// run. A state's output cannot move a seat into someone else's conversation,
// or start it from a copy of one.
func TestModelVars_AStatesOutputCannotSeatAnAgentInAnotherConversation(t *testing.T) {
	t.Parallel()

	state := &machineState{vars: map[string]string{}}
	applyVars(state, `{"signal": "ok", "vars": {"session:reviewer": "someone-elses-session", "fork:coder": "theirs"}}`, true, nil)

	assert.NotContains(t, state.vars, SessionVarPrefix+"reviewer")
	assert.NotContains(t, state.vars, ForkVarPrefix+"coder")

	// A tool state is the spec author's code: it names a seat's parent.
	applyVars(state, `{"signal": "named", "vars": {"fork:planner": "run-designer"}}`, false, nil)
	assert.Equal(t, "run-designer", state.vars[ForkVarPrefix+"planner"])
}

// Where a child works is named by a list a model wrote. It is always somewhere
// under the parent's own folder: a name that climbs out of it, or names an
// absolute place, stops the step instead of creating that folder.
func TestModelVars_AChildCannotBePlacedOutsideItsParentsFolder(t *testing.T) {
	t.Parallel()

	for _, place := range []string{"../../escaped", "/etc/loom-escaped", "repos/../../escaped"} {
		e := &StateMachineExecutor{dir: t.TempDir()}
		_, err := e.childDir(&loomv1.DelegateState{}, listEntry{Item: "x", Dir: place})
		assert.Error(t, err, "a child placed at %q", place)

		_, err = e.childDir(&loomv1.DelegateState{DirTemplate: "repos/{item}"}, listEntry{Item: place})
		assert.Error(t, err, "an item %q spelled into the template", place)
	}

	e := &StateMachineExecutor{dir: "/runs/r1"}
	got, err := e.childDir(&loomv1.DelegateState{DirTemplate: "repos/{item}"}, listEntry{Item: "loom"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/runs/r1", "repos", "loom"), got)
}

// What the host started the run with — where the work lives, which unit it is —
// and the names the engine fills itself are not a model's to change: a model
// writing {ws} could point a later command at a Makefile of its choosing.
func TestModelVars_AModelCannotRedefineWhatTheRunWasStartedWith(t *testing.T) {
	t.Parallel()

	state := &machineState{vars: map[string]string{"ws": "/work/tera-ws"}}
	host := map[string]string{"ws": "/work/tera-ws", "unit_dir": "/work/tera-ws/units/u1"}
	applyVars(state, `{"signal":"ok","vars":{"ws":"/tmp/evil","unit_dir":"/tmp/evil","loom_dir":"/tmp/evil","loom_run":"x","repo":"loom"}}`, true, host)

	assert.Equal(t, "/work/tera-ws", state.vars["ws"], "a model redefined where the work lives")
	assert.NotContains(t, state.vars, "unit_dir", "a model wrote a name the host set")
	assert.NotContains(t, state.vars, ParamDir, "a model shadowed a name the engine fills")
	assert.NotContains(t, state.vars, ParamRun)
	assert.Equal(t, "loom", state.vars["repo"], "an ordinary variable is still taken")
}
