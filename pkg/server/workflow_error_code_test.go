// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/teradata-labs/loom/pkg/collaboration"
	"google.golang.org/grpc/codes"
)

func TestWorkflowErrorCode(t *testing.T) {
	wrapped := fmt.Errorf("collaboration execution failed: %w", fmt.Errorf("%w: pair_programming", collaboration.ErrPatternNotImplemented))
	assert.Equal(t, codes.Unimplemented, workflowErrorCode(wrapped), "a proto-only pattern is Unimplemented, not Internal")
	assert.Equal(t, codes.Internal, workflowErrorCode(errors.New("stage 2 failed")))
}
