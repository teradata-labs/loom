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
package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	llmscheduler "github.com/teradata-labs/loom/pkg/llm/scheduler"
	"google.golang.org/grpc/metadata"
)

func mdCtx(kv ...string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(kv...))
}

func TestSlotOriginFromMetadata(t *testing.T) {
	assert.Equal(t, loomv1.SlotOrigin_SLOT_ORIGIN_INTERACTIVE,
		slotOriginFromMetadata(mdCtx(SlotOriginMetadataKey, "interactive")),
		"a client-asserted interactive turn bands INTERACTIVE")
	assert.Equal(t, loomv1.SlotOrigin_SLOT_ORIGIN_BATCH,
		slotOriginFromMetadata(mdCtx(SlotOriginMetadataKey, "batch")))
	assert.Equal(t, loomv1.SlotOrigin_SLOT_ORIGIN_BATCH,
		slotOriginFromMetadata(mdCtx("other-key", "x")),
		"absent metadata defaults to BATCH")
	assert.Equal(t, loomv1.SlotOrigin_SLOT_ORIGIN_BATCH,
		slotOriginFromMetadata(context.Background()),
		"no incoming metadata at all defaults to BATCH")
}

// Every turn-executing entry point installs the turn's SlotInfo through this
// helper; the SlotInfo must actually land on the context so the agent's LLM
// funnel schedules the turn.
func TestInstallTurnSlotInfoInstallsSlotInfo(t *testing.T) {
	ctx := installTurnSlotInfo(mdCtx(SlotOriginMetadataKey, "interactive"), false, "sess-1", "agent-1")
	require.NotNil(t, llmscheduler.SlotInfoFrom(ctx),
		"a fresh turn must carry SlotInfo")

	resumed := installTurnSlotInfo(mdCtx(SlotOriginMetadataKey, "batch"), true, "sess-2", "agent-2")
	require.NotNil(t, llmscheduler.SlotInfoFrom(resumed),
		"a resumed turn must carry SlotInfo (seeded IN_FLIGHT)")
}
