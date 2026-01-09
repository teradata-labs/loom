// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package types provides type aliases for backward compatibility.
// The actual types are now defined in github.com/teradata-labs/loom/pkg/types
// to break import cycles between pkg/agent and pkg/llm packages.
package types

import (
	"github.com/teradata-labs/loom/pkg/types"
)

// Type aliases for backward compatibility.
// Code that imports pkg/llm/types will continue to work.
type ToolCall = types.ToolCall
type Message = types.Message
type Usage = types.Usage
type LLMResponse = types.LLMResponse
type LLMProvider = types.LLMProvider
type TokenCallback = types.TokenCallback
type StreamingLLMProvider = types.StreamingLLMProvider
