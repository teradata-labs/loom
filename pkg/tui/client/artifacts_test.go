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
package client

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// These two wrappers are the only artifact entry points a remote surface uses,
// and the whole reason they exist is to put session_id on the wire. A wrapper
// that quietly dropped the field would still compile, still return artifacts,
// and hand every surface the session-blind listing this change set out to fix
// — so what is asserted here is that the field actually reaches the request.
//
// The fixture is local rather than the shared mockLoomServiceServer, which does
// not implement the artifact RPCs; recording the request needs a stub of its own
// anyway.
type artifactRecordingServer struct {
	loomv1.UnimplementedLoomServiceServer

	gotList *loomv1.ListArtifactsRequest
	gotGet  *loomv1.GetArtifactRequest
}

func (s *artifactRecordingServer) ListArtifacts(_ context.Context, req *loomv1.ListArtifactsRequest) (*loomv1.ListArtifactsResponse, error) {
	s.gotList = req
	return &loomv1.ListArtifactsResponse{
		Artifacts: []*loomv1.Artifact{
			{Id: "a1", Name: "report.md", SessionId: req.SessionId},
		},
		TotalCount: 1,
	}, nil
}

func (s *artifactRecordingServer) GetArtifact(_ context.Context, req *loomv1.GetArtifactRequest) (*loomv1.GetArtifactResponse, error) {
	s.gotGet = req
	return &loomv1.GetArtifactResponse{
		Artifact: &loomv1.Artifact{Id: "a1", Name: req.Name, SessionId: req.SessionId},
	}, nil
}

func setupArtifactClient(t *testing.T) (*Client, *artifactRecordingServer) {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	stub := &artifactRecordingServer{}
	server := grpc.NewServer()
	loomv1.RegisterLoomServiceServer(server, stub)
	go func() {
		if err := server.Serve(lis); err != nil {
			t.Logf("server error: %v", err)
		}
	}()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &Client{
		conn:   conn,
		client: loomv1.NewLoomServiceClient(conn),
		addr:   "passthrough:///bufnet",
	}, stub
}

func TestListSessionArtifactsSendsSessionID(t *testing.T) {
	c, stub := setupArtifactClient(t)

	arts, err := c.ListSessionArtifacts(context.Background(), "sess-1", 10, 5)
	if err != nil {
		t.Fatalf("ListSessionArtifacts: %v", err)
	}

	if stub.gotList == nil {
		t.Fatal("server received no ListArtifacts request")
	}
	if stub.gotList.SessionId != "sess-1" {
		t.Errorf("session_id on the wire = %q, want sess-1", stub.gotList.SessionId)
	}
	if stub.gotList.Limit != 10 || stub.gotList.Offset != 5 {
		t.Errorf("limit/offset = %d/%d, want 10/5", stub.gotList.Limit, stub.gotList.Offset)
	}
	if len(arts) != 1 || arts[0].SessionId != "sess-1" {
		t.Errorf("got %d artifacts, want 1 reporting sess-1", len(arts))
	}
}

// limit 0 must stay 0 on the wire so the server applies its own default rather
// than the client inventing one.
func TestListSessionArtifactsLeavesDefaultLimitToServer(t *testing.T) {
	c, stub := setupArtifactClient(t)

	if _, err := c.ListSessionArtifacts(context.Background(), "sess-1", 0, 0); err != nil {
		t.Fatalf("ListSessionArtifacts: %v", err)
	}
	if stub.gotList.Limit != 0 {
		t.Errorf("limit = %d, want 0 so the server default applies", stub.gotList.Limit)
	}
}

func TestGetArtifactByNameSendsNameAndSession(t *testing.T) {
	c, stub := setupArtifactClient(t)

	art, err := c.GetArtifactByName(context.Background(), "report.md", "sess-2")
	if err != nil {
		t.Fatalf("GetArtifactByName: %v", err)
	}

	if stub.gotGet == nil {
		t.Fatal("server received no GetArtifact request")
	}
	if stub.gotGet.Name != "report.md" || stub.gotGet.SessionId != "sess-2" {
		t.Errorf("request = (name %q, session %q), want (report.md, sess-2)",
			stub.gotGet.Name, stub.gotGet.SessionId)
	}
	// Names are only unique per session, so the name path must not fall back to
	// an id lookup — an id here would silently ignore the session.
	if stub.gotGet.Id != "" {
		t.Errorf("id = %q, want empty so the lookup resolves by name within the session", stub.gotGet.Id)
	}
	if art.SessionId != "sess-2" {
		t.Errorf("returned artifact reports session %q, want sess-2", art.SessionId)
	}
}
