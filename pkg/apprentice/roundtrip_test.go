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

// This file is the round-trip oracle: the P0 correctness gate for the
// apprentice, and a permanent regression suite thereafter.
//
//	shipped skill with an authored TaskTemplate
//	  -> pkg/skills/tasks emitter -> real task board (sqlite)
//	  -> Distill -> recovered SkillTaskTemplate
//	  -> structural diff against the original
//
// It runs against the real emitter and a real store rather than a hand-rolled
// mapping, because a fake would only assert this package's assumption about
// how the emitter behaves. The corpus is every embedded skill that authors a
// task_template, so it grows for free as skills land.
//
// Each skill is checked twice, and the two modes assert deliberately different
// things:
//
//   - keyed: the emitter's step indices are available, so recovery is exact
//     and every field plus the whole sequence is compared.
//   - topological: IgnoreKeys forces order to be inferred from the dependency
//     DAG, the path real non-skill-emitted work takes. Here exact sequence is
//     NOT assertable, because two of the three shipped templates fan out
//     (weaver-templates steps 4 and 5 both depend on 3; weaver-from-scratch
//     steps 1 and 2 both depend on 0). When two steps are ready at once the
//     board holds no record of which ran first, so the assertion is that the
//     recovered order is *a* valid topological order over the same steps.
//
// That asymmetry is a finding, not a workaround: authored step order survives
// only where step indices do. Anything reading a real board gets an ordering
// consistent with the dependencies and a warning saying so.
package apprentice

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/embedded"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/skills"
	skilltasks "github.com/teradata-labs/loom/pkg/skills/tasks"
	sqlitestore "github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
)

// newRoundTripManager builds a task.Manager over a migrated temp sqlite
// database, mirroring the emitter package's own test harness.
func newRoundTripManager(t *testing.T) *task.Manager {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "apprentice.db")
	db, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mig, err := sqlitestore.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, mig.MigrateUp(context.Background()))

	store := sqlitestore.NewTaskStore(db, observability.NewNoOpTracer())
	return task.NewManager(store, nil, observability.NewNoOpTracer(), nil)
}

// loadEmbeddedSkill parses embedded skill YAML through the real loader, so
// the oracle exercises the same parse path production uses.
func loadEmbeddedSkill(t *testing.T, name string, raw []byte) *skills.Skill {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".yaml")
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	s, err := skills.LoadSkill(path)
	require.NoError(t, err, "embedded skill %s must parse", name)
	require.NotNil(t, s.TaskTemplate, "%s must author a task_template", name)
	require.NotEmpty(t, s.TaskTemplate.Steps, "%s must author steps", name)
	return s
}

// emitTemplate runs the real emitter and returns the board it wrote to.
func emitTemplate(t *testing.T, m *task.Manager, s *skills.Skill, boardID string) {
	t.Helper()
	e := skilltasks.NewEmitter(m, nil)
	res, err := e.EmitForActivation(context.Background(), skilltasks.EmitRequest{
		Skill:             s,
		SessionID:         "sess-roundtrip",
		AgentID:           "apprentice-test",
		BoardID:           boardID,
		AgentTasksEnabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, "template", res.Source, "authored template must take the template path")
	require.Len(t, res.Tasks, expectedStepCount(s.TaskTemplate))
}

// expectedStepCount applies the emitter's max_tasks cap the same way the
// emitter does, so the oracle compares against what was actually emitted.
func expectedStepCount(tmpl *skills.SkillTaskTemplate) int {
	cap := int(tmpl.MaxTasks)
	if cap <= 0 {
		cap = skilltasks.DefaultMaxTasks
	}
	if len(tmpl.Steps) < cap {
		return len(tmpl.Steps)
	}
	return cap
}

func TestRoundTrip_ShippedSkillTemplates(t *testing.T) {
	corpus := []struct {
		name string
		raw  []byte
	}{
		{name: "weaver-presets", raw: embedded.GetWeaverPresetsSkill()},
		{name: "weaver-templates", raw: embedded.GetWeaverTemplatesSkill()},
		{name: "weaver-from-scratch", raw: embedded.GetWeaverFromScratchSkill()},
	}

	for _, tc := range corpus {
		for _, ignoreKeys := range []bool{false, true} {
			mode := "keyed"
			if ignoreKeys {
				mode = "topological"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				m := newRoundTripManager(t)
				skill := loadEmbeddedSkill(t, tc.name, tc.raw)
				boardID := "board-" + tc.name

				emitTemplate(t, m, skill, boardID)

				got, err := Distill(context.Background(), NewManagerReader(m), Options{
					BoardID:    boardID,
					IgnoreKeys: ignoreKeys,
				})
				require.NoError(t, err)

				if ignoreKeys {
					require.Equal(t, OrderTopological, got.OrderSource)
					assertValidTopologicalRecovery(t, skill, got)
					return
				}

				require.Equal(t, OrderIdempotencyKey, got.OrderSource)
				require.Empty(t, got.Warnings,
					"keyed recovery of a clean emit must not warn: %v", got.Warnings)
				assertTemplateRecovered(t, skill, got)
			})
		}
	}
}

