// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTools_WarnsWhenListedToolIsNotReady(t *testing.T) {
	SetToolReadinessFunc(func(name string) (string, string, string, bool) {
		switch name {
		case "web_search":
			return "degraded", "No API key for tavily.", "Set TAVILY_API_KEY and restart looms.", true
		case "http_request":
			return "ready", "", "", true
		}
		return "", "", "", false
	})
	t.Cleanup(func() { SetToolReadinessFunc(nil) })

	_, warnings := validateTools([]interface{}{"web_search", "http_request"})
	var got []ValidationWarning
	for _, w := range warnings {
		if w.Field == "spec.tools" && strings.Contains(w.Message, "in this environment") {
			got = append(got, w)
		}
	}
	require.Len(t, got, 1, "only the degraded tool warns")
	assert.Contains(t, got[0].Message, "Tool 'web_search' is degraded in this environment: No API key for tavily.")
	assert.Equal(t, "Set TAVILY_API_KEY and restart looms.", got[0].Fix)
}

func TestValidateTools_NoReadinessHookMeansNoWarning(t *testing.T) {
	SetToolReadinessFunc(nil)
	_, warnings := validateTools([]interface{}{"web_search"})
	for _, w := range warnings {
		assert.NotContains(t, w.Message, "in this environment")
	}
}
