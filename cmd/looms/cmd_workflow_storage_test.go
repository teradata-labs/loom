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

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/checkpoint"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/orchestration"
	"github.com/teradata-labs/loom/pkg/storage/backend"
	"github.com/teradata-labs/loom/pkg/types"
)

// isolateWorkflowStorage points the workflow commands at a throwaway SQLite
// database: LOOM_DATA_DIR in a temp dir (never the developer's real ~/.loom)
// and a zero-value package `config` (normally populated by cobra's
// initConfig, which these tests bypass). The database is NOT migrated, as a
// fresh machine's would not be; see migrateWorkflowStorage.
func isolateWorkflowStorage(t *testing.T) {
	t.Helper()
	t.Setenv("LOOM_DATA_DIR", t.TempDir())
	origConfig, origUser := config, workflowUser
	config = &Config{}
	workflowUser = ""
	t.Cleanup(func() { config, workflowUser = origConfig, origUser })
}

// migrateWorkflowStorage applies the storage migrations the way `looms serve`
// does, which the workflow commands deliberately never do.
func migrateWorkflowStorage(t *testing.T) {
	t.Helper()
	sb, err := backend.NewStorageBackend(context.Background(), config.BuildProtoStorageConfig(), observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("NewStorageBackend: %v", err)
	}
	defer func() { _ = sb.Close() }()
	if err := sb.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
}

// realOpener is the opener the workflow commands use, counting how many times
// the storage backend is opened.
func realOpener(opens *atomic.Int32) openCheckpointStoreFunc {
	return func(ctx context.Context) (*checkpointHandle, error) {
		opens.Add(1)
		return openCheckpointStore(ctx, observability.NewNoOpTracer())
	}
}

// saveStored saves ckpt through the real opener as the current workflowUser.
func saveStored(t *testing.T, ckpt *loomv1.WorkflowCheckpoint) {
	t.Helper()
	var opens atomic.Int32
	err := withCheckpointStore(context.Background(), realOpener(&opens), func(ctx context.Context, store checkpoint.CheckpointStore) error {
		return store.SaveCheckpoint(ctx, ckpt)
	})
	if err != nil {
		t.Fatalf("save %q: %v", ckpt.GetWorkflowId(), err)
	}
}

func TestOpenCheckpointStore_DoesNotMigrate(t *testing.T) {
	isolateWorkflowStorage(t)

	// storage.migration.auto_migrate defaults to true in a real config; the
	// workflow commands must not act on it. Opening an unmigrated database
	// leaves it unmigrated, so a save fails for want of the table.
	config.Storage.Migration.AutoMigrate = true
	var opens atomic.Int32
	err := withCheckpointStore(context.Background(), realOpener(&opens), func(ctx context.Context, store checkpoint.CheckpointStore) error {
		return store.SaveCheckpoint(ctx, &loomv1.WorkflowCheckpoint{WorkflowId: "wf-unmigrated"})
	})
	if err == nil {
		t.Fatal("SaveCheckpoint succeeded on a never-migrated database: opening the store must not run migrations")
	}

	// Once something else (serve, upgrade) migrates it, the same call works.
	migrateWorkflowStorage(t)
	saveStored(t, &loomv1.WorkflowCheckpoint{WorkflowId: "wf-migrated"})
}

func TestNewWorkflowRuntime_OpensNoStorage(t *testing.T) {
	isolateWorkflowStorage(t)
	// An unreachable Postgres host: building the runtime used to open the
	// backend and ping it, which took over a minute to give up.
	config.Storage.Backend = "postgres"
	config.Storage.Postgres.Host = "10.255.255.1"
	config.Storage.Postgres.Database = "loom"
	workflowUser = "alice"

	start := time.Now()
	rt, err := newWorkflowRuntime()
	if err != nil {
		t.Fatalf("newWorkflowRuntime: %v", err)
	}
	defer rt.Close()
	if rt.openCheckpoints == nil {
		t.Fatal("runtime has no checkpoint opener")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("newWorkflowRuntime took %v: it must not touch the storage backend", elapsed)
	}
}

