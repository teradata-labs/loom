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

package apprentice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/task"
)

var testEpoch = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

// fakeReader is an in-memory Reader so the deterministic half of the
// distiller is testable without a database.
type fakeReader struct {
	tasks   []*task.Task
	total   int
	deps    map[string][]*task.TaskDependency
	listErr error
	depErr  error
}

func (f *fakeReader) ListTasks(_ context.Context, _ task.ListTasksOpts) ([]*task.Task, int, error) {
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	total := f.total
	if total == 0 {
		total = len(f.tasks)
	}
	return f.tasks, total, nil
}

func (f *fakeReader) GetDependencies(_ context.Context, taskID string) ([]*task.TaskDependency, error) {
	if f.depErr != nil {
		return nil, f.depErr
	}
	return f.deps[taskID], nil
}

// mkTask builds a task at a deterministic creation time. offset drives both
// CreatedAt and the default title so ordering assertions read clearly.
func mkTask(id string, offset int) *task.Task {
	return &task.Task{
		ID:        id,
		Title:     "step " + id,
		Status:    loomv1.TaskStatus_TASK_STATUS_OPEN,
		Priority:  loomv1.TaskPriority_TASK_PRIORITY_MEDIUM,
		Category:  loomv1.TaskCategory_TASK_CATEGORY_OTHER,
		CreatedAt: testEpoch.Add(time.Duration(offset) * time.Minute),
	}
}

// withKey attaches the emitter's idempotency key for a step index.
func withKey(t *task.Task, skill, session string, idx int) *task.Task {
	t.SkillIdempotencyKey = fmt.Sprintf("skill:%s|sess:%s|step:%d", skill, session, idx)
	return t
}

// blocks records "from depends on to", matching GetDependencies semantics.
func blocks(from, to string) *task.TaskDependency {
	return &task.TaskDependency{
		FromTaskID: from,
		ToTaskID:   to,
		Type:       loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS,
	}
}

func titles(r *Result) []string {
	out := make([]string, 0, len(r.Template.Steps))
	for _, s := range r.Template.Steps {
		out = append(out, s.Title)
	}
	return out
}

