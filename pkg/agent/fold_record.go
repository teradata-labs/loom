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
	"encoding/json"
	"errors"
)

// ErrRewindAcrossFold is a rewind that would undo a turn during which a fold
// was made that did not record its turn and rows (folds written before they
// did). Its summary may describe the undone turn, and which rows it folded is
// unknown, so the rewind is refused rather than guessed.
var ErrRewindAcrossFold = errors.New("cannot rewind across a fold that does not record its turn")

// FoldRecord is one stored fold: its summary version, the turn during which it
// was made, and the message rows it folded. Turn is nil for a fold written
// before folds recorded it.
type FoldRecord struct {
	N    int     `json:"n"`
	Text string  `json:"text"`
	Seqs []int64 `json:"seqs,omitempty"`
	Turn *int64  `json:"turn,omitempty"`
}

// EncodeFoldRecord is the stored form of a fold.
func EncodeFoldRecord(n int, text string, seqs []int64, turn int64) (string, error) {
	b, err := json.Marshal(FoldRecord{N: n, Text: text, Seqs: seqs, Turn: &turn})
	return string(b), err
}

// DecodeFoldRecord reads a stored fold. ok is false for content that is not one.
func DecodeFoldRecord(content string) (FoldRecord, bool) {
	var r FoldRecord
	if err := json.Unmarshal([]byte(content), &r); err != nil {
		return FoldRecord{}, false
	}
	return r, true
}

// FoldsToUndo picks, from a session's folds, the ones a rewind of every turn
// from turn onward must remove. A fold made during an undone turn may describe
// it, since its summary was written with that turn in front of the model; and
// every later fold carries that summary forward, so it goes too. A fold that
// does not record its turn and was made at or after the first undone row
// (madeFrom) cannot be placed, and the rewind is refused.
func FoldsToUndo(folds []FoldRecord, made []int64, turn int64, madeFrom int64) ([]int, error) {
	first := -1
	for i, f := range folds {
		if f.Turn == nil {
			if made[i] >= madeFrom {
				return nil, ErrRewindAcrossFold
			}
			continue
		}
		if *f.Turn >= turn && (first < 0 || f.N < folds[first].N) {
			first = i
		}
	}
	if first < 0 {
		return nil, nil
	}
	var undo []int
	for i, f := range folds {
		if f.N >= folds[first].N {
			undo = append(undo, i)
		}
	}
	return undo, nil
}