func TestWithCheckpointStore_BoundsASlowOpen(t *testing.T) {
	orig := workflowStorageTimeout
	workflowStorageTimeout = 100 * time.Millisecond
	t.Cleanup(func() { workflowStorageTimeout = orig })

	// An opener that blocks until its context ends, like a pool ping to a
	// host that never answers.
	open := func(ctx context.Context) (*checkpointHandle, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	start := time.Now()
	err := withCheckpointStore(context.Background(), open, func(context.Context, checkpoint.CheckpointStore) error {
		t.Fatal("fn ran although the store never opened")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %v, want about the 100ms bound", elapsed)
	}
}

func TestPersistCheckpoint_FallsBackToFileWhenNoStore(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.checkpoint.pb"
	ckpt := &loomv1.WorkflowCheckpoint{WorkflowId: "wf-fallback-test"}

	stored, err := persistCheckpoint(context.Background(), nil, zap.NewNop(), path, ckpt)
	if err != nil {
		t.Fatalf("persistCheckpoint: %v", err)
	}
	if stored {
		t.Fatal("storedInBackend = true with no store")
	}

	got, err := readCheckpointFile(path)
	if err != nil {
		t.Fatalf("expected fallback file to exist: %v", err)
	}
	if got.WorkflowId != "wf-fallback-test" {
		t.Fatalf("got workflow_id %q, want wf-fallback-test", got.WorkflowId)
	}
}

func TestPersistCheckpoint_WritesBothStoreAndFileWhenStoreAvailable(t *testing.T) {
	isolateWorkflowStorage(t)
	migrateWorkflowStorage(t)

	path := t.TempDir() + "/test.checkpoint.pb"
	var opens atomic.Int32
	stored, err := persistCheckpoint(context.Background(), realOpener(&opens), zap.NewNop(), path,
		&loomv1.WorkflowCheckpoint{WorkflowId: "wf-store-test"})
	if err != nil {
		t.Fatalf("persistCheckpoint: %v", err)
	}
	if !stored {
		t.Fatal("storedInBackend = false although the store write landed")
	}

	// Both the local file (same-machine resume) and the store (resume by
	// workflow ID) are written, not either/or.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected local file to still be written: %v", err)
	}
	loaded, err := loadStoredCheckpoint(context.Background(), realOpener(&opens), "wf-store-test")
	if err != nil {
		t.Fatalf("loadStoredCheckpoint: %v", err)
	}
	if loaded.WorkflowId != "wf-store-test" {
		t.Fatalf("got workflow_id %q, want wf-store-test", loaded.WorkflowId)
	}
}

func TestPersistCheckpoint_StoreFailureKeepsFileAndReportsNotStored(t *testing.T) {
	isolateWorkflowStorage(t) // never migrated, so the store write fails

	core, logs := observer.New(zap.WarnLevel)
	path := t.TempDir() + "/test.checkpoint.pb"
	var opens atomic.Int32
	stored, err := persistCheckpoint(context.Background(), realOpener(&opens), zap.New(core), path,
		&loomv1.WorkflowCheckpoint{WorkflowId: "wf-store-fails"})
	if err != nil {
		t.Fatalf("persistCheckpoint: a store failure must not fail the command: %v", err)
	}
	if stored {
		t.Fatal("storedInBackend = true although the store write failed")
	}
	if _, err := readCheckpointFile(path); err != nil {
		t.Fatalf("the file must still be written: %v", err)
	}
	if logs.Len() != 1 {
		t.Fatalf("got %d warnings, want 1: %v", logs.Len(), logs.All())
	}
}

// TestPersistCheckpoint_PostgresWithoutTenantWritesFileOnly covers a postgres
// config where no --user names the tenant: the run still writes its file, says
// why the store was skipped, and does not touch the network.
func TestPersistCheckpoint_PostgresWithoutTenantWritesFileOnly(t *testing.T) {
	isolateWorkflowStorage(t)
	config.Storage.Backend = "postgres"
	config.Storage.Postgres.Host = "10.255.255.1" // unroutable: reaching for it would stall
	config.Storage.Postgres.Database = "loom"
	config.Storage.Postgres.DefaultUserID = "svc" // no longer a fallback

	core, logs := observer.New(zap.WarnLevel)
	path := t.TempDir() + "/test.checkpoint.pb"
	var opens atomic.Int32
	start := time.Now()
	stored, err := persistCheckpoint(context.Background(), realOpener(&opens), zap.New(core), path,
		&loomv1.WorkflowCheckpoint{WorkflowId: "wf-no-tenant"})
	if err != nil {
		t.Fatalf("persistCheckpoint: %v", err)
	}
	if stored {
		t.Fatal("storedInBackend = true with no tenant")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %v: the tenant check must come before any network call", elapsed)
	}
	if _, err := readCheckpointFile(path); err != nil {
		t.Fatalf("the file must still be written: %v", err)
	}
	if logs.Len() != 1 || !strings.Contains(logs.All()[0].ContextMap()["error"].(string), "--user is required") {
		t.Fatalf("want one warning naming --user, got %v", logs.All())
	}
}

func TestReadCheckpointFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.checkpoint.pb")
	data, err := proto.Marshal(&loomv1.WorkflowCheckpoint{WorkflowId: "wf-file-present-test"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(good, data, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	bad := filepath.Join(dir, "bad.checkpoint.pb")
	if err := os.WriteFile(bad, []byte("not a protobuf \xff\xff"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	got, err := readCheckpointFile(good)
	if err != nil || got.WorkflowId != "wf-file-present-test" {
		t.Fatalf("readCheckpointFile(good) = (%+v, %v), want workflow_id=wf-file-present-test", got, err)
	}
	if _, err := readCheckpointFile(bad); err == nil || !strings.Contains(err.Error(), "invalid checkpoint file") {
		t.Fatalf("readCheckpointFile(bad) error = %v, want an invalid-file error", err)
	}
	// A mistyped path is an error naming the path, never a workflow-ID lookup.
	if _, err := readCheckpointFile(filepath.Join(dir, "typo.pb")); err == nil || !strings.Contains(err.Error(), "typo.pb") {
		t.Fatalf("readCheckpointFile(missing) error = %v, want one naming the path", err)
	}
}

// TestLoadStoredCheckpoint_ScopedToTenant resumes by workflow ID across
// tenants: two tenants may use the same ID, and each reads only its own.
func TestLoadStoredCheckpoint_ScopedToTenant(t *testing.T) {
	isolateWorkflowStorage(t)
	migrateWorkflowStorage(t)

	workflowUser = "alice"
	saveStored(t, &loomv1.WorkflowCheckpoint{WorkflowId: "pipeline-3f2b9c1e", NextStageIndex: 1})
	workflowUser = "bob"
	saveStored(t, &loomv1.WorkflowCheckpoint{WorkflowId: "pipeline-3f2b9c1e", NextStageIndex: 2})
	workflowUser = "carol"
	var opens atomic.Int32

	if got, err := loadStoredCheckpoint(context.Background(), realOpener(&opens), "pipeline-3f2b9c1e"); err == nil {
		t.Fatalf("carol read %+v, a checkpoint she does not own", got)
	} else if !strings.Contains(err.Error(), "no checkpoint found") {
		t.Fatalf("carol's lookup error = %v, want a not-found error", err)
	}

	for user, want := range map[string]int32{"alice": 1, "bob": 2} {
		workflowUser = user
		got, err := loadStoredCheckpoint(context.Background(), realOpener(&opens), "pipeline-3f2b9c1e")
		if err != nil {
			t.Fatalf("%s: loadStoredCheckpoint: %v", user, err)
		}
		if got.NextStageIndex != want {
			t.Fatalf("%s read next_stage_index=%d, want %d (another tenant's checkpoint)", user, got.NextStageIndex, want)
		}
	}
}

// TestLoadStoredCheckpoint_Postgres runs the same cross-tenant lookup on a
// real Postgres, when TEST_POSTGRES_URL is set.
func TestLoadStoredCheckpoint_Postgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping PostgreSQL test")
	}
	isolateWorkflowStorage(t)
	config.Storage.Backend = "postgres"
	config.Storage.Postgres.DSN = dsn
	migrateWorkflowStorage(t)

	id := fmt.Sprintf("pipeline-%d", time.Now().UnixNano())
	alice, bob := fmt.Sprintf("alice-%d", time.Now().UnixNano()), fmt.Sprintf("bob-%d", time.Now().UnixNano())
	var opens atomic.Int32
	t.Cleanup(func() {
		for _, u := range []string{alice, bob} {
			workflowUser = u
			consumeStoredCheckpoint(context.Background(), realOpener(&opens), zap.NewNop(), id)
		}
	})

	workflowUser = alice
	saveStored(t, &loomv1.WorkflowCheckpoint{WorkflowId: id, NextStageIndex: 1})

	workflowUser = bob
	if got, err := loadStoredCheckpoint(context.Background(), realOpener(&opens), id); err == nil {
		t.Fatalf("bob read %+v, a checkpoint alice owns", got)
	}
	workflowUser = alice
	got, err := loadStoredCheckpoint(context.Background(), realOpener(&opens), id)
	if err != nil || got.NextStageIndex != 1 {
		t.Fatalf("alice: loadStoredCheckpoint = (%+v, %v), want her checkpoint", got, err)
	}
}

func TestConsumeStoredCheckpoint(t *testing.T) {
	isolateWorkflowStorage(t)
	migrateWorkflowStorage(t)
	saveStored(t, &loomv1.WorkflowCheckpoint{WorkflowId: "wf-consumed"})

	var opens atomic.Int32
	consumeStoredCheckpoint(context.Background(), realOpener(&opens), zap.NewNop(), "wf-consumed")

	if _, err := loadStoredCheckpoint(context.Background(), realOpener(&opens), "wf-consumed"); err == nil {
		t.Fatal("the checkpoint is still loadable after being consumed")
	}
	// Consuming again, or an ID that was never stored, is harmless.
	core, logs := observer.New(zap.WarnLevel)
	consumeStoredCheckpoint(context.Background(), realOpener(&opens), zap.New(core), "wf-consumed")
	if logs.Len() != 0 {
		t.Fatalf("consuming an absent checkpoint warned: %v", logs.All())
	}
}

func TestConsumeStoredCheckpoint_FailureWarnsButDoesNotFail(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	open := func(context.Context) (*checkpointHandle, error) { return nil, errors.New("connection refused") }

	consumeStoredCheckpoint(context.Background(), open, zap.New(core), "wf-1")

	if logs.Len() != 1 || !strings.Contains(logs.All()[0].Message, "can still be resumed with --workflow-id") {
		t.Fatalf("want one warning that the stored copy stays resumable, got %v", logs.All())
	}
}

func TestConsumeStoredCheckpoint_NoTenantIsSilent(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	open := func(context.Context) (*checkpointHandle, error) { return nil, errCheckpointTenantRequired }

	consumeStoredCheckpoint(context.Background(), open, zap.New(core), "wf-1")
	consumeStoredCheckpoint(context.Background(), nil, zap.New(core), "wf-1")

	if logs.Len() != 0 {
		t.Fatalf("got warnings %v: with no tenant nothing was stored, so there is nothing to report", logs.All())
	}
}

func TestValidateResumeSource(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		id      string
		wantErr bool
	}{
		{name: "file only", file: "wf.checkpoint.pb"},
		{name: "workflow id only", id: "pipeline-3f2b9c1e"},
		{name: "neither", wantErr: true},
		{name: "both", file: "wf.checkpoint.pb", id: "pipeline-3f2b9c1e", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateResumeSource(tt.file, tt.id); (err != nil) != tt.wantErr {
				t.Fatalf("validateResumeSource(%q, %q) error = %v, wantErr %v", tt.file, tt.id, err, tt.wantErr)
			}
		})
	}
}

// TestResumeFinished: a stored checkpoint is consumed when the workflow
// completes or is rejected, and kept when the resume fails or suspends again
// (the new suspension saves its own checkpoint under the same ID).
func TestResumeFinished(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "completed", err: nil, want: true},
		{name: "rejected", err: &orchestration.GateRejected{StageAgentID: "designer"}, want: true},
		{name: "wrapped rejection", err: fmt.Errorf("resume: %w", &orchestration.GateRejected{}), want: true},
		{name: "suspended again", err: &orchestration.WorkflowSuspended{Checkpoint: &loomv1.WorkflowCheckpoint{}}, want: false},
		{name: "failed", err: errors.New("stage 2: llm call failed"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resumeFinished(tt.err); got != tt.want {
				t.Fatalf("resumeFinished(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestWorkflowCheckpointUserID covers the tenant the workflow commands use for
// checkpoints in the storage backend, by the same rule as `looms hitl`:
// --user wins; postgres requires it (default_user_id is not a fallback); SQLite
// uses types.DefaultUserID like SQLite sessions do.
func TestWorkflowCheckpointUserID(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		pgUser  string
		flag    string
		want    string
		wantErr bool
	}{
		{name: "sqlite default", backend: "sqlite", want: types.DefaultUserID},
		{name: "unset backend means sqlite", backend: "", want: types.DefaultUserID},
		{name: "sqlite with flag", backend: "sqlite", flag: "alice", want: "alice"},
		{name: "postgres with flag", backend: "postgres", pgUser: "svc", flag: "alice", want: "alice"},
		{name: "postgres default_user_id is not a fallback", backend: "postgres", pgUser: "svc", wantErr: true},
		{name: "postgres with no tenant", backend: "postgres", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origConfig, origUser := config, workflowUser
			t.Cleanup(func() { config, workflowUser = origConfig, origUser })
			config = &Config{}
			config.Storage.Backend = tt.backend
			config.Storage.Postgres.DefaultUserID = tt.pgUser
			workflowUser = tt.flag

			got, err := workflowCheckpointUserID()
			if tt.wantErr {
				if !errors.Is(err, errCheckpointTenantRequired) {
					t.Fatalf("workflowCheckpointUserID() = (%q, %v), want errCheckpointTenantRequired", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("workflowCheckpointUserID(): %v", err)
			}
			if got != tt.want {
				t.Fatalf("workflowCheckpointUserID() = %q, want %q", got, tt.want)
			}
		})
	}
}

// gatedWorkflowYAML is a two-stage pipeline with a HITL gate on the first.
const gatedWorkflowYAML = `apiVersion: loom/v1
kind: Workflow
metadata:
  name: gated
spec:
  type: pipeline
  initial_prompt: "go"
  stages:
    - agent_id: designer
      prompt_template: "Design: {{previous}}"
      hitl_gate:
        prompt_template: "Review:\n{{output}}"
        request_type: approval
    - agent_id: executor
      prompt_template: "Execute: {{previous}}"
`

func gatedCheckpoint(workflowID string) *loomv1.WorkflowCheckpoint {
	return &loomv1.WorkflowCheckpoint{
		CheckpointVersion: 1,
		WorkflowId:        workflowID,
		PendingGate:       &loomv1.HITLGateRequest{StageAgentId: "designer", StageNumber: 1},
	}
}

func writeGatedWorkflow(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gated.yaml")
	if err := os.WriteFile(path, []byte(gatedWorkflowYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// setResumeFlags sets the resume command's package-level flag variables and
// restores them when the test ends.
func setResumeFlags(t *testing.T, reject bool, workflowID string) {
	t.Helper()
	origReject, origID := resumeReject, resumeWorkflowID
	t.Cleanup(func() { resumeReject, resumeWorkflowID = origReject, origID })
	resumeReject, resumeWorkflowID = reject, workflowID
}

// TestRunResume_RejectNeedsNoLLM checks that `looms workflow resume --reject`
// ends the workflow without building the LLM provider or any agent: a
// rejection runs no stage, so it must not depend on LLM credentials.
func TestRunResume_RejectNeedsNoLLM(t *testing.T) {
	isolateWorkflowStorage(t)
	setResumeFlags(t, true, "")

	workflowPath := writeGatedWorkflow(t)
	ckptPath := filepath.Join(t.TempDir(), "gated.checkpoint.pb")
	data, err := proto.Marshal(gatedCheckpoint("wf-reject"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ckptPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { runResume(nil, []string{workflowPath, ckptPath}) })

	if !strings.Contains(out, "Workflow rejected") {
		t.Fatalf("output does not report the rejection:\n%s", out)
	}
	if strings.Contains(out, "LLM Provider") {
		t.Fatalf("resume --reject built an LLM provider; output:\n%s", out)
	}
}

// TestRunResume_RejectByWorkflowIDConsumesTheStoredCheckpoint resumes from the
// storage backend with no file at all, and checks the rejection ends the
// workflow for good: the same ID cannot be resumed again.
func TestRunResume_RejectByWorkflowIDConsumesTheStoredCheckpoint(t *testing.T) {
	isolateWorkflowStorage(t)
	migrateWorkflowStorage(t)
	setResumeFlags(t, true, "pipeline-3f2b9c1e")
	saveStored(t, gatedCheckpoint("pipeline-3f2b9c1e"))

	out := captureStdout(t, func() { runResume(nil, []string{writeGatedWorkflow(t)}) })

	if !strings.Contains(out, "Workflow rejected") {
		t.Fatalf("output does not report the rejection:\n%s", out)
	}
	var opens atomic.Int32
	if got, err := loadStoredCheckpoint(context.Background(), realOpener(&opens), "pipeline-3f2b9c1e"); err == nil {
		t.Fatalf("the rejected workflow's checkpoint is still stored and resumable: %+v", got)
	}
}

// TestRunResume_RejectFromFileConsumesTheStoredCopy: the file and the stored
// copy are the same suspension, so finishing it from the file must also retire
// the copy another machine could resume.
func TestRunResume_RejectFromFileConsumesTheStoredCopy(t *testing.T) {
	isolateWorkflowStorage(t)
	migrateWorkflowStorage(t)
	setResumeFlags(t, true, "")

	ckpt := gatedCheckpoint("pipeline-aa11bb22")
	saveStored(t, ckpt)
	ckptPath := filepath.Join(t.TempDir(), "gated.checkpoint.pb")
	data, err := proto.Marshal(ckpt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ckptPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	captureStdout(t, func() { runResume(nil, []string{writeGatedWorkflow(t), ckptPath}) })

	var opens atomic.Int32
	if got, err := loadStoredCheckpoint(context.Background(), realOpener(&opens), "pipeline-aa11bb22"); err == nil {
		t.Fatalf("the stored copy is still resumable after the file was rejected: %+v", got)
	}
}

// TestWriteCheckpointAndPrintGate_OffersResumeByIDOnlyWhenStored checks the
// storedInBackend result reaches the output: the workflow-ID resume is printed
// (with the tenant) only when the store write landed.
func TestWriteCheckpointAndPrintGate_OffersResumeByIDOnlyWhenStored(t *testing.T) {
	origSuspend := workflowSuspendTo
	t.Cleanup(func() { workflowSuspendTo = origSuspend })

	t.Run("stored", func(t *testing.T) {
		isolateWorkflowStorage(t)
		migrateWorkflowStorage(t)
		workflowUser = "alice"
		workflowSuspendTo = filepath.Join(t.TempDir(), "wf.checkpoint.pb")
		var opens atomic.Int32
		rt := &workflowRuntime{logger: zap.NewNop(), openCheckpoints: realOpener(&opens)}

		out := captureStdout(t, func() { writeCheckpointAndPrintGate(rt, "wf.yaml", gatedCheckpoint("pipeline-3f2b9c1e")) })

		want := "looms workflow resume wf.yaml --workflow-id pipeline-3f2b9c1e --user alice --approve"
		if !strings.Contains(out, want) {
			t.Fatalf("output does not offer %q:\n%s", want, out)
		}
	})

	t.Run("store write failed", func(t *testing.T) {
		isolateWorkflowStorage(t) // never migrated
		workflowSuspendTo = filepath.Join(t.TempDir(), "wf.checkpoint.pb")
		var opens atomic.Int32
		rt := &workflowRuntime{logger: zap.NewNop(), openCheckpoints: realOpener(&opens)}

		out := captureStdout(t, func() { writeCheckpointAndPrintGate(rt, "wf.yaml", gatedCheckpoint("pipeline-3f2b9c1e")) })

		if strings.Contains(out, "--workflow-id") {
			t.Fatalf("output offers a resume by workflow ID that cannot work:\n%s", out)
		}
		if !strings.Contains(out, "--approve") {
			t.Fatalf("output lost the file resume instructions:\n%s", out)
		}
	})
}

// captureStdout returns everything fn writes to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = orig }()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}
