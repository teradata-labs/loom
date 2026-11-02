// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Which conversation a seat continues is the spec's to say: a seat declared
// to think on from another seat of the run forks that seat's session. A run
// var naming a session for the agent still wins, which is how a run started
// from a design made elsewhere carries that design mind in.
func TestASeatContinuesTheConversationTheSpecNames(t *testing.T) {
	e := &StateMachineExecutor{workflowID: "groom-7"}
	seat := &loomv1.AgentState{AgentId: "test_architect", ForkOf: "creator"}
	assert.Equal(t, "groom-7-creator", e.forkParent(seat, nil))
	assert.Equal(t, "groom-3-creator", e.forkParent(seat, map[string]string{"fork:test_architect": "groom-3-creator"}))
	assert.Equal(t, "", e.forkParent(&loomv1.AgentState{AgentId: "coder"}, nil), "a seat with no named mind starts cold")
}
