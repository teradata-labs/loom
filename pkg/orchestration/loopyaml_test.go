// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// The generated specs are the artifacts the loop will actually run, so they are
// what should be checked — not the JSON they were generated from.
//
// This closes the chain: the loop's own LoopSpec objects were dumped to JSON, a
// generator turned that into YAML, and here the YAML is loaded back and
// compared to the loop, edge for edge. A mistake anywhere along it shows up
// here rather than in a run.

func loadGeneratedSpecs(t *testing.T) map[string]*loomv1.StateMachinePattern {
	t.Helper()
	out := map[string]*loomv1.StateMachinePattern{}
	paths, err := filepath.Glob(filepath.Join("testdata", "loops", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "run bin/gen-loop-yaml.py to regenerate")
	for _, path := range paths {
		pattern, err := LoadWorkflowFromYAML(path)
		require.NoError(t, err, "%s must load", path)
		sm := pattern.GetStateMachine()
		require.NotNil(t, sm, "%s must be a state machine", path)
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		out[name] = sm
	}
	return out
}

func TestGeneratedSpecs_MatchTheLoop(t *testing.T) {
	t.Parallel()

	truth := loadLoopSpecs(t)
	generated := loadGeneratedSpecs(t)

	names := make([]string, 0, len(truth))
	for name := range truth {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		spec := truth[name]
		t.Run(name, func(t *testing.T) {
			sm, ok := generated[name]
			require.True(t, ok, "no YAML was generated for %s", name)

			assert.Equal(t, spec.StartState, sm.EntryState)
			assert.ElementsMatch(t, spec.LoopbackStates, sm.LoopbackStates,
				"stalling is a property of a cycle; the wrong states make a healthy run park")
			assert.ElementsMatch(t, spec.BubbleSignals, sm.BubbleSignals,
				"a bubbling signal belongs to whoever started the run")

			for signal, fields := range spec.SignalFields {
				got, ok := sm.SignalFields[strings.ToLower(signal)]
				require.True(t, ok, "signal %q lost its payload contract", signal)
				assert.ElementsMatch(t, fields, got.GetFields())
			}

			for _, terminal := range spec.Terminal {
				st, ok := sm.States[terminal]
				require.True(t, ok, "terminal %s is missing", terminal)
				assert.True(t, st.GetTerminal(), "%s must be terminal — it is how this loop answers its caller", terminal)
			}

			for stateName, want := range spec.States {
				st, ok := sm.States[stateName]
				require.True(t, ok, "state %s is missing", stateName)
				if want.Worker != nil && *want.Worker != "" {
					b := st.GetAgent()
					require.NotNil(t, b, "%s has a worker in the loop", stateName)
					assert.Equal(t, *want.Worker, b.GetAgentId())
					// The Python loop's first-entry skill is the state's instructions;
					// its re-entry skill has no counterpart, since a return carries
					// only what came back.
					assert.Equal(t, want.Prompts.Mint, b.GetInstructions())
				} else {
					b := st.GetTool()
					require.NotNil(t, b, "%s is worker-less in the loop, so it must cost nothing", stateName)
					assert.Equal(t, want.Prompts.Mint, b.GetTool())
				}
				assert.ElementsMatch(t, spec.Vocab[stateName], st.GetVocab(),
					"%s's vocabulary decides what is a malfunction and what is an escalation", stateName)
			}

			for _, e := range spec.Route {
				st := sm.States[e.State]
				require.NotNil(t, st)
				tr, ok := st.GetRoute()[strings.ToLower(e.Signal)]
				require.True(t, ok, "%s/%s is routed in the loop and not here", e.State, e.Signal)

				if strings.HasPrefix(e.Target, "advance:") {
					require.NotNil(t, tr.GetNextTool(), "%s/%s computes its destination", e.State, e.Signal)
					assert.Equal(t, e.Target, tr.GetNextTool().GetTool())
				} else {
					assert.Equal(t, e.Target, tr.GetNextState())
				}
				assert.ElementsMatch(t, e.Dismiss, tr.GetDismiss())
				switch e.Hook {
				case "":
					assert.Nil(t, tr.GetHook())
					assert.False(t, tr.GetCheckpoint())
				case HookCheckpoint:
					assert.True(t, tr.GetCheckpoint())
				default:
					require.NotNil(t, tr.GetHook(), "%s/%s fires %s", e.State, e.Signal, e.Hook)
					assert.Equal(t, e.Hook, tr.GetHook().GetTool())
				}
			}

			// Nothing invented on the way through the generator.
			for stateName, st := range sm.States {
				for signal := range st.GetRoute() {
					_, ok := spec.Route[stateName+"|"+signal]
					assert.True(t, ok, "%s/%s exists here but not in the loop", stateName, signal)
				}
			}
		})
	}
}

func TestGeneratedSpecs_CoverEveryLoop(t *testing.T) {
	t.Parallel()
	assert.Len(t, loadGeneratedSpecs(t), len(loadLoopSpecs(t)),
		"every loop must have a spec; a missing one is a phase that cannot run")
	_, err := os.Stat(filepath.Join("testdata", "loops"))
	require.NoError(t, err)
}
