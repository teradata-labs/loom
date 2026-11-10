// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package communication

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRestartTopic(t *testing.T) {
	assert.Equal(t, "workflow.restart.iterative-0001", RestartTopicFor("iterative-0001"))
	assert.NotEqual(t, RestartTopicFor("run-a"), RestartTopicFor("run-b"), "one topic per run")

	assert.Equal(t, "", RestartTopicFromContext(context.Background()), "no run: no topic")
	assert.Equal(t, "", RestartTopicFromContext(nil)) //nolint:staticcheck // nil ctx must not panic

	ctx := WithRestartTopic(context.Background(), RestartTopicFor("run-a"))
	assert.Equal(t, "workflow.restart.run-a", RestartTopicFromContext(ctx))
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	assert.Equal(t, "workflow.restart.run-a", RestartTopicFromContext(child), "derived contexts keep the topic")
}
