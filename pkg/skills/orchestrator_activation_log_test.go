//go:build fts5

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

package skills

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func activeNames(as []*ActiveSkill) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Skill.Name)
	}
	return out
}

func TestSkillsActiveSince(t *testing.T) {
	alpha := &Skill{Name: "alpha"}
	beta := &Skill{Name: "beta"}
	gamma := &Skill{Name: "gamma"}

	tests := []struct {
		name string
		// run performs activity; it receives the turn-start mark.
		run  func(o *Orchestrator, mark func() time.Time)
		want []string
	}{
		{
			name: "active skills are reported",
			run: func(o *Orchestrator, mark func() time.Time) {
				mark()
				o.ActivatePinned("s", alpha, "manual", "", 1)
			},
			want: []string{"alpha"},
		},
		{
			name: "deactivated during the window is still reported",
			run: func(o *Orchestrator, mark func() time.Time) {
				mark()
				o.ActivatePinned("s", alpha, "manual", "", 1)
				o.DeactivateSkill("s", "alpha")
			},
			want: []string{"alpha"},
		},
		{
			name: "active before the window and deactivated during it is reported",
			run: func(o *Orchestrator, mark func() time.Time) {
				o.ActivatePinned("s", alpha, "manual", "", 1)
				mark()
				o.DeactivateSkill("s", "alpha")
			},
			want: []string{"alpha"},
		},
		{
			name: "deactivated before the window is not reported",
			run: func(o *Orchestrator, mark func() time.Time) {
				o.ActivatePinned("s", alpha, "manual", "", 1)
				o.DeactivateSkill("s", "alpha")
				mark()
				o.ActivatePinned("s", beta, "manual", "", 1)
			},
			want: []string{"beta"},
		},
		{
			name: "replacement reports one entry per name",
			run: func(o *Orchestrator, mark func() time.Time) {
				mark()
				o.ActivatePinned("s", alpha, "manual", "", 1)
				o.ActivatePinned("s", alpha, "slash", "", 1)
				o.ActivateSkill("s", alpha, "router", "", 0.5)
			},
			want: []string{"alpha"},
		},
		{
			name: "evicted during the window is still reported",
			run: func(o *Orchestrator, mark func() time.Time) {
				o.SetMaxConcurrentSkills(1)
				mark()
				o.ActivateSkill("s", alpha, "router", "", 0.1)
				o.ActivateSkill("s", beta, "router", "", 0.9) // evicts alpha
			},
			want: []string{"alpha", "beta"},
		},
		{
			name: "other sessions are isolated",
			run: func(o *Orchestrator, mark func() time.Time) {
				mark()
				o.ActivatePinned("other", gamma, "manual", "", 1)
			},
			want: []string{},
		},
		{
			name: "cleanup forgets the session",
			run: func(o *Orchestrator, mark func() time.Time) {
				mark()
				o.ActivatePinned("s", alpha, "manual", "", 1)
				o.CleanupSession("s")
			},
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := newTestOrchestrator(alpha, beta, gamma)
			var since time.Time
			mark := func() time.Time {
				// Separate the mark from surrounding timestamps on coarse clocks.
				time.Sleep(2 * time.Millisecond)
				since = time.Now()
				time.Sleep(2 * time.Millisecond)
				return since
			}
			tt.run(o, mark)
			require.False(t, since.IsZero(), "test must set the window start")
			assert.ElementsMatch(t, tt.want, activeNames(o.SkillsActiveSince("s", since)))
		})
	}
}

func TestSkillsActiveSince_OrderAndLatestActivation(t *testing.T) {
	o := newTestOrchestrator()
	since := time.Now()
	o.ActivatePinned("s", &Skill{Name: "first"}, "manual", "", 1)
	time.Sleep(2 * time.Millisecond)
	o.ActivatePinned("s", &Skill{Name: "second"}, "manual", "", 1)
	time.Sleep(2 * time.Millisecond)
	o.ActivatePinned("s", &Skill{Name: "first"}, "slash", "v2", 1)

	got := o.SkillsActiveSince("s", since)
	require.Len(t, got, 2)
	assert.Equal(t, []string{"second", "first"}, activeNames(got), "ordered by latest activation")
	assert.Equal(t, "slash", got[1].TriggerType, "the latest activation of a name wins")
}

func TestActivationLog_BoundedKeepsOpenRecords(t *testing.T) {
	o := newTestOrchestrator()
	pinned := &Skill{Name: "pinned"}
	o.ActivatePinned("s", pinned, "manual", "", 1)
	for i := 0; i < maxActivationLogPerSession*2; i++ {
		name := fmt.Sprintf("churn-%d", i)
		o.ActivatePinned("s", &Skill{Name: name}, "manual", "", 1)
		o.DeactivateSkill("s", name)
	}

	o.mu.RLock()
	n := len(o.activationLog["s"])
	o.mu.RUnlock()
	assert.LessOrEqual(t, n, maxActivationLogPerSession)
	assert.Contains(t, activeNames(o.SkillsActiveSince("s", time.Time{})), "pinned",
		"an open record is never dropped by the bound")
}

func TestSkillsActiveSince_Concurrent(t *testing.T) {
	o := newTestOrchestrator()
	since := time.Now()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				name := fmt.Sprintf("s-%d", i%5)
				o.ActivatePinned("sess", &Skill{Name: name}, "manual", "", 1)
				_ = o.SkillsActiveSince("sess", since)
				o.DeactivateSkill("sess", name)
			}
		}(g)
	}
	wg.Wait()
	assert.Len(t, o.SkillsActiveSince("sess", since), 5)
}
