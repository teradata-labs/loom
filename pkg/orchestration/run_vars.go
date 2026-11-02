// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"strings"
)

// Run variables: the values every state of one run may interpolate, supplied by
// whoever started it.
//
// A state machine already interpolates four reserved names ({loom_dir},
// {loom_state}, {loom_signal}, {loom_message}) plus whatever an earlier state
// emitted as `vars` on its signal line. That is enough for a machine whose
// prompts only need to know where they are, and not enough for a ported loop:
// the loops this engine runs were written against a per-loop variables()
// function that computed a dozen paths from the run's directory and handed them
// to EVERY state, including the first. Without a seam for those, the entry
// state's prompt reaches the model with literal braces in it — the agent
// improvises, the machine still walks, and the run goes green on nothing.
//
// They ride the context rather than the pattern because they belong to one RUN,
// not to the process's copy of a spec: two runs of the same loop in the same
// process differ in exactly these values. (The pattern would also mean a proto
// change, and its Dir field already shows what happens when run state is filed
// under the definition — it has to be overwritten per run before every call.)
//
// A state that emits `vars` still wins: a seeded value is the run's starting
// knowledge, and a state that has worked something out is the only thing that
// knows better.
type runVarsKey struct{}

// WithRunVars carries the run's starting variables. Keys are bare names — the
// interpolator adds the braces — so "spec_path" becomes {spec_path}. A nil or
// empty map is the same as not calling it.
func WithRunVars(ctx context.Context, vars map[string]string) context.Context {
	if len(vars) == 0 {
		return ctx
	}
	copied := make(map[string]string, len(vars))
	for k, v := range vars {
		copied[k] = v
	}
	return context.WithValue(ctx, runVarsKey{}, copied)
}

// handDown replaces the run's starting variables with what a parent gives its
// child, even when that is nothing: a child is given what its parent hands it
// and no more, so an empty hand must not leave the parent's own in place.
func handDown(ctx context.Context, vars map[string]string) context.Context {
	copied := make(map[string]string, len(vars))
	for k, v := range vars {
		copied[k] = v
	}
	return context.WithValue(ctx, runVarsKey{}, copied)
}

// RunVarsFromContext returns the run's starting variables, or nil.
func RunVarsFromContext(ctx context.Context) map[string]string {
	if v, ok := ctx.Value(runVarsKey{}).(map[string]string); ok {
		return v
	}
	return nil
}

// seedRunVars merges the context's run variables into a state's var map without
// overwriting what is already there.
//
// Not overwriting is the whole contract on resume: the checkpoint holds what the
// run has since worked out, and a seed is only what it began with. Re-seeding
// over a checkpoint would reset a cursor mid-walk — the run would repeat work it
// had already finished, which is worse than not seeding at all.
func seedRunVars(ctx context.Context, vars map[string]string) map[string]string {
	seed := RunVarsFromContext(ctx)
	if len(seed) == 0 {
		return vars
	}
	if vars == nil {
		vars = make(map[string]string, len(seed))
	}
	for k, v := range seed {
		if _, exists := vars[k]; !exists {
			vars[k] = v
		}
	}
	return vars
}

// A child's opening message.
//
// A state's output does two jobs: its last line routes, and everything above it
// is what the next state needs to know. When the next state is a delegated loop,
// "the next state" is the child's first state — so the handoff has to cross into
// the child, or the child starts deaf. The case that showed it: a lead judged a
// delivered task not right, wrote the finding, and re-ran the task; the coder,
// never told the finding, delivered the same correct thing three times, and the
// lead gave up.
type initialMessageKey struct{}

// WithInitialMessage is what a parent hands its child on the way in.
func WithInitialMessage(ctx context.Context, msg string) context.Context {
	if strings.TrimSpace(msg) == "" {
		return ctx
	}
	return context.WithValue(ctx, initialMessageKey{}, msg)
}

// handMessage is what a parent hands its children on the way in, even when
// that is nothing: a child opens with what its parent gave it, never with
// what a machine further up gave its parent.
func handMessage(ctx context.Context, msg string) context.Context {
	return context.WithValue(ctx, initialMessageKey{}, msg)
}

// InitialMessageFromContext is what a fresh run opens with, if anything.
func InitialMessageFromContext(ctx context.Context) string {
	v, _ := ctx.Value(initialMessageKey{}).(string)
	return v
}
