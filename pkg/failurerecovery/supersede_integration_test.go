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

package failurerecovery_test

// This file holds the real-backend proof for lesson evolution (spec §7) that the
// unit tests and the live agent demo cannot give:
//
//   - The unit tests use fakes, so they prove the engine's branching logic but
//     not that the real Postgres store creates a SUPERSEDES lineage and hides the
//     old row from recall.
//   - The live agent demo cannot force a supersede: the LLM abstractor varies the
//     problem/solution wording run-to-run, so the exact-match dedup (findExisting)
//     rarely fires, and a rule-abstracted identical solution only TOUCHES (reuse),
//     never supersedes.
//
// So we drive the write path directly (via the LearnForTest shim) with crafted
// (problem, solution) pairs against a real GraphMemoryStore and real Titan
// embeddings. Same abstract problem, an EVOLVED solution → the store must
// supersede (latest fix wins, lineage kept, reuse_count carried forward). An
// identical solution → the store must only touch.
//
// It lives in the external test package to break the import cycle it would
// otherwise create (postgres -> agent -> failurerecovery), and uses the
// LearnForTest / RecallForTest / ReuseCountOf shims from export_test.go.
//
// It never runs in the normal suite: it is gated behind the `integration` build
// tag AND the LOOM_FR_INTEGRATION=1 env var, and it needs a reachable Postgres
// (pgvector) plus AWS Bedrock credentials. Run it with:
//
//	# PowerShell, from repo root, with AWS SSO creds loaded (. .\load-env.ps1):
//	$env:LOOM_FR_INTEGRATION = '1'
//	$env:LOOM_FR_DSN = 'postgres://chatuser:chatpass@localhost:5432/loom_e2e'
//	$env:AWS_REGION  = 'us-west-2'
//	go test -tags 'fts5 integration' -run TestIntegration_Supersede ./pkg/failurerecovery

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teradata-labs/loom/pkg/embedding"
	fr "github.com/teradata-labs/loom/pkg/failurerecovery"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
	"github.com/teradata-labs/loom/pkg/types"
)

const (
	defaultIntegrationDSN = "postgres://chatuser:chatpass@localhost:5432/loom_e2e"
	defaultIntegrationAWS = "us-west-2"
)

// countingSink records the engine's lesson-write events by name so the test can
// assert WHICH branch (learned / superseded / reused) fired.
type countingSink struct{ events []string }

func (s *countingSink) AddEvent(name string, _ map[string]interface{}) {
	s.events = append(s.events, name)
}

func (s *countingSink) count(name string) int {
	n := 0
	for _, e := range s.events {
		if e == name {
			n++
		}
	}
	return n
}

// newIntegrationEngine wires a real Postgres store + real Titan embedder into a
// user-scoped engine. It skips the test when the gate env var is unset or any
// dependency is unreachable, so the suite stays green on machines without a DB
// or AWS credentials.
func newIntegrationEngine(t *testing.T) (*fr.Engine, *pgxpool.Pool) {
	t.Helper()

	if os.Getenv("LOOM_FR_INTEGRATION") != "1" {
		t.Skip("set LOOM_FR_INTEGRATION=1 (plus a reachable Postgres and AWS creds) to run the real-backend supersede proof")
	}

	dsn := os.Getenv("LOOM_FR_DSN")
	if dsn == "" {
		dsn = defaultIntegrationDSN
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = defaultIntegrationAWS
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to Postgres at %s: %v", dsn, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("Postgres ping failed at %s: %v", dsn, err)
	}

	embedder, err := embedding.NewBedrockEmbedder(ctx, embedding.BedrockConfig{
		Region:     region,
		Dimensions: embedding.DefaultTitanDimensions,
		Normalize:  true,
	})
	if err != nil {
		pool.Close()
		t.Skipf("cannot build Titan embedder (region %s): %v", region, err)
	}

	store := postgres.NewGraphMemoryStore(pool, nil, nil)
	e := fr.NewEngine(store, embedder, fr.Config{
		Enabled:    true,
		UserScoped: true,
		MaxHints:   2,
	})
	return e, pool
}

// uniqueOwner returns a per-run owner id so each test writes into its own private
// partition and cleanup never touches another run's rows.
func uniqueOwner(prefix string) string {
	return prefix + "-" + time.Now().UTC().Format("20060102T150405.000000000")
}

// cleanupOwner deletes every row this test wrote, identified by its unique owner.
func cleanupOwner(t *testing.T, pool *pgxpool.Pool, owner string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `DELETE FROM graph_memories WHERE owner = $1`, owner); err != nil {
		t.Logf("cleanup: failed to delete rows for owner %s: %v", owner, err)
	}
}