func TestDistill_NilReader(t *testing.T) {
	_, err := Distill(context.Background(), nil, Options{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "non-nil Reader")
}

func TestDistill_EmptyBoard(t *testing.T) {
	res, err := Distill(context.Background(), &fakeReader{}, Options{BoardID: "b1"})
	require.NoError(t, err)
	require.NotNil(t, res.Template, "template must never be nil on success")
	require.Empty(t, res.Template.Steps)
	require.Len(t, res.Warnings, 1)
	require.Contains(t, res.Warnings[0], "no tasks")
}

func TestDistill_KeyedOrderIsExact(t *testing.T) {
	// Listed out of order on purpose, and with creation times that
	// contradict the step indices, so only the key can produce 0,1,2.
	f := &fakeReader{tasks: []*task.Task{
		withKey(mkTask("c", 0), "s", "sess", 2),
		withKey(mkTask("a", 5), "s", "sess", 0),
		withKey(mkTask("b", 3), "s", "sess", 1),
	}}

	res, err := Distill(context.Background(), f, Options{BoardID: "b1"})
	require.NoError(t, err)
	require.Equal(t, OrderIdempotencyKey, res.OrderSource)
	require.Equal(t, []string{"step a", "step b", "step c"}, titles(res))
	require.Equal(t, []string{"a", "b", "c"}, res.TaskIDs)
	require.Empty(t, res.Warnings)
}

func TestDistill_MetadataStepIndexFallback(t *testing.T) {
	// No idempotency key, but the emitter's redundant metadata channel is
	// present and must still yield exact order.
	a, b := mkTask("a", 5), mkTask("b", 0)
	a.Metadata = map[string]string{metadataStepKey: "0"}
	b.Metadata = map[string]string{metadataStepKey: "1"}

	res, err := Distill(context.Background(), &fakeReader{tasks: []*task.Task{b, a}}, Options{})
	require.NoError(t, err)
	require.Equal(t, OrderIdempotencyKey, res.OrderSource)
	require.Equal(t, []string{"step a", "step b"}, titles(res))
}

func TestDistill_PartialKeysFallBackToTopological(t *testing.T) {
	// One task has no step index. Mixing keyed and unkeyed tasks would
	// silently interleave them, so recovery must fall back wholesale.
	f := &fakeReader{tasks: []*task.Task{
		withKey(mkTask("a", 0), "s", "sess", 0),
		mkTask("b", 1),
	}}

	res, err := Distill(context.Background(), f, Options{})
	require.NoError(t, err)
	require.Equal(t, OrderTopological, res.OrderSource)
}

func TestDistill_DuplicateKeysFallBackToTopological(t *testing.T) {
	f := &fakeReader{tasks: []*task.Task{
		withKey(mkTask("a", 0), "s", "sess", 1),
		withKey(mkTask("b", 1), "s", "sess", 1),
	}}

	res, err := Distill(context.Background(), f, Options{})
	require.NoError(t, err)
	require.Equal(t, OrderTopological, res.OrderSource)
}

func TestDistill_IgnoreKeysForcesTopological(t *testing.T) {
	// Keys say c,a,b; the DAG says a -> b -> c. IgnoreKeys must honour the
	// DAG, which is what the round-trip oracle relies on to exercise the
	// inference path.
	f := &fakeReader{
		tasks: []*task.Task{
			withKey(mkTask("c", 2), "s", "sess", 0),
			withKey(mkTask("a", 0), "s", "sess", 1),
			withKey(mkTask("b", 1), "s", "sess", 2),
		},
		deps: map[string][]*task.TaskDependency{
			"b": {blocks("b", "a")},
			"c": {blocks("c", "b")},
		},
	}

	res, err := Distill(context.Background(), f, Options{IgnoreKeys: true})
	require.NoError(t, err)
	require.Equal(t, OrderTopological, res.OrderSource)
	require.Equal(t, []string{"step a", "step b", "step c"}, titles(res))
}

func TestDistill_TopologicalRecoversDependsOn(t *testing.T) {
	// Diamond: b and c both depend on a; d depends on b and c.
	f := &fakeReader{
		tasks: []*task.Task{mkTask("d", 3), mkTask("c", 2), mkTask("b", 1), mkTask("a", 0)},
		deps: map[string][]*task.TaskDependency{
			"b": {blocks("b", "a")},
			"c": {blocks("c", "a")},
			"d": {blocks("d", "c"), blocks("d", "b")},
		},
	}

	res, err := Distill(context.Background(), f, Options{})
	require.NoError(t, err)
	require.Equal(t, []string{"step a", "step b", "step c", "step d"}, titles(res))

	steps := res.Template.Steps
	require.Nil(t, steps[0].DependsOn)
	require.Equal(t, []int32{0}, steps[1].DependsOn)
	require.Equal(t, []int32{0}, steps[2].DependsOn)
	require.Equal(t, []int32{1, 2}, steps[3].DependsOn, "depends_on must be sorted for determinism")

	// b and c are ready simultaneously, so their relative order is not
	// evidence. That must be surfaced, not hidden.
	require.True(t, hasWarning(res.Warnings, "unconstrained"),
		"a fan-out must report unconstrained ordering: %v", res.Warnings)
}

func TestDistill_StrictChainIsFullyConstrained(t *testing.T) {
	// a -> b -> c leaves no choice at any point, so recovery is unambiguous
	// and must not warn.
	f := &fakeReader{
		tasks: []*task.Task{mkTask("c", 2), mkTask("b", 1), mkTask("a", 0)},
		deps: map[string][]*task.TaskDependency{
			"b": {blocks("b", "a")},
			"c": {blocks("c", "b")},
		},
	}

	res, err := Distill(context.Background(), f, Options{})
	require.NoError(t, err)
	require.Equal(t, []string{"step a", "step b", "step c"}, titles(res))
	require.Empty(t, res.Warnings, "a strict chain is fully determined: %v", res.Warnings)
}

func TestDistill_TieBreakIgnoresContentHashIDs(t *testing.T) {
	// Two independent tasks sharing a creation timestamp. IDs are content
	// hashes, so ordering by ID would be stable but arbitrary; board read
	// order is the meaningful fallback.
	first, second := mkTask("zzz-hashes-last", 0), mkTask("aaa-hashes-first", 0)
	f := &fakeReader{tasks: []*task.Task{first, second}}

	res, err := Distill(context.Background(), f, Options{})
	require.NoError(t, err)
	require.Equal(t, []string{"step zzz-hashes-last", "step aaa-hashes-first"}, titles(res),
		"tie-break must follow board read order, not ID")
	require.True(t, hasWarning(res.Warnings, "unconstrained"))
}

func TestDistill_OffBoardDependencyDroppedWithWarning(t *testing.T) {
	f := &fakeReader{
		tasks: []*task.Task{mkTask("a", 0)},
		deps:  map[string][]*task.TaskDependency{"a": {blocks("a", "elsewhere")}},
	}

	res, err := Distill(context.Background(), f, Options{})
	require.NoError(t, err)
	require.Len(t, res.Template.Steps, 1)
	require.Nil(t, res.Template.Steps[0].DependsOn)
	require.Len(t, res.Warnings, 1)
	require.Contains(t, res.Warnings[0], "not on this board")
}

func TestDistill_SelfDependencyIgnored(t *testing.T) {
	f := &fakeReader{
		tasks: []*task.Task{mkTask("a", 0)},
		deps:  map[string][]*task.TaskDependency{"a": {blocks("a", "a")}},
	}

	res, err := Distill(context.Background(), f, Options{})
	require.NoError(t, err)
	require.Nil(t, res.Template.Steps[0].DependsOn)
	require.Empty(t, res.Warnings, "a self-edge is not a recovery problem")
}

func TestDistill_CycleTerminatesWithWarning(t *testing.T) {
	// Manager.AddDependency rejects cycles, so this board came from
	// elsewhere. Recovery must terminate and keep every task.
	f := &fakeReader{
		tasks: []*task.Task{mkTask("a", 0), mkTask("b", 1)},
		deps: map[string][]*task.TaskDependency{
			"a": {blocks("a", "b")},
			"b": {blocks("b", "a")},
		},
	}

	res, err := Distill(context.Background(), f, Options{})
	require.NoError(t, err)
	require.Len(t, res.Template.Steps, 2, "no task may be dropped on a cycle")
	require.Equal(t, []string{"step a", "step b"}, titles(res))

	require.True(t, hasWarning(res.Warnings, "cycle"),
		"a cycle must be reported: %v", res.Warnings)
}

func TestDistill_FieldMapping(t *testing.T) {
	src := mkTask("a", 0)
	src.Title = "Profile the source table"
	src.Objective = "Know the row count and skew"
	src.AcceptanceCriteria = "Row count and top-10 skew recorded"
	src.EstimatedEffort = "10 min"
	src.Tags = []string{"teradata", "profiling"}
	src.Category = loomv1.TaskCategory_TASK_CATEGORY_ANALYSIS
	src.Priority = loomv1.TaskPriority_TASK_PRIORITY_HIGH

	res, err := Distill(context.Background(), &fakeReader{tasks: []*task.Task{src}}, Options{})
	require.NoError(t, err)

	got := res.Template.Steps[0]
	require.Equal(t, "Profile the source table", got.Title)
	require.Equal(t, "Know the row count and skew", got.Objective)
	require.Equal(t, "Row count and top-10 skew recorded", got.AcceptanceCriteria)
	require.Equal(t, "10 min", got.EstimatedEffort)
	require.Equal(t, []string{"teradata", "profiling"}, got.Tags)
	require.Equal(t, "analysis", got.Category)
	require.Equal(t, "P1", got.Priority)
}

func TestDistill_TagsAreCopiedNotAliased(t *testing.T) {
	src := mkTask("a", 0)
	src.Tags = []string{"one"}

	res, err := Distill(context.Background(), &fakeReader{tasks: []*task.Task{src}}, Options{})
	require.NoError(t, err)

	src.Tags[0] = "mutated"
	require.Equal(t, []string{"one"}, res.Template.Steps[0].Tags,
		"recovered tags must not alias the source task's slice")
}

func TestDistill_TruncatedReadWarns(t *testing.T) {
	f := &fakeReader{tasks: []*task.Task{mkTask("a", 0)}, total: 900}

	res, err := Distill(context.Background(), f, Options{})
	require.NoError(t, err)

	require.True(t, hasWarning(res.Warnings, "partial"),
		"a truncated read must be reported: %v", res.Warnings)
}

func TestDistill_ExceedingEmitCapWarns(t *testing.T) {
	var tasks []*task.Task
	for i := 0; i < 12; i++ {
		tasks = append(tasks, mkTask(fmt.Sprintf("t%02d", i), i))
	}

	res, err := Distill(context.Background(), &fakeReader{tasks: tasks}, Options{})
	require.NoError(t, err)
	require.Len(t, res.Template.Steps, 12)

	require.True(t, hasWarning(res.Warnings, "max_tasks"),
		"exceeding the emitter cap must be reported: %v", res.Warnings)
}

func TestDistill_ReaderErrorsPropagate(t *testing.T) {
	sentinel := errors.New("boom")

	_, err := Distill(context.Background(), &fakeReader{listErr: sentinel}, Options{})
	require.ErrorIs(t, err, sentinel)

	_, err = Distill(context.Background(),
		&fakeReader{tasks: []*task.Task{mkTask("a", 0)}, depErr: sentinel}, Options{})
	require.ErrorIs(t, err, sentinel)
}

// TestEnumFormattersReparse is the property the round-trip oracle leans on:
// whatever formatCategory/formatPriority emit must parse back to the same
// enum, or a recovered template silently changes meaning on re-emission.
func TestEnumFormattersReparse(t *testing.T) {
	categories := []loomv1.TaskCategory{
		loomv1.TaskCategory_TASK_CATEGORY_RESEARCH,
		loomv1.TaskCategory_TASK_CATEGORY_ANALYSIS,
		loomv1.TaskCategory_TASK_CATEGORY_IMPLEMENTATION,
		loomv1.TaskCategory_TASK_CATEGORY_REVIEW,
		loomv1.TaskCategory_TASK_CATEGORY_WRITING,
		loomv1.TaskCategory_TASK_CATEGORY_DECISION,
		loomv1.TaskCategory_TASK_CATEGORY_INVESTIGATION,
		loomv1.TaskCategory_TASK_CATEGORY_PLANNING,
		loomv1.TaskCategory_TASK_CATEGORY_OTHER,
		loomv1.TaskCategory_TASK_CATEGORY_UNSPECIFIED,
	}
	for _, c := range categories {
		t.Run("category_"+c.String(), func(t *testing.T) {
			got := task.ParseCategory(formatCategory(c))
			want := c
			if want == loomv1.TaskCategory_TASK_CATEGORY_UNSPECIFIED {
				// ParseCategory has no UNSPECIFIED output; OTHER is the
				// documented floor.
				want = loomv1.TaskCategory_TASK_CATEGORY_OTHER
			}
			require.Equal(t, want, got)
		})
	}

	priorities := []loomv1.TaskPriority{
		loomv1.TaskPriority_TASK_PRIORITY_CRITICAL,
		loomv1.TaskPriority_TASK_PRIORITY_HIGH,
		loomv1.TaskPriority_TASK_PRIORITY_MEDIUM,
		loomv1.TaskPriority_TASK_PRIORITY_LOW,
		loomv1.TaskPriority_TASK_PRIORITY_BACKLOG,
		loomv1.TaskPriority_TASK_PRIORITY_UNSPECIFIED,
	}
	for _, p := range priorities {
		t.Run("priority_"+p.String(), func(t *testing.T) {
			got := task.ParsePriority(formatPriority(p))
			want := p
			if want == loomv1.TaskPriority_TASK_PRIORITY_UNSPECIFIED {
				// ParsePriority defaults to MEDIUM.
				want = loomv1.TaskPriority_TASK_PRIORITY_MEDIUM
			}
			require.Equal(t, want, got)
		})
	}
}

func TestStepIndexOf_MalformedKeys(t *testing.T) {
	cases := []struct {
		name string
		key  string
		meta map[string]string
		want bool
	}{
		{name: "well formed", key: "skill:s|sess:x|step:3", want: true},
		{name: "no step segment", key: "skill:s|sess:x", want: false},
		{name: "non numeric", key: "skill:s|sess:x|step:abc", want: false},
		{name: "negative", key: "skill:s|sess:x|step:-1", want: false},
		{name: "empty", key: "", want: false},
		{name: "metadata rescues bad key", key: "skill:s|sess:x|step:abc",
			meta: map[string]string{metadataStepKey: " 2 "}, want: true},
		{name: "metadata non numeric", key: "",
			meta: map[string]string{metadataStepKey: "nope"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tk := &task.Task{SkillIdempotencyKey: tc.key, Metadata: tc.meta}
			_, ok := stepIndexOf(tk)
			require.Equal(t, tc.want, ok)
		})
	}
}

// hasWarning reports whether any warning mentions substr.
func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
