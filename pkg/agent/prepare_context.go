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

	"github.com/teradata-labs/loom/pkg/types"
)

// prepareContext is the single-writer pressure pipeline's only mutation entry
// point after message admission. It runs zone dispatch against the current
// budget usage — red folds the ledger, yellow evicts low-value context,
// below yellow does nothing — then returns the assembled message list for
// the LLM call. Both LLM-bound call sites (chatWithRetry callers) run this
// before calling the LLM; no other code mutates l1Messages/l2Summary.
func (a *Agent) prepareContext(ctx context.Context, session *types.Session) ([]Message, error) {
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	if !ok || segMem == nil {
		return session.GetMessages(), nil
	}

	pct := segMem.BudgetPct()
	yellowPct, redPct := segMem.ZoneThresholds()

	switch {
	case pct >= redPct:
		// Snapshot the flat history under lock so the ledger-count read and
		// flatLen argument are consistent with each other and with any
		// concurrent AddMessage. Direct session.Messages reads would race
		// against Session's own mutex and violate its documented contract.
		flat := session.SnapshotMessages()
		if err := segMem.Fold(ctx, countLedgerUsers(flat), len(flat)); err != nil {
			return nil, err
		}
	case pct >= yellowPct:
		segMem.ValveEvict(ctx)
	}

	return session.GetMessages(), nil
}
