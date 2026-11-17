// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import "github.com/teradata-labs/loom/pkg/agent"

// agentTokens counts text with the tokenizer the hand-off budget uses.
func agentTokens(text string) int {
	return agent.GetTokenCounter().CountTokens(text)
}
