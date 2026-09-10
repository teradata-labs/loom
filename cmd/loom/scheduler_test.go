// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestSchedulerRenderers pins the human-facing shortenings: the tables are
// the only window operators have into the scheduler, so an unmapped enum
// must read as "unspecified" rather than a raw proto constant.
func TestSchedulerRenderers(t *testing.T) {
	classes := map[loomv1.SlotPriorityClass]string{
		loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_NEW:             "new",
		loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_IN_FLIGHT:       "in-flight",
		loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_RESOURCE_HOLDER: "holder",
		loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_UNSPECIFIED:     "unspecified",
	}
	for in, want := range classes {
		assert.Equal(t, want, shortClass(in))
	}

	origins := map[loomv1.SlotOrigin]string{
		loomv1.SlotOrigin_SLOT_ORIGIN_INTERACTIVE: "interactive",
		loomv1.SlotOrigin_SLOT_ORIGIN_BATCH:       "batch",
		loomv1.SlotOrigin_SLOT_ORIGIN_UNSPECIFIED: "unspecified",
	}
	for in, want := range origins {
		assert.Equal(t, want, shortOrigin(in))
	}
}

// TestSchedulerSetFlagsDefaultToUnset pins the partial-update contract: the
// sentinel is negative, so a caller that passes only --tpm must not silently
// zero the other fields (0 is meaningful for every one of them).
func TestSchedulerSetFlagsDefaultToUnset(t *testing.T) {
	assert.Negative(t, setTPM, "tpm sentinel must be negative so 0 can mean recalibrate")
	assert.Negative(t, setStarvationAge)
	assert.Negative(t, setUtilization)
	assert.Negative(t, setHeadroom)
}
