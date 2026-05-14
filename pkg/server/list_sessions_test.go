// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestListSessionsNeedsArtifactDisk(t *testing.T) {
	if listSessionsNeedsArtifactDisk(nil) {
		t.Fatal("nil req")
	}
	prev := artifacts.SessionMetadataEnabled()
	artifacts.SetSessionMetadataEnabled(false)
	defer artifacts.SetSessionMetadataEnabled(prev)
	if listSessionsNeedsArtifactDisk(&loomv1.ListSessionsRequest{MetadataStatus: "completed"}) {
		t.Fatal("metadata_status should not require disk when feature disabled")
	}
	artifacts.SetSessionMetadataEnabled(true)
	if listSessionsNeedsArtifactDisk(&loomv1.ListSessionsRequest{}) {
		t.Fatal("empty filters")
	}
	if !listSessionsNeedsArtifactDisk(&loomv1.ListSessionsRequest{MetadataStatus: "completed"}) {
		t.Fatal("metadata_status filter")
	}
	if !listSessionsNeedsArtifactDisk(&loomv1.ListSessionsRequest{ProjectId: "p"}) {
		t.Fatal("project_id filter")
	}
}

func TestFilterListSessions(t *testing.T) {
	t.Parallel()
	sessions := []*loomv1.Session{
		{Id: "a", AgentId: "guide", State: "active", Backend: "anthropic", Metadata: map[string]string{"project_id": "p1"}, MetadataStatus: "active"},
		{Id: "b", AgentId: "other", State: "active", Backend: "anthropic", Metadata: map[string]string{"project_id": "p2"}, MetadataStatus: "completed"},
	}
	req := &loomv1.ListSessionsRequest{AgentId: "guide"}
	got := filterListSessions(sessions, req)
	if len(got) != 1 || got[0].Id != "a" {
		t.Fatalf("agent filter: got %+v", got)
	}

	req = &loomv1.ListSessionsRequest{ProjectId: "p2"}
	got = filterListSessions(sessions, req)
	if len(got) != 1 || got[0].Id != "b" {
		t.Fatalf("project filter: got %+v", got)
	}

	req = &loomv1.ListSessionsRequest{MetadataStatus: "completed"}
	got = filterListSessions(sessions, req)
	if len(got) != 1 || got[0].Id != "b" {
		t.Fatalf("metadata_status filter: got %+v", got)
	}
}

func TestPageProtoSessions(t *testing.T) {
	t.Parallel()
	s := []*loomv1.Session{{Id: "1"}, {Id: "2"}, {Id: "3"}}
	if got := pageProtoSessions(s, 1, 1); len(got) != 1 || got[0].Id != "2" {
		t.Fatalf("page 1,1: got %+v", got)
	}
	// limit 0 uses default page size (50); only 3 sessions exist
	if got := pageProtoSessions(s, 0, 0); len(got) != 3 {
		t.Fatalf("limit 0 uses default page size: got len %d", len(got))
	}
	if got := pageProtoSessions(s, 0, 2); len(got) != 2 || got[0].Id != "1" {
		t.Fatalf("page first 2: got %+v", got)
	}
	if got := pageProtoSessions(s, 10, 5); len(got) != 0 {
		t.Fatalf("offset past end: got %+v", got)
	}
}

func TestValidateListSessionsRequest(t *testing.T) {
	t.Parallel()
	err := validateListSessionsRequest(&loomv1.ListSessionsRequest{Offset: -1})
	if err == nil {
		t.Fatal("expected error for negative offset")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if err := validateListSessionsRequest(&loomv1.ListSessionsRequest{Offset: 0}); err != nil {
		t.Fatalf("zero offset ok: %v", err)
	}
	if err := validateListSessionsRequest(nil); err != nil {
		t.Fatalf("nil req ok: %v", err)
	}
}

func TestNormalizeListSessionsLimit(t *testing.T) {
	t.Parallel()
	if normalizeListSessionsLimit(0) != listSessionsDefaultPageSize {
		t.Fatalf("zero -> default")
	}
	if normalizeListSessionsLimit(-1) != listSessionsDefaultPageSize {
		t.Fatalf("negative -> default")
	}
	if normalizeListSessionsLimit(100) != 100 {
		t.Fatalf("in-range unchanged")
	}
	if normalizeListSessionsLimit(9999) != listSessionsMaxPageSize {
		t.Fatalf("over max capped")
	}
}
