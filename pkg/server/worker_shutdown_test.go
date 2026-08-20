// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
)

// TestShutdownBackgroundWorkers_ActiveSpawnedAgent proves the production
// shutdown sequence — cancel the queue monitor's context, then
// ShutdownBackgroundWorkers — joins an active spawned agent's monitor and
// message loop promptly. Before ShutdownBackgroundWorkers existed, runServe
// could only cancel the monitor's context: nothing cancelled the spawned-agent
// contexts, so this exact scenario consumed the full shutdown timeout and the
// workers survived into dependency teardown.
func TestShutdownBackgroundWorkers_ActiveSpawnedAgent(t *testing.T) {
	llm := &replyingLLM{}
	srv := spawnTestServer(t, llm)

	// Production runs the queue monitor; mirror runServe and start it.
	monitorCtx, cancelMonitor := context.WithCancel(context.Background())
	defer cancelMonitor()
	srv.StartMessageQueueMonitor(monitorCtx)

	// An active spawned agent: subscribed, so both its lifecycle monitor and
	// its message-processing loop are running.
	resp, err := srv.SpawnSubAgent(context.Background(), &builtin.SpawnSubAgentRequest{
		ParentSessionID: parentSession(t, srv),
		ParentAgentID:   "parent",
		AgentID:         "helper",
		AutoSubscribe:   []string{"audit-events"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.SubscribedTopics, "the spawn must be a live topic participant")

	// The production sequence from runServe: the monitor's context is the
	// caller's to cancel; everything else is ShutdownBackgroundWorkers' job.
	cancelMonitor()

	// runServe's budget is 10s; a healthy join must come in well under it or
	// SIGTERM eats the whole timeout on every deploy with an active spawn.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	err = srv.ShutdownBackgroundWorkers(ctx)
	require.NoError(t, err,
		"shutdown must cancel the spawned agent's workers and join them, not ride out the timeout (waited %v)", time.Since(start))
}

// TestShutdownBackgroundWorkers_ClosesAdmission proves the join is a stable
// lifecycle boundary: once shutdown has run, no new worker can register behind
// it, and a spawn racing shutdown fails cleanly rather than producing an
// unmonitored agent during dependency teardown.
func TestShutdownBackgroundWorkers_ClosesAdmission(t *testing.T) {
	llm := &replyingLLM{}
	srv := spawnTestServer(t, llm)

	require.NoError(t, srv.ShutdownBackgroundWorkers(context.Background()))

	assert.False(t, srv.goWorker("late-worker", func() {
		t.Error("a worker admitted after shutdown would outlive the join")
	}), "admission must be closed after shutdown")

	parent := parentSession(t, srv)
	_, err := srv.SpawnSubAgent(context.Background(), &builtin.SpawnSubAgentRequest{
		ParentSessionID: parent,
		ParentAgentID:   "parent",
		AgentID:         "helper",
	})
	require.Error(t, err, "a spawn racing shutdown must fail, not run unmonitored")
	assert.Contains(t, err.Error(), "shutting down")
	assert.Zero(t, srv.countSpawnedAgentsByParent(parent),
		"a refused spawn leaves nothing tracked")
}

// TestGoWorker_AdmissionShutdownRace hammers worker admission concurrently
// with shutdown. Every worker is either admitted — and then joined before
// ShutdownBackgroundWorkers returns — or refused and never run; there is no
// third outcome where an admitted worker escapes the join. Run with -race:
// without the admission gate this is exactly the "WaitGroup.Add called
// concurrently with Wait" misuse.
func TestGoWorker_AdmissionShutdownRace(t *testing.T) {
	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, nil)

	const spawners = 8
	const perSpawner = 50

	var admitted, ran atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < spawners; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < perSpawner; j++ {
				if srv.goWorker("race-test-worker", func() { ran.Add(1) }) {
					admitted.Add(1)
				}
			}
		}()
	}

	close(start)
	// Shutdown lands mid-hammer: workers admitted before it are joined,
	// attempts after it are refused.
	require.NoError(t, srv.ShutdownBackgroundWorkers(context.Background()))
	wg.Wait()

	assert.Equal(t, admitted.Load(), ran.Load(),
		"every admitted worker ran to completion under the join; every refused one never ran")
}
