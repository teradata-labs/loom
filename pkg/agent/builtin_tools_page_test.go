// Copyright 2026 Teradata
package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSplitOversizedUnits_KeepsEveryByteReachable pins the paging contract: a
// unit larger than the page budget is split into pieces rather than cut, so
// every byte stays retrievable by paging on. Truncating instead would strip the
// remainder with has_more=false — offset addresses units, so nothing could ask
// for the rest, and re-running the producing call regenerates the same unit.
func TestSplitOversizedUnits_KeepsEveryByteReachable(t *testing.T) {
	const budget = 1024
	huge := strings.Repeat("x", 5000)
	out := splitOversizedUnits([]string{huge, "small"}, budget)

	require.Greater(t, len(out), 2, "the oversized unit is split into pieces")
	for i, u := range out {
		assert.LessOrEqual(t, len(u), budget, "piece %d respects the budget", i)
	}
	assert.Equal(t, huge+"small", strings.Join(out, ""), "no byte is lost")

	// Units already within budget are returned untouched (same backing slice).
	small := []string{"a", "b"}
	assert.Equal(t, small, splitOversizedUnits(small, budget))
}

// TestSplitOversizedUnits_CutsOnRuneBoundaries proves multi-byte runes are not
// severed, so each piece is still valid UTF-8.
func TestSplitOversizedUnits_CutsOnRuneBoundaries(t *testing.T) {
	const budget = 1024
	out := splitOversizedUnits([]string{strings.Repeat("é", 2000)}, budget)
	require.Greater(t, len(out), 1)
	for i, u := range out {
		assert.True(t, utf8.ValidString(u), "piece %d is valid UTF-8", i)
	}
}
