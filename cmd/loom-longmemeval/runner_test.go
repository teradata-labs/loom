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

//go:build fts5

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildWeaveRequest(t *testing.T) {
	tests := []struct {
		name       string
		occurredAt time.Time
		wantSet    bool
	}{
		{
			name:       "zero time omits occurred_at",
			occurredAt: time.Time{},
			wantSet:    false,
		},
		{
			name:       "historical date sets occurred_at",
			occurredAt: time.Date(2023, 4, 10, 17, 50, 0, 0, time.UTC),
			wantSet:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := buildWeaveRequest("sess-1", "query", "agent-1", tt.occurredAt)
			assert.Equal(t, "sess-1", req.SessionId)
			assert.Equal(t, "query", req.Query)
			assert.Equal(t, "agent-1", req.AgentId)

			if !tt.wantSet {
				assert.Nil(t, req.OccurredAt)
				return
			}
			require.NotNil(t, req.OccurredAt)
			assert.True(t, tt.occurredAt.Equal(req.OccurredAt.AsTime()))
		})
	}
}

func TestBuildWeaveRequestFromDatasetDate(t *testing.T) {
	// The exact format the LongMemEval dataset uses for haystack/question dates.
	parsed, err := ParseDate("2023/04/10 (Mon) 17:50")
	require.NoError(t, err)

	req := buildWeaveRequest("sess-1", "query", "agent-1", parsed)
	require.NotNil(t, req.OccurredAt)
	assert.Equal(t, parsed.UTC(), req.OccurredAt.AsTime())
}

func TestSessionOccurredAt(t *testing.T) {
	sess := SessionWithDate{
		Date:     "2023/04/10 (Mon) 17:50",
		ParsedAt: time.Date(2023, 4, 10, 17, 50, 0, 0, time.UTC),
	}

	enabled := &Runner{config: RunConfig{UseOccurredAt: true}}
	assert.True(t, sess.ParsedAt.Equal(enabled.sessionOccurredAt(sess)))

	disabled := &Runner{config: RunConfig{UseOccurredAt: false}}
	assert.True(t, disabled.sessionOccurredAt(sess).IsZero())
}
