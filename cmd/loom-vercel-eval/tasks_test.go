package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// knownKinds is every check kind the checker evaluates.
var knownKinds = map[string]bool{
	checkContains: true, checkNotContains: true, checkRegex: true, checkNotRegex: true,
	checkFileUnchanged: true, checkFileContains: true, checkFileNotContain: true,
	checkCmdExit0: true, checkToolCalled: true, checkToolNotCalled: true,
	checkToolInputNotContains: true, checkLabelAccuracy: true, checkTimeRangeWithin: true,
	checkAllocValid: true, checkJevNotDefective: true,
}

func TestTaskSetIsComplete(t *testing.T) {
	ts := allTasks()
	assert.Equal(t, 31, len(ts), "plan says 31 tasks")

	seen := map[string]bool{}
	for _, tk := range ts {
		assert.False(t, seen[tk.ID], "duplicate task id %s", tk.ID)
		seen[tk.ID] = true
		assert.NotEmpty(t, tk.Turns, "task %s has no turns", tk.ID)
		assert.NotEmpty(t, tk.Checks, "task %s has no checks", tk.ID)
	}
}

func TestEveryCheckKindIsKnownAndRegexesCompile(t *testing.T) {
	for _, tk := range allTasks() {
		for _, c := range tk.Checks {
			assert.True(t, knownKinds[c.Kind], "task %s: unknown check kind %q", tk.ID, c.Kind)
			if c.Kind == checkRegex || c.Kind == checkNotRegex {
				_, err := regexp.Compile(c.Value)
				assert.NoError(t, err, "task %s: bad regex %q", tk.ID, c.Value)
			}
		}
	}
}

// Deferred (Jev) checks must point at a fixture file that exists and a
// criterion that is defined, or the judge pass would fail at run time.
func TestDeferredChecksReferenceRealFixturesAndCriteria(t *testing.T) {
	for _, tk := range allTasks() {
		for _, c := range tk.Checks {
			if !isDeferred(c.Kind) {
				continue
			}
			_, ok := criteriaByName[c.Value]
			assert.True(t, ok, "task %s: unknown criterion %q", tk.ID, c.Value)
			_, ok = tk.Files[c.Path]
			assert.True(t, ok, "task %s: deferred check source %q is not in the fixture", tk.ID, c.Path)
		}
	}
}

// File-based checks must name a file the fixture creates, or the check
// would compare against nothing.
func TestFileChecksReferenceFixtureFiles(t *testing.T) {
	for _, tk := range allTasks() {
		for _, c := range tk.Checks {
			switch c.Kind {
			case checkFileUnchanged, checkFileContains, checkFileNotContain:
				_, ok := tk.Files[c.Path]
				assert.True(t, ok, "task %s: %s names %q which the fixture does not create", tk.ID, c.Kind, c.Path)
			}
		}
	}
}

func TestTicketLabelsMatchTicketText(t *testing.T) {
	files := ticketFiles()
	lines := strings.Split(strings.TrimSpace(files["tickets.txt"]), "\n")
	require.Len(t, lines, 20)
	spec := strings.Split(ticketLabelSpec(), ",")
	require.Len(t, spec, 20)

	p1 := map[string]bool{}
	for _, s := range []string{"Production is down", "lost customer data", "Nobody can log in"} {
		p1[s] = true
	}
	for i, line := range lines {
		id, text, _ := strings.Cut(line, ": ")
		wantID, wantLabel, _ := strings.Cut(spec[i], "=")
		require.Equal(t, wantID, id)
		isP1 := false
		for s := range p1 {
			if strings.Contains(text, s) {
				isP1 = true
			}
		}
		if wantLabel == "P1" {
			assert.True(t, isP1, "%s is labelled P1 but its text is not a P1 template", id)
		} else {
			assert.False(t, isP1, "%s is labelled %s but its text is a P1 template", id, wantLabel)
		}
	}
}

