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
package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"github.com/teradata-labs/loom/pkg/observability"
)

func TestNewSessionStore_LoggerInitialization(t *testing.T) {
	tests := []struct {
		name         string
		logger       *zap.Logger
		expectNonNil bool
		description  string
	}{
		{
			name:         "nil logger defaults to nop",
			logger:       nil,
			expectNonNil: true,
			description:  "passing nil logger should initialize a nop logger",
		},
		{
			name:         "explicit logger is used",
			logger:       zaptest.NewLogger(t),
			expectNonNil: true,
			description:  "passing an explicit logger should use it",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewSessionStore(nil, observability.NewNoOpTracer(), tt.logger)
			require.NotNil(t, store, "store should not be nil")
			assert.NotNil(t, store.logger, tt.description)
		})
	}
}

func TestNewSessionStore_TracerInitialization(t *testing.T) {
	store := NewSessionStore(nil, nil, nil)
	require.NotNil(t, store, "store should not be nil")
	assert.NotNil(t, store.tracer, "nil tracer should default to no-op")
}

func TestNewAdminStore_LoggerInitialization(t *testing.T) {
	tests := []struct {
		name         string
		logger       *zap.Logger
		expectNonNil bool
		description  string
	}{
		{
			name:         "nil logger defaults to nop",
			logger:       nil,
			expectNonNil: true,
			description:  "passing nil logger should initialize a nop logger",
		},
		{
			name:         "explicit logger is used",
			logger:       zaptest.NewLogger(t),
			expectNonNil: true,
			description:  "passing an explicit logger should use it",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewAdminStore(nil, observability.NewNoOpTracer(), tt.logger)
			require.NotNil(t, store, "store should not be nil")
			assert.NotNil(t, store.logger, tt.description)
		})
	}
}

func TestNewAdminStore_TracerInitialization(t *testing.T) {
	store := NewAdminStore(nil, nil, nil)
	require.NotNil(t, store, "store should not be nil")
	assert.NotNil(t, store.tracer, "nil tracer should default to no-op")
}
