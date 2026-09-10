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

func TestSetBackpressureRoundTrip(t *testing.T) {
	hint := BackpressureHint{
		Code:        "session_handle_budget_full",
		RetryAfterS: 42,
		WaitParam:   "wait_s",
		MaxWaitS:    300,
	}

	e := &Error{Code: "BUDGET_FULL", Message: "no handles free"}
	e.SetBackpressure(hint)

	got := e.Backpressure()
	require.NotNil(t, got)
	assert.Equal(t, hint, *got)
	assert.True(t, e.Retryable, "backpressure is by definition retryable")
}

func TestSetBackpressureNilReceiverIsNoOp(t *testing.T) {
	var e *Error
	e.SetBackpressure(BackpressureHint{Code: "x"})
	assert.Nil(t, e.Backpressure())
}

func TestBackpressureTolerantParse(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want *BackpressureHint
	}{
		{
			name: "nil error",
			err:  nil,
			want: nil,
		},
		{
			name: "nil details",
			err:  &Error{Code: "X"},
			want: nil,
		},
		{
			name: "missing key",
			err:  &Error{Details: map[string]interface{}{"other": 1}},
			want: nil,
		},
		{
			name: "malformed value yields nil, never an error",
			err:  &Error{Details: map[string]interface{}{DetailsBackpressure: "busy"}},
			want: nil,
		},
		{
			name: "typed hint stored directly",
			err: &Error{Details: map[string]interface{}{
				DetailsBackpressure: BackpressureHint{Code: "c", RetryAfterS: 1},
			}},
			want: &BackpressureHint{Code: "c", RetryAfterS: 1},
		},
		{
			name: "JSON numbers arrive as float64",
			err: &Error{Details: map[string]interface{}{
				DetailsBackpressure: map[string]interface{}{
					"code":          "budget_full",
					"retry_after_s": float64(42),
					"wait_param":    "wait_s",
					"max_wait_s":    float64(300),
				},
			}},
			want: &BackpressureHint{Code: "budget_full", RetryAfterS: 42, WaitParam: "wait_s", MaxWaitS: 300},
		},
		{
			name: "wrong-typed fields inside a well-formed map read as zero",
			err: &Error{Details: map[string]interface{}{
				DetailsBackpressure: map[string]interface{}{
					"code":          7,
					"retry_after_s": "soon",
					"wait_param":    true,
					"max_wait_s":    nil,
				},
			}},
			want: &BackpressureHint{},
		},
		{
			name: "empty map is a declaration with no estimate",
			err: &Error{Details: map[string]interface{}{
				DetailsBackpressure: map[string]interface{}{},
			}},
			want: &BackpressureHint{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.err.Backpressure())
		})
	}
}

// The stored form must survive a JSON marshal/unmarshal boundary and parse
// to the identical hint on the other side.
func TestBackpressureSurvivesJSONRoundTrip(t *testing.T) {
	e := &Error{Code: "BUDGET_FULL", Message: "no handles free"}
	e.SetBackpressure(BackpressureHint{Code: "budget_full", RetryAfterS: 5, WaitParam: "wait_s", MaxWaitS: 60})

	raw, err := json.Marshal(e.Details)
	require.NoError(t, err)
	var back map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &back))

	assert.Equal(t, e.Backpressure(), (&Error{Details: back}).Backpressure())
}

func TestDetailsInt64(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want int64
	}{
		{"int64", int64(9), 9},
		{"int", int(8), 8},
		{"int32", int32(7), 7},
		{"float64", float64(6), 6},
		{"json.Number", json.Number("5"), 5},
		{"json.Number malformed", json.Number("x"), 0},
		{"string", "4", 0},
		{"nil", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, detailsInt64(tt.in))
		})
	}
}
