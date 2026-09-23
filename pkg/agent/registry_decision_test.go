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

package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/decision"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
)

// TestRegistryWaitDecisionShadows is the guard for short-lived processes:
// a shadow started on an agent's last turn is recorded before the registry
// reports it is safe to exit.
func TestRegistryWaitDecisionShadows(t *testing.T) {
	tmp := t.TempDir()
	reg, err := NewRegistry(RegistryConfig{ConfigDir: tmp, DBPath: filepath.Join(tmp, "r.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })

	// Empty registry: returns at once.
	reg.WaitDecisionShadows()

	store := &memShadowStore{}
	dec := decisionmock.New().
		AnswerChoice(sites.QBranch, map[string]float64{"a": 0.9, "b": 0.05, sites.BranchNoneOfThese: 0.05}).
		SetLatency(50 * time.Millisecond)
	ag := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("slow"),
		WithDecisionRouter(decision.NewRouter(dec)),
		WithDecisionShadowStore(store))
	reg.mu.Lock()
	reg.agents["slow"] = ag
	reg.mu.Unlock()

	req, err := sites.BranchRequest("classify", []string{"a", "b"})
	require.NoError(t, err)
	ag.RunDecisionShadow(context.Background(), "s", req, sites.BranchReference("a"))

	reg.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	assert.Len(t, rows, 1, "the row landed before Wait returned")
}