func TestBusinessDaysKnownValue(t *testing.T) {
	// 2026-03-02 (Mon) to 2026-03-06 (Fri): five weekdays, no holidays.
	assert.Equal(t, 5, businessDays(mustDate("2026-03-02"), mustDate("2026-03-06"), nil))
	// A holiday on a weekday is excluded; a weekend is never counted.
	assert.Equal(t, 4, businessDays(mustDate("2026-03-02"), mustDate("2026-03-08"),
		map[string]bool{"2026-03-04": true}))
}

func TestLabelAccuracy(t *testing.T) {
	cases := []struct {
		name  string
		spec  string
		final string
		ok    bool
	}{
		{"all right", "T-01=P1,T-02=P3", "T-01: P1\nT-02: P3", true},
		{"half right", "T-01=P1,T-02=P3", "T-01: P1\nT-02: P1", false},
		{"missing lines count as wrong", "T-01=P1,T-02=P3", "T-01: P1", false},
		{"ids are whole tokens", "T-01=P1,T-10=P2", "T-010: P3\nT-01: P1\nT-10: P2", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, _ := labelAccuracy(c.spec, "0.85", c.final)
			assert.Equal(t, c.ok, ok)
		})
	}
}

func TestTimeRangeWithin(t *testing.T) {
	cases := []struct {
		name  string
		final string
		ok    bool
	}{
		{"exact window", "Start from 10:30 to 11:30.", true},
		{"dash variant", "10:30-11:30", true},
		{"too early", "09:30-10:30", false},
		{"too short", "10:30-11:00", false},
		{"no range", "I can't find a slot", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, _ := timeRangeWithin("10:30|11:30|60", c.final)
			assert.Equal(t, c.ok, ok)
		})
	}
}

func TestAllocValid(t *testing.T) {
	spec := "A:12000:20000,B:8000:20000,C:10000:20000,D:0:20000|50000"
	cases := []struct {
		name  string
		final string
		ok    bool
	}{
		{"valid", "A: $15,000\nB: $10,000\nC: $12,000\nD: $13,000", true},
		{"under minimum", "A: $11,000\nB: $10,000\nC: $12,000\nD: $13,000", false},
		{"over total", "A: $15,000\nB: $15,000\nC: $15,000\nD: $15,000", false},
		{"over cap", "A: $21,000\nB: $10,000\nC: $10,000\nD: $9,000", false},
		{"missing team", "A: $15,000\nB: $10,000\nC: $12,000", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, _ := allocValid(spec, c.final)
			assert.Equal(t, c.ok, ok)
		})
	}
}

func TestCalibrationItemsAreBalanced(t *testing.T) {
	items := calibrationItems()
	require.Len(t, items, 26, "24 planned items plus the two E4 note items added 2026-11-19")
	var defective, correct int
	perType := map[string]int{}
	for _, it := range items {
		perType[it.TaskType]++
		if it.Defective {
			defective++
			assert.NotEmpty(t, it.Defect, "defective item %s has no defect name", it.ID)
		} else {
			correct++
		}
	}
	assert.Equal(t, 13, defective)
	assert.Equal(t, 13, correct)
	assert.Equal(t, 12, perType["D5"])
	assert.Equal(t, 14, perType["E4"])
}

func TestGateThresholdsMatchPlan(t *testing.T) {
	assert.InDelta(t, 0.90, gateMinAgreement, 1e-9)
	assert.InDelta(t, 0.10, gateMaxFalsePass, 1e-9)
}

func TestNumberPatternAcceptsSeparators(t *testing.T) {
	re := regexp.MustCompile(numberPattern("2280"))
	assert.True(t, re.MatchString("West: 2280"))
	assert.True(t, re.MatchString("West: 2,280"))
	assert.False(t, re.MatchString("West: 22801"))
	assert.False(t, re.MatchString("West: 1.2280"))
	assert.True(t, regexp.MustCompile(numberPattern("40")).MatchString("It is 40 °C"))
}
