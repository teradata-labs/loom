// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"testing"
)

// Run variables exist so a ported loop's FIRST state can interpolate the paths
// its prompt was written against. The loops this engine runs came from a
// per-loop variables() function that handed a dozen values to every state; with
// no seam for those, an entry state's prompt reaches the model with literal
// braces in it and the run goes green on a prompt nobody could read.
//
// Two properties carry that: a seed reaches a fresh state, and a seed never
// overwrites what a run has since worked out.

func TestASeedReachesAFreshRun(t *testing.T) {
	ctx := WithRunVars(context.Background(), map[string]string{
		"spec_path": "/u/10-spec.md",
		"repos_dir": "/u/repos",
	})

	got := seedRunVars(ctx, map[string]string{})

	for k, want := range map[string]string{
		"spec_path": "/u/10-spec.md", "repos_dir": "/u/repos",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

// TestASeedNeverOverwritesWhatTheRunWorkedOut is the resume contract. The
// checkpoint holds a cursor a state advanced; re-seeding over it would send the
// run back to repeat work it had already finished.
func TestASeedNeverOverwritesWhatTheRunWorkedOut(t *testing.T) {
	ctx := WithRunVars(context.Background(), map[string]string{
		"task":      "D-1", // where the run STARTED
		"spec_path": "/u/10-spec.md",
	})
	restored := map[string]string{"task": "D-7"} // where it had got to

	got := seedRunVars(ctx, restored)

	if got["task"] != "D-7" {
		t.Errorf("task = %q, want D-7 — the seed reset a cursor mid-walk", got["task"])
	}
	if got["spec_path"] != "/u/10-spec.md" {
		t.Errorf("spec_path = %q, want the seed to fill a gap", got["spec_path"])
	}
}

func TestNoSeedChangesNothing(t *testing.T) {
	existing := map[string]string{"task": "D-1"}

	got := seedRunVars(context.Background(), existing)

	if len(got) != 1 || got["task"] != "D-1" {
		t.Errorf("an unseeded run's vars were altered: %v", got)
	}
	if got := seedRunVars(context.Background(), nil); got != nil {
		t.Errorf("seeding nil with nothing produced %v, want nil", got)
	}
}

// TestTheSeedIsCopied, because a caller that reuses its map after starting a run
// would otherwise mutate the run's starting knowledge from outside it.
func TestTheSeedIsCopied(t *testing.T) {
	caller := map[string]string{"spec_path": "/u/10-spec.md"}
	ctx := WithRunVars(context.Background(), caller)

	caller["spec_path"] = "/somewhere/else"
	caller["late_addition"] = "should not appear"

	got := RunVarsFromContext(ctx)
	if got["spec_path"] != "/u/10-spec.md" {
		t.Errorf("spec_path = %q — the caller mutated a started run", got["spec_path"])
	}
	if _, leaked := got["late_addition"]; leaked {
		t.Error("a key added after the run started reached it")
	}
}

func TestAnEmptySeedIsNotCarried(t *testing.T) {
	for _, m := range []map[string]string{nil, {}} {
		if RunVarsFromContext(WithRunVars(context.Background(), m)) != nil {
			t.Errorf("WithRunVars(%v) carried something", m)
		}
	}
}

// TestInterpolationConsumesSeededVars ties the seam to the thing it exists for:
// the interpolator must substitute a seeded name exactly as it does a reserved
// one, braces added by the engine and not by the caller.
func TestInterpolationConsumesSeededVars(t *testing.T) {
	ctx := WithRunVars(context.Background(), map[string]string{"spec_path": "/u/10-spec.md"})
	vars := seedRunVars(ctx, map[string]string{})

	got := interpolateRun("read the spec at {spec_path} in {loom_dir}",
		map[string]string{ParamDir: "/u"}, vars)

	if want := "read the spec at /u/10-spec.md in /u"; got != want {
		t.Errorf("interpolateRun = %q, want %q", got, want)
	}
}

// TestAnUnseededPlaceholderSurvivesLiterally documents the failure this seam
// prevents, so the verification step has something to assert against: an
// unknown name is left untouched and travels to the model as braces.
func TestAnUnseededPlaceholderSurvivesLiterally(t *testing.T) {
	got := interpolateRun("read {spec_path}", map[string]string{ParamDir: "/u"}, nil)

	if got != "read {spec_path}" {
		t.Errorf("interpolateRun = %q; an unknown placeholder must stay literal so "+
			"a coverage check can find it", got)
	}
}
