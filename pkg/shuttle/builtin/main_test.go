// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"os"
	"testing"
)

// TestMain isolates the builtin tool tests from the ambient environment.
//
// The web search tool reads provider API keys from environment variables and
// force-selects Tavily whenever TAVILY_API_KEY is present (see web_search.go).
// If a developer runs the suite with any of these keys exported, tests that
// assert "no API key" behavior or pin a specific provider would silently change
// outcome — for example TestWebSearchTool_DefaultProvider_NoAPIKey would attempt
// a live Tavily request and dereference a nil error. Clearing the keys once,
// here, keeps every test in the package deterministic regardless of who runs it.
func TestMain(m *testing.M) {
	for _, key := range []string{
		"TAVILY_API_KEY",
		"BRAVE_API_KEY",
		"BRAVE_SEARCH_API_KEY",
		"SERPAPI_KEY",
		"SERPAPI_API_KEY",
	} {
		_ = os.Unsetenv(key)
	}

	os.Exit(m.Run())
}
