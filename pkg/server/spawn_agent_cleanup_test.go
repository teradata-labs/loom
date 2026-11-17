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
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
)

// TestCleanupSpawnedAgent_PublishDuringCleanup checks that a Publish running
// while a spawned agent is cleaned up never sends on the agent's notification
// channel after cleanup closed it ("send on closed channel" panic). Cleanup
// must unsubscribe, which waits out any Publish holding the bus lock, before
// it closes the notification channels.
func TestCleanupSpawnedAgent_PublishDuringCleanup(t *testing.T) {
	ctx := context.Background()
	rounds := 5000
	if testing.Short() {
		rounds = 500
	}
	for i := 0; i < rounds; i++ {
		bus := communication.NewMessageBus(nil, nil, nil, nil)
		s := NewMultiAgentServer(nil, nil)
		s.messageBus = bus

		sub, err := bus.Subscribe(ctx, "sub-agent", "spawn.cleanup", nil, 1000)
		require.NoError(t, err)
		// Capacity 1, as small as it gets, so a pending notification never
		// hides the send: Publish always tries it and drops on a full channel.
		notify := make(chan struct{}, 1)
		bus.RegisterNotificationChannel(sub.ID, notify)
		s.spawnedAgents["session"] = &spawnedAgentContext{
			subAgentID:      "sub-agent",
			subscriptionIDs: []string{sub.ID},
			notifyChannels:  []chan struct{}{notify},
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				// Drain the notification so the next Publish sends on it.
				select {
				case <-notify:
				default:
				}
				_, _, _ = bus.Publish(ctx, "spawn.cleanup", &loomv1.BusMessage{Id: "x", FromAgent: "parent"})
			}
		}()
		go func() {
			defer wg.Done()
			s.cleanupSpawnedAgent("session", "test")
		}()
		wg.Wait()
		require.NoError(t, bus.Close())
	}
}