// TestIntegration_Supersede proves the evolved-solution path end to end against
// the real store: the same abstract problem with a BETTER fix supersedes the old
// lesson, recall returns only the latest fix, and reuse_count carries forward.
func TestIntegration_Supersede(t *testing.T) {
	e, pool := newIntegrationEngine(t)
	defer pool.Close()

	owner := uniqueOwner("fr-int-supersede")
	defer cleanupOwner(t, pool, owner)

	ctx := types.ContextWithUserID(context.Background(), owner)
	const agentID = "fr-int-agent"

	problem := "running a query that selects the column named order without quoting it fails on Teradata (reserved keyword, Error 3706)"
	firstFix := `quote the identifier as "order"`
	evolvedFix := `quote the reserved identifier as "order" and prefer aliasing it (SELECT "order" AS channel) so downstream columns stay readable`

	// 1) First recovery → a brand-new lesson is learned.
	sink1 := &countingSink{}
	e.LearnForTest(ctx, agentID, fr.ClassToolExec, "execute_sql", problem, firstFix, sink1)
	if sink1.count("failure_recovery.learned") != 1 {
		t.Fatalf("first learn: expected 1 learned event, got events %v", sink1.events)
	}

	// 2) Same problem, EVOLVED fix → supersede (not a second row, not a touch).
	sink2 := &countingSink{}
	e.LearnForTest(ctx, agentID, fr.ClassToolExec, "execute_sql", problem, evolvedFix, sink2)
	if got := sink2.count("failure_recovery.superseded"); got != 1 {
		t.Fatalf("evolved learn: expected 1 superseded event, got %d (events %v)", got, sink2.events)
	}
	if got := sink2.count("failure_recovery.learned"); got != 0 {
		t.Fatalf("evolved learn: expected 0 learned events (should supersede, not insert), got %d", got)
	}

	// 3) Recall must now return exactly the latest fix — the old row is hidden by
	//    the SUPERSEDES lineage the real store maintains.
	mems := e.RecallForTest(ctx, agentID, problem, fr.ClassToolExec, "execute_sql", 0.0)
	if len(mems) == 0 {
		t.Fatalf("recall after supersede returned no lessons; expected the evolved fix")
	}
	var latest string
	for _, m := range mems {
		if m == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(m.Summary), strings.TrimSpace(evolvedFix)) {
			latest = m.Summary
		}
		if strings.EqualFold(strings.TrimSpace(m.Summary), strings.TrimSpace(firstFix)) {
			t.Errorf("recall still returns the superseded (old) fix %q; it should be hidden", firstFix)
		}
	}
	if latest == "" {
		got := make([]string, 0, len(mems))
		for _, m := range mems {
			if m != nil {
				got = append(got, m.Summary)
			}
		}
		t.Fatalf("recall did not return the evolved fix; summaries seen: %v", got)
	}

	// 4) reuse_count carried forward and incremented (0 on first, 1 after evolve).
	for _, m := range mems {
		if m != nil && strings.EqualFold(strings.TrimSpace(m.Summary), strings.TrimSpace(evolvedFix)) {
			if rc := fr.ReuseCountOf(m); rc != 1 {
				t.Errorf("evolved lesson reuse_count = %d, want 1 (carried forward from the superseded row)", rc)
			}
		}
	}
}

// TestIntegration_ReuseNotSupersede proves the identical-solution path: the same
// problem with the SAME fix only refreshes the existing lesson (reuse), never
// creating a second row and never superseding.
func TestIntegration_ReuseNotSupersede(t *testing.T) {
	e, pool := newIntegrationEngine(t)
	defer pool.Close()

	owner := uniqueOwner("fr-int-reuse")
	defer cleanupOwner(t, pool, owner)

	ctx := types.ContextWithUserID(context.Background(), owner)
	const agentID = "fr-int-agent"

	problem := "selecting a reserved identifier without quoting fails (Error 3706)"
	fix := `quote the identifier`

	sink1 := &countingSink{}
	e.LearnForTest(ctx, agentID, fr.ClassToolExec, "execute_sql", problem, fix, sink1)
	if sink1.count("failure_recovery.learned") != 1 {
		t.Fatalf("first learn: expected 1 learned event, got %v", sink1.events)
	}

	sink2 := &countingSink{}
	e.LearnForTest(ctx, agentID, fr.ClassToolExec, "execute_sql", problem, fix, sink2)
	if got := sink2.count("failure_recovery.reused"); got != 1 {
		t.Fatalf("repeat learn with identical fix: expected 1 reused event, got %d (events %v)", got, sink2.events)
	}
	if got := sink2.count("failure_recovery.superseded"); got != 0 {
		t.Fatalf("repeat learn with identical fix: expected 0 superseded events, got %d", got)
	}

	// Exactly one row should answer recall for this problem.
	mems := e.RecallForTest(ctx, agentID, problem, fr.ClassToolExec, "execute_sql", 0.0)
	matching := 0
	for _, m := range mems {
		if m != nil && strings.EqualFold(strings.TrimSpace(m.Content), strings.TrimSpace(problem)) {
			matching++
		}
	}
	if matching != 1 {
		t.Errorf("expected exactly 1 lesson for the reused problem, got %d", matching)
	}
}
