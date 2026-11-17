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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision/jev"
)

// Review #410 blocking 1: RegisterJudge stored a JUDGE_TYPE_DECISION config
// whose decision.base_url named a caller's host, and EvaluateWithJudges then
// sent the server's env key there. Registration must refuse the endpoint,
// and a config that reaches evaluation without registration (stored
// directly) must still never produce a request to it.
// Not parallel: it sets the decider key in the environment.
func TestJudgeServerRefusesDecisionEndpoint(t *testing.T) {
	t.Setenv(jev.EnvTypeSafeAPIKey, "ts_fake_judge_key_not_real")
	jev.ResetShared()
	t.Cleanup(jev.ResetShared)

	var requests, sawKey atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" {
			sawKey.Add(1)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(collector.Close)

	cfg := &loomv1.JudgeConfig{
		Id:   "exfil",
		Name: "exfil",
		Type: loomv1.JudgeType_JUDGE_TYPE_DECISION,
		Decision: &loomv1.DecisionConfig{
			Provider: "jev",
			Model:    jev.DefaultModel,
			BaseUrl:  collector.URL,
		},
	}
	s := newTestJudgeServer()
	ctx := context.Background()

	_, err := s.RegisterJudge(ctx, &loomv1.RegisterJudgeRequest{Config: cfg})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "server-level only")

	// Bypass registration: the construction path refuses it too.
	s.mu.Lock()
	s.configs["exfil"] = cfg
	s.mu.Unlock()
	_, err = s.EvaluateWithJudges(ctx, &loomv1.EvaluateRequest{
		JudgeIds: []string{"exfil"},
		Context:  &loomv1.EvaluationContext{Prompt: "q", Response: "a"},
	})
	require.Error(t, err)

	assert.Equal(t, int32(0), requests.Load(), "the judge-named endpoint received a request")
	assert.Equal(t, int32(0), sawKey.Load(), "the judge-named endpoint received the server's key")
}

// A decision judge without an endpoint registers normally.
func TestJudgeServerAcceptsDecisionJudgeWithoutEndpoint(t *testing.T) {
	t.Parallel()
	s := newTestJudgeServer()
	_, err := s.RegisterJudge(context.Background(), &loomv1.RegisterJudgeRequest{Config: &loomv1.JudgeConfig{
		Id: "ok", Name: "ok", Type: loomv1.JudgeType_JUDGE_TYPE_DECISION,
		Decision: &loomv1.DecisionConfig{Provider: "mock"},
	}})
	require.NoError(t, err)
}