// TestRoundTrip_ReEmissionIsAFixpoint checks the property that actually
// matters for deputizing: emitting a recovered template must reproduce the
// same board, so distill(emit(distill(emit(T)))) stops changing. A template
// that drifts on every generation would corrupt a skill each time it ran.
func TestRoundTrip_ReEmissionIsAFixpoint(t *testing.T) {
	m := newRoundTripManager(t)
	original := loadEmbeddedSkill(t, "weaver-presets", embedded.GetWeaverPresetsSkill())

	emitTemplate(t, m, original, "board-gen1")
	gen1, err := Distill(context.Background(), NewManagerReader(m), Options{BoardID: "board-gen1"})
	require.NoError(t, err)

	// Re-emit the recovered template as a distinct skill so idempotency keys
	// do not collide with generation 1.
	recovered := &skills.Skill{
		Name:         original.Name + "-recovered",
		Title:        original.Title,
		Prompt:       original.Prompt,
		TaskTemplate: gen1.Template,
	}
	emitTemplate(t, m, recovered, "board-gen2")
	gen2, err := Distill(context.Background(), NewManagerReader(m), Options{BoardID: "board-gen2"})
	require.NoError(t, err)

	require.Equal(t, gen1.Template.Steps, gen2.Template.Steps,
		"a recovered template must survive re-emission unchanged")
}

// TestRoundTrip_UnrecoverableTemplateFields pins the fields a board cannot
// carry, so the gap is visible rather than mistaken for a distiller bug.
//
// root_title is the notable one: SkillTaskTemplate documents it as "names the
// parent task created to group emitted children", but emitTemplate never
// creates that parent and never sets ParentID — so nothing on the board holds
// it. All three shipped templates set root_title and all three lose it here.
// If the emitter gains root-task support, this test should start failing and
// the distiller should learn to recover it.
func TestRoundTrip_UnrecoverableTemplateFields(t *testing.T) {
	m := newRoundTripManager(t)
	skill := loadEmbeddedSkill(t, "weaver-presets", embedded.GetWeaverPresetsSkill())
	require.NotEmpty(t, skill.TaskTemplate.RootTitle, "fixture must author a root_title")

	emitTemplate(t, m, skill, "board-fields")
	got, err := Distill(context.Background(), NewManagerReader(m), Options{BoardID: "board-fields"})
	require.NoError(t, err)

	require.Empty(t, got.Template.RootTitle,
		"root_title is not represented on the board; the emitter creates no parent task")
	require.Zero(t, got.Template.MaxTasks,
		"max_tasks is an emission cap, not board state")
	require.False(t, got.Template.EphemeralOnDeactivate,
		"ephemeral_on_deactivate is activation policy, not board state")
}

