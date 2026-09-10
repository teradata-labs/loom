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

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendLeaseEvent(t *testing.T) {
	t.Run("nil result is a no-op", func(t *testing.T) {
		AppendLeaseEvent(nil, LeaseEvent{Action: LeaseAcquired, Kind: "db-session", ID: "s1"})
	})

	t.Run("initializes nil metadata", func(t *testing.T) {
		res := &Result{Success: true}
		AppendLeaseEvent(res, LeaseEvent{Action: LeaseAcquired, Kind: "db-session", ID: "s1"})
		require.NotNil(t, res.Metadata)
		assert.Equal(t, []LeaseEvent{{Action: LeaseAcquired, Kind: "db-session", ID: "s1"}}, LeaseEventsFrom(res))
	})

	t.Run("appends in emit order", func(t *testing.T) {
		res := &Result{Success: true}
		AppendLeaseEvent(res, LeaseEvent{Action: LeaseAcquired, Kind: "db-session", ID: "s1"})
		AppendLeaseEvent(res, LeaseEvent{Action: LeaseReleased, Kind: "db-session", ID: "s1"})
		assert.Equal(t, []LeaseEvent{
			{Action: LeaseAcquired, Kind: "db-session", ID: "s1"},
			{Action: LeaseReleased, Kind: "db-session", ID: "s1"},
		}, LeaseEventsFrom(res))
	})

	t.Run("preserves malformed sibling entries", func(t *testing.T) {
		res := &Result{Metadata: map[string]interface{}{
			MetadataLeaseEvents: []interface{}{"garbage", 42},
		}}
		AppendLeaseEvent(res, LeaseEvent{Action: LeaseAcquired, Kind: "k", ID: "i"})
		entries, ok := res.Metadata[MetadataLeaseEvents].([]interface{})
		require.True(t, ok)
		assert.Len(t, entries, 3, "malformed entries stay in the metadata; only parsing skips them")
		assert.Equal(t, []LeaseEvent{{Action: LeaseAcquired, Kind: "k", ID: "i"}}, LeaseEventsFrom(res))
	})

	t.Run("replaces a non-slice value under the key", func(t *testing.T) {
		res := &Result{Metadata: map[string]interface{}{MetadataLeaseEvents: "not-a-slice"}}
		AppendLeaseEvent(res, LeaseEvent{Action: LeaseAcquired, Kind: "k", ID: "i"})
		assert.Equal(t, []LeaseEvent{{Action: LeaseAcquired, Kind: "k", ID: "i"}}, LeaseEventsFrom(res))
	})
}

func TestLeaseEventsFrom(t *testing.T) {
	tests := []struct {
		name string
		res  *Result
		want []LeaseEvent
	}{
		{
			name: "nil result",
			res:  nil,
			want: nil,
		},
		{
			name: "nil metadata",
			res:  &Result{Success: true},
			want: nil,
		},
		{
			name: "missing key",
			res:  &Result{Metadata: map[string]interface{}{"other": true}},
			want: nil,
		},
		{
			name: "value is not a slice",
			res:  &Result{Metadata: map[string]interface{}{MetadataLeaseEvents: "acquired"}},
			want: nil,
		},
		{
			name: "JSON-shaped map entries",
			res: &Result{Metadata: map[string]interface{}{
				MetadataLeaseEvents: []interface{}{
					map[string]interface{}{"action": "acquired", "kind": "db-session", "id": "sess-42"},
					map[string]interface{}{"action": "released", "kind": "db-session", "id": "sess-42"},
				},
			}},
			want: []LeaseEvent{
				{Action: LeaseAcquired, Kind: "db-session", ID: "sess-42"},
				{Action: LeaseReleased, Kind: "db-session", ID: "sess-42"},
			},
		},
		{
			name: "typed LeaseEvent entries",
			res: &Result{Metadata: map[string]interface{}{
				MetadataLeaseEvents: []interface{}{
					LeaseEvent{Action: LeaseAcquired, Kind: "api-slot", ID: "7"},
				},
			}},
			want: []LeaseEvent{{Action: LeaseAcquired, Kind: "api-slot", ID: "7"}},
		},
		{
			name: "empty kind is a valid identity",
			res: &Result{Metadata: map[string]interface{}{
				MetadataLeaseEvents: []interface{}{
					map[string]interface{}{"action": "acquired", "id": "bare"},
				},
			}},
			want: []LeaseEvent{{Action: LeaseAcquired, ID: "bare"}},
		},
		{
			name: "malformed entries are skipped, never an error",
			res: &Result{Metadata: map[string]interface{}{
				MetadataLeaseEvents: []interface{}{
					"not-a-map",
					42,
					nil,
					map[string]interface{}{"kind": "db-session", "id": "no-action"},
					map[string]interface{}{"action": "granted", "kind": "db-session", "id": "bad-action"},
					map[string]interface{}{"action": "acquired", "kind": "db-session"},
					map[string]interface{}{"action": "acquired", "kind": "db-session", "id": ""},
					map[string]interface{}{"action": 1, "kind": "db-session", "id": "typed-wrong"},
					map[string]interface{}{"action": "acquired", "kind": "db-session", "id": "good"},
				},
			}},
			want: []LeaseEvent{{Action: LeaseAcquired, Kind: "db-session", ID: "good"}},
		},
		{
			name: "all entries malformed yields nil",
			res: &Result{Metadata: map[string]interface{}{
				MetadataLeaseEvents: []interface{}{"junk"},
			}},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, LeaseEventsFrom(tt.res))
		})
	}
}

// The stored form must survive a JSON marshal/unmarshal boundary (backend
// adapters and subprocess transports serialize Result.Metadata) and parse to
// the identical events on the other side.
func TestLeaseEventsSurviveJSONRoundTrip(t *testing.T) {
	res := &Result{Success: true}
	AppendLeaseEvent(res, LeaseEvent{Action: LeaseAcquired, Kind: "db-session", ID: "sess-42"})
	AppendLeaseEvent(res, LeaseEvent{Action: LeaseReleased, Kind: "api-slot", ID: "7"})

	raw, err := json.Marshal(res.Metadata)
	require.NoError(t, err)
	var back map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &back))

	assert.Equal(t,
		LeaseEventsFrom(res),
		LeaseEventsFrom(&Result{Metadata: back}))
}
