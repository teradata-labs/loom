// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"github.com/teradata-labs/loom/pkg/types"
)

// GetRecentConversationTurns retrieves the last N messages from L1 cache,
// including all roles (user, assistant, tool). Used by graph memory extraction
// to get richer context than tool-only results.
func (sm *SegmentedMemory) GetRecentConversationTurns(n int) []types.Message {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if n <= 0 {
		return nil
	}

	if len(sm.l1Messages) <= n {
		messages := make([]types.Message, len(sm.l1Messages))
		copy(messages, sm.l1Messages)
		return messages
	}

	start := len(sm.l1Messages) - n
	messages := make([]types.Message, n)
	copy(messages, sm.l1Messages[start:])
	return messages
}
