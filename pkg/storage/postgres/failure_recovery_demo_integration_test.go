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

//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/failurerecovery"
	"github.com/teradata-labs/loom/pkg/memory"
)

// demoSink records failure-recovery observability events so the demo can print
// the exact signals (learned / reactive_hit / proactive_hit) the feature emits.
// It structurally satisfies the engine's unexported eventSink interface.
type demoSink struct{ events []string }

func (s *demoSink) AddEvent(name string, _ map[string]interface{}) {
	s.events = append(s.events, name)
}

// TestDemo_FailureRecovery_CrossSession is a runnable, self-verifying proof of
// the failure-recovery-memory POC against a live Postgres/pgvector database.
//
// Run it with narration:
//
//	just demo-failure-recovery
//	# or:
//	TEST_POSTGRES_URL=postgres://chatuser:chatpass@localhost:5432/loom_test \
//	  go test -tags integration ./pkg/storage/postgres/ \
//	  -run TestDemo_FailureRecovery_CrossSession -v
//
// It tells the two-session story and asserts every business claim:
//
//  1. LEARN   — a lesson is written only when a failure is recovered from.
//  2. PII-SAFE — the stored lesson holds abstract text only; no raw error,
//     query, table name, or secret value is ever persisted.
//  3. REACTIVE — in a NEW session, a DIFFERENT error of the same class recalls
//     the stored fix (proving cross-session reuse, no embedder).
//  4. PROACTIVE — a task intent recalls the pitfall up front, before any tool
//     runs (keyword-overlap on the fallback path; semantic on the
//     embedder path).
//
// No embedder is configured, so this exercises the keyword (FTS) fallback path —
// exactly the current Bedrock deployment shape.
func TestDemo_FailureRecovery_CrossSession(t *testing.T) {
	pool := testPool(t) // applies all migrations, including pgvector + HNSW
	ensureRLSWriterRole(t, pool)

	store := NewGraphMemoryStore(pool, nil, nil)

	// No embedder => FTS keyword fallback (the current Bedrock setup).
	engine := failurerecovery.NewEngine(store, nil, failurerecovery.Config{
		Enabled:   true,
		Proactive: failurerecovery.ProactiveConfig{Enabled: true},
	})

	agentID := uniqueID("demo-agent")
	ctx := ContextWithUserID(context.Background(), uniqueID("demo-user"))
	sink := &demoSink{}

	// A raw tool error that deliberately carries SENSITIVE data. The abstraction
	// layer must strip all of it and store only the fixed problem/solution.
	const rawErrorS1 = `ERROR: syntax error at or near "from"; ` +
		`query=SELECT from FROM secret_accounts WHERE ssn='123-45-6789'`

	t.Log("── Session 1: pay once ────────────────────────────────────────")
	session1 := uniqueID("session-1")

	// The tool fails...
	engine.OnToolFailure(session1, failurerecovery.ClassToolExec, "run_sql", rawErrorS1)
	// ...the agent self-corrects and the SAME tool later succeeds => recovery.
	engine.OnToolSuccess(ctx, agentID, session1, "run_sql", sink)
	t.Logf("   events after recovery: %v", sink.events)

	// CLAIM 1: exactly one lesson was learned.
	lessons, err := store.Recall(ctx, memory.RecallOpts{
		AgentID:    agentID,
		MemoryType: memory.MemoryTypeFailureRecovery,
		Limit:      10,
	})
	require.NoError(t, err)
	require.Len(t, lessons, 1, "recovery must persist exactly one lesson")
	lesson := lessons[0]
	t.Logf("   LEARNED problem : %q", lesson.Content)
	t.Logf("   LEARNED solution: %q", lesson.Summary)
	assert.Contains(t, sink.events, "failure_recovery.learned")

	// CLAIM 2: PII safety — none of the raw/sensitive tokens are persisted.
	haystack := strings.ToLower(lesson.Content + " " + lesson.Summary + " " + lesson.PropertiesJSON)
	for _, secret := range []string{"123-45-6789", "secret_accounts", "ssn", "select from"} {
		assert.NotContainsf(t, haystack, strings.ToLower(secret),
			"stored lesson must not contain raw/sensitive token %q", secret)
	}
	// The lesson is the abstract rule, not the incident.
	assert.Equal(t, "reserved SQL keyword used as an unquoted identifier", lesson.Content)
	assert.Equal(t, "wrap the identifier in double quotes", lesson.Summary)
	t.Log("   PII-SAFE: no raw query, table name, or SSN stored ✓")

	t.Log("── Session 2: reap it (a brand-new session) ───────────────────")
	session2 := uniqueID("session-2")
	_ = session2

	// A DIFFERENT raw error, different secret, same failure class.
	const rawErrorS2 = `ERROR: syntax error at or near "select"; ` +
		`query=SELECT select FROM payroll WHERE card='4111-1111-1111-1111'`

	// CLAIM 3: reactive recall returns the stored fix across sessions.
	hints := engine.ReactiveHints(ctx, agentID, failurerecovery.ClassToolExec, "run_sql", rawErrorS2, sink)
	require.NotEmpty(t, hints, "reactive recall must find the lesson via keyword search (no embedder)")
	t.Logf("   REACTIVE hint injected: %q", hints[0])
	assert.Contains(t, hints[0], "wrap the identifier in double quotes")
	assert.Contains(t, sink.events, "failure_recovery.reactive_hit")

	t.Log("── Proactive: pre-warn before any tool runs ───────────────────")

	// CLAIM 4: a task intent recalls the pitfall up front, before any tool runs.
	//
	// NOTE ON THE FALLBACK PATH: with no embedder, proactive recall uses Postgres
	// keyword search (plainto_tsquery), which ANDs the intent's terms — so the
	// intent must share vocabulary with the stored lesson. Loose, semantically-
	// similar phrasings ("column name clash", "quoting problem") only match once
	// an embedder is configured and the vector path is used. This intent shares
	// terms with the stored lesson to exercise the keyword path.
	const intent = "using a reserved SQL keyword as an identifier"
	pitfalls := engine.ProactiveHints(ctx, agentID, intent, sink)
	require.NotEmpty(t, pitfalls, "proactive recall must pre-warn about the known pitfall")
	t.Logf("   PROACTIVE pitfall injected: %q", pitfalls[0])
	assert.Contains(t, pitfalls[0], "wrap the identifier in double quotes")
	assert.Contains(t, sink.events, "failure_recovery.proactive_hit")

	t.Log("── POC proven: learn-once, reuse across sessions, PII-safe ✓ ───")
}
