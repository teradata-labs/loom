package orchestration

import (
	"context"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// fakeCheckpointStore is an in-memory CheckpointStore used only to prove the
// interface shape is implementable and usable by callers in this package.
type fakeCheckpointStore struct {
	saved map[string]*loomv1.WorkflowCheckpoint
}

func newFakeCheckpointStore() *fakeCheckpointStore {
	return &fakeCheckpointStore{saved: make(map[string]*loomv1.WorkflowCheckpoint)}
}

func (f *fakeCheckpointStore) SaveCheckpoint(_ context.Context, ckpt *loomv1.WorkflowCheckpoint) error {
	f.saved[ckpt.WorkflowId] = ckpt
	return nil
}

func (f *fakeCheckpointStore) LoadCheckpoint(_ context.Context, workflowID string) (*loomv1.WorkflowCheckpoint, error) {
	return f.saved[workflowID], nil // nil, nil on a miss — matches map zero value
}

var _ CheckpointStore = (*fakeCheckpointStore)(nil)

func TestCheckpointStore_SaveThenLoad_RoundTrips(t *testing.T) {
	store := newFakeCheckpointStore()
	ctx := context.Background()
	ckpt := &loomv1.WorkflowCheckpoint{
		WorkflowId:        "wf-123",
		CheckpointVersion: 1,
		NextStageIndex:    2,
	}

	if err := store.SaveCheckpoint(ctx, ckpt); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	got, err := store.LoadCheckpoint(ctx, "wf-123")
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if got == nil || got.WorkflowId != "wf-123" || got.NextStageIndex != 2 {
		t.Fatalf("LoadCheckpoint: got %+v, want workflow_id=wf-123 next_stage_index=2", got)
	}
}

func TestCheckpointStore_LoadMissing_ReturnsNilNil(t *testing.T) {
	store := newFakeCheckpointStore()
	got, err := store.LoadCheckpoint(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("LoadCheckpoint: got error %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("LoadCheckpoint: got %+v, want nil for a missing checkpoint", got)
	}
}
