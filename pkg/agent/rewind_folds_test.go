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

package agent

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// seqsOfTurn is every stored row id of one turn, folded or not.
func seqsOfTurn(t *testing.T, store *SessionStore, ctx context.Context, sessionID string, turn int64) []int64 {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, "SELECT id FROM messages WHERE session_id = ? AND turn = ? ORDER BY id", sessionID, turn)
	require.NoError(t, err)
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		out = append(out, id)
	}
	return out
}

func foldCount(t *testing.T, store *SessionStore, ctx context.Context, sessionID string) int {
	t.Helper()
	snaps, err := store.LoadMemorySnapshots(ctx, sessionID, "fold", 0)
	require.NoError(t, err)
	return len(snaps)
}

// A fold records the turn it was made in and the rows it folded.
func TestAFoldRecordsItsTurnAndRows(t *testing.T) {
	store, ctx := rewindStore(t)
	exchange(t, store, ctx, "first")
	exchange(t, store, ctx, "second")
	rows := seqsOfTurn(t, store, ctx, "s", 1)
	require.NoError(t, store.FoldMessages(ctx, "s", rows, 1, "summary"))
	snaps, err := store.LoadMemorySnapshots(ctx, "s", "fold", 0)
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	r, ok := DecodeFoldRecord(snaps[0].Content)
	require.True(t, ok)
	require.NotNil(t, r.Turn)
	require.EqualValues(t, 2, *r.Turn, "the fold was made during turn 2")
	require.Equal(t, rows, r.Seqs)
	require.Equal(t, "summary", r.Text)
}

// Rewinding a turn during which a fold was made removes the fold and makes
// the rows it folded, which the rewind keeps, live again. A fold made before
// the undone turns stays, rows and all.
func TestARewindTakesTheFoldsOfItsTurnsWithIt(t *testing.T) {
	store, ctx := rewindStore(t)
	exchange(t, store, ctx, "first")
	require.NoError(t, store.FoldMessages(ctx, "s", seqsOfTurn(t, store, ctx, "s", 1)[:1], 1, "fold in turn one"))
	exchange(t, store, ctx, "second")
	exchange(t, store, ctx, "third")
	// made during turn 3, folding the rest of turn 1 and all of turn 2
	rest := append(seqsOfTurn(t, store, ctx, "s", 1)[1:], seqsOfTurn(t, store, ctx, "s", 2)...)
	require.NoError(t, store.FoldMessages(ctx, "s", rest, 2, "fold in turn three: second, third"))

	n, err := store.TruncateFrom(ctx, "s", 3)
	require.NoError(t, err)
	require.Equal(t, 3, n, "turn 3 is three rows")
	require.Equal(t, 1, foldCount(t, store, ctx, "s"), "only the fold made before the undone turn stays")
	msgs, err := store.LoadMessages(ctx, "s")
	require.NoError(t, err)
	require.Equal(t, []string{"tool output for first", "reply to first", "second", "tool output for second", "reply to second"},
		contents(msgs), "the rows the removed fold folded are live again; the earlier fold's row stays folded")
}

// A rewind removes every fold after one it removes: each carries the one
// before it.
func TestARewindTakesEveryLaterFold(t *testing.T) {
	store, ctx := rewindStore(t)
	exchange(t, store, ctx, "first")
	exchange(t, store, ctx, "second")
	require.NoError(t, store.FoldMessages(ctx, "s", seqsOfTurn(t, store, ctx, "s", 1), 1, "made in turn two"))
	exchange(t, store, ctx, "third")
	require.NoError(t, store.FoldMessages(ctx, "s", seqsOfTurn(t, store, ctx, "s", 2), 2, "made in turn three"))
	_, err := store.TruncateFrom(ctx, "s", 2)
	require.NoError(t, err)
	require.Zero(t, foldCount(t, store, ctx, "s"))
	msgs, err := store.LoadMessages(ctx, "s")
	require.NoError(t, err)
	require.Equal(t, []string{"first", "tool output for first", "reply to first"}, contents(msgs))
}

// A fold that does not record its turn (written before folds did), made
// during the turns a rewind would undo, refuses the rewind and changes
// nothing; one made before them does not stand in the way.
func TestARewindAcrossAnUnplacedFoldIsRefused(t *testing.T) {
	store, ctx := rewindStore(t)
	timed := func() { // the exchange helper writes no time; real rows carry one
		_, err := store.db.ExecContext(ctx, "UPDATE messages SET timestamp = ? WHERE timestamp <= 0", time.Now().Unix())
		require.NoError(t, err)
	}
	exchange(t, store, ctx, "first")
	timed()
	require.NoError(t, store.SaveMemorySnapshot(ctx, "s", "fold", `{"n":1,"text":"before"}`, 1))
	_, err := store.db.ExecContext(ctx, "UPDATE memory_snapshots SET created_at = created_at - 10")
	require.NoError(t, err)
	exchange(t, store, ctx, "second")
	timed()
	n, err := store.TruncateFrom(ctx, "s", 2)
	require.NoError(t, err, "a fold made before the undone turn refused the rewind")
	require.Equal(t, 3, n)

	exchange(t, store, ctx, "second")
	timed()
	require.NoError(t, store.SaveMemorySnapshot(ctx, "s", "fold", `{"n":2,"text":"covers msg:4-6 SECRET"}`, 1))
	n, err = store.TruncateFrom(ctx, "s", 2)
	require.ErrorIs(t, err, ErrRewindAcrossFold)
	require.Zero(t, n)
	msgs, err := store.LoadMessages(ctx, "s")
	require.NoError(t, err)
	require.Len(t, msgs, 6, "a refused rewind removed rows")
	require.Equal(t, 2, foldCount(t, store, ctx, "s"))
}

// Through a real turn: after rewinding a turn during which a fold was made,
// the model is shown neither the fold's summary nor the undone turn, and is
// shown the rows the fold had folded.
func TestTheModelDoesNotSeeAFoldOfAnUndoneTurn(t *testing.T) {
	store := turnStore(t)
	a, llmA, _, _ := twoObjects(t, store, &recordingEventLog{})
	ctx := context.Background()
	_, err := a.Chat(ctx, "s1", "one-KEPT")
	require.NoError(t, err)
	_, err = a.Chat(ctx, "s1", "two-UNDONE")
	require.NoError(t, err)
	var turnOne []int64
	for _, m := range mustLoad(t, store, ctx, "s1") {
		if m.Turn == 1 {
			id, _ := strconv.ParseInt(m.ID, 10, 64)
			turnOne = append(turnOne, id)
		}
	}
	require.NoError(t, store.FoldMessages(ctx, "s1", turnOne, 1, "SECRET-FROM-TURN-2"))

	_, err = a.RewindSession(ctx, "s1", 1)
	require.NoError(t, err)
	_, err = a.Chat(ctx, "s1", "three")
	require.NoError(t, err)
	call := llmA.lastCall()
	require.NotContains(t, call, "SECRET-FROM-TURN-2", "the undone turn's fold summary reached the model")
	require.NotContains(t, call, "two-UNDONE")
	require.Contains(t, call, "one-KEPT", "the rows the removed fold had folded did not come back")
}

func mustLoad(t *testing.T, store *SessionStore, ctx context.Context, sessionID string) []Message {
	t.Helper()
	msgs, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	return msgs
}
