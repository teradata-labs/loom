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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A rerun of the same question must not share a graph-memory scope with an
// earlier run: DeleteAgent leaves memories behind, and a name built from the
// question id alone let the second run recall the first run's memories.
func TestTempAgentNameIsUniquePerRun(t *testing.T) {
	n1, n2 := newRunNonce(), newRunNonce()
	require.Len(t, n1, 8)
	require.NotEqual(t, n1, n2, "two runs must get different nonces")

	a := tempAgentName(n1, "q-123")
	b := tempAgentName(n2, "q-123")
	assert.NotEqual(t, a, b)
	assert.True(t, strings.HasPrefix(a, "lme-tmp-"+n1+"-"), a)
	assert.True(t, strings.HasSuffix(a, "-q-123"), a)

	// Same run, same question: stable, so create/delete pair up.
	assert.Equal(t, a, tempAgentName(n1, "q-123"))
}