// assertValidTopologicalRecovery checks what a board can actually prove when
// step indices are unavailable: the same steps came back, and every authored
// dependency is respected in the recovered order.
//
// It deliberately does not assert the authored sequence. For a fan-out the
// board holds no evidence of which branch ran first, so demanding the authored
// order would be asserting information that was never recorded.
func assertValidTopologicalRecovery(t *testing.T, want *skills.Skill, got *Result) {
	t.Helper()

	wantSteps := want.TaskTemplate.Steps[:expectedStepCount(want.TaskTemplate)]
	require.Len(t, got.Template.Steps, len(wantSteps))

	// Same steps, order aside. Titles are unique across all three fixtures.
	position := make(map[string]int, len(got.Template.Steps))
	for i, s := range got.Template.Steps {
		require.NotContains(t, position, s.Title, "duplicate step title %q", s.Title)
		position[s.Title] = i
	}
	for _, w := range wantSteps {
		require.Contains(t, position, w.Title, "step %q went missing", w.Title)
	}

	// Every authored edge must hold: a step appears after everything it
	// depends on.
	for i, w := range wantSteps {
		for _, dep := range w.DependsOn {
			if dep < 0 || int(dep) >= len(wantSteps) || dep == int32(i) {
				continue
			}
			require.Less(t, position[wantSteps[dep].Title], position[w.Title],
				"%q depends on %q and must follow it", w.Title, wantSteps[dep].Title)
		}
	}

	// Recovered depends_on indices must point strictly backwards, or the
	// template would be unusable when re-emitted.
	for i, s := range got.Template.Steps {
		for _, dep := range s.DependsOn {
			require.Less(t, int(dep), i, "step %d depends forwards on %d", i, dep)
		}
	}
}

// assertTemplateRecovered compares a recovered template against the authored
// one step by step.
//
// Category and Priority are compared through task.Parse* rather than as
// strings: the emitter maps them into enums on the way in, and that mapping is
// lossy for empty and alias values ("" and "other" both mean OTHER; "" and
// "P2" both mean MEDIUM). Semantic equality is the strongest claim the board
// supports, and string equality would fail for reasons that do not matter.
func assertTemplateRecovered(t *testing.T, want *skills.Skill, got *Result) {
	t.Helper()

	wantSteps := want.TaskTemplate.Steps[:expectedStepCount(want.TaskTemplate)]
	require.Len(t, got.Template.Steps, len(wantSteps))
	require.Len(t, got.TaskIDs, len(wantSteps), "every step must trace back to a task")

	for i, w := range wantSteps {
		g := got.Template.Steps[i]
		label := fmt.Sprintf("step %d (%q)", i, w.Title)

		wantTitle := w.Title
		if wantTitle == "" {
			// Mirrors the emitter's fallback title.
			wantTitle = fmt.Sprintf("%s step %d", want.Title, i+1)
		}
		require.Equal(t, wantTitle, g.Title, label)
		require.Equal(t, w.Objective, g.Objective, label)
		require.Equal(t, w.AcceptanceCriteria, g.AcceptanceCriteria, label)
		require.Equal(t, w.EstimatedEffort, g.EstimatedEffort, label)
		require.Equal(t, task.ParseCategory(w.Category), task.ParseCategory(g.Category), label)
		require.Equal(t, task.ParsePriority(w.Priority), task.ParsePriority(g.Priority), label)

		if len(w.Tags) == 0 {
			require.Empty(t, g.Tags, label)
		} else {
			require.Equal(t, w.Tags, g.Tags, label)
		}

		require.Equal(t, expectedDependsOn(w, i, len(wantSteps)), g.DependsOn, label)
	}
}

// expectedDependsOn applies the same filtering the emitter does when wiring
// edges: out-of-range and self references are skipped, and the result is
// sorted because Distill sorts for determinism.
func expectedDependsOn(step skills.SkillTaskStep, index, total int) []int32 {
	var out []int32
	seen := make(map[int32]bool, len(step.DependsOn))
	for _, dep := range step.DependsOn {
		if dep < 0 || int(dep) >= total || dep == int32(index) || seen[dep] {
			continue
		}
		seen[dep] = true
		out = append(out, dep)
	}
	if len(out) == 0 {
		return nil
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
