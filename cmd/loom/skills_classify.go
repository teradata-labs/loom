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

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// skillsClassifyCmd is the gRPC client for ClassifySkill. Re-classifies
// an already-imported skill in the catalog without re-running the full
// SKILL.md → loom/v1 conversion.
//
// The server must have a classifier provider configured
// (LOOM_CLASSIFY_PROVIDER + creds); the call returns FailedPrecondition
// otherwise.
var skillsClassifyCmd = &cobra.Command{
	Use:   "classify <skill-name>",
	Short: "Re-classify a skill in the server's catalog",
	Long: `Asks the server to assign a fresh parent_index_path to an
already-imported skill, using the graph-aware classifier so the new
path tends to join existing buckets.

The server must have a classifier provider configured
(LOOM_CLASSIFY_PROVIDER + the provider's standard creds). After a
successful classification the server reloads all running agents'
routers so the new path becomes routable on the next chat turn.

Examples:

  # Re-classify with the server's default taxonomy:
  loom skills classify teradata-statistics

  # Re-classify against a custom taxonomy:
  loom skills classify teradata-statistics --taxonomy ~/my-taxonomy.yaml`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillsClassify,
}

var skillsClassifyTaxonomy string

func init() {
	skillsClassifyCmd.Flags().StringVar(&skillsClassifyTaxonomy, "taxonomy", "",
		"path to a custom taxonomy YAML (uploaded to the server). "+
			"Empty uses the server's default seed.")
}

func runSkillsClassify(cmd *cobra.Command, args []string) error {
	skillName := args[0]

	conn, err := dialSkillsImportServer()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	client := loomv1.NewSkillsImportServiceClient(conn)

	req := &loomv1.ClassifySkillRequest{SkillName: skillName}
	if skillsClassifyTaxonomy != "" {
		taxBytes, err := os.ReadFile(filepath.Clean(skillsClassifyTaxonomy)) // #nosec G304 -- documented contract for --taxonomy
		if err != nil {
			return fmt.Errorf("read taxonomy file %s: %w", skillsClassifyTaxonomy, err)
		}
		req.TaxonomyOverride = taxBytes
	}

	var ctx context.Context
	if cmd != nil {
		ctx = cmd.Context()
	}
	if ctx == nil {
		ctx = context.Background()
	}

	resp, err := client.ClassifySkill(ctx, req)
	if err != nil {
		return describeRPCError("classify", err)
	}

	previous := resp.PreviousPath
	if previous == "" {
		previous = "(unclassified)"
	}
	fmt.Fprintf(os.Stderr, "==> %s: %s -> %s\n", skillName, previous, resp.ParentIndexPath)
	if resp.Reason != "" {
		fmt.Fprintf(os.Stderr, "    reason: %s\n", resp.Reason)
	}
	if resp.RouterReloaded {
		fmt.Fprintf(os.Stderr, "    router: reloaded (running agents see the new path immediately)\n")
	} else {
		fmt.Fprintf(os.Stderr, "    router: NOT reloaded (restart looms serve to pick up the new path)\n")
	}
	return nil
}

// =============================================================================
// gRPC dialer shared by import / classify / add subcommands
// =============================================================================

// dialSkillsImportServer opens a gRPC connection to the configured
// looms server using the same creds builder as the chat command.
// Caller is responsible for Close.
//
// On dial failure, returns a wrapped error that points the user at
// `looms serve` and the --server flag, so a missing-server case is
// distinguishable from a code bug.
func dialSkillsImportServer() (*grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if tlsEnabled {
		tlsCfg, err := buildSkillsTLSConfig()
		if err != nil {
			return nil, fmt.Errorf("build TLS config: %w", err)
		}
		creds = credentials.NewTLS(tlsCfg)
	} else {
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(serverAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("connect to looms server at %s: %w (make sure looms serve is running, or set --server / LOOM_SERVER)", serverAddr, err)
	}
	return conn, nil
}

// describeRPCError turns a gRPC error into a user-friendly message
// for the CLI. Specifically, Unavailable (server not reachable) gets
// pointed at `looms serve` instead of bubbling the raw transport
// error, since that's by far the most common failure mode.
//
// Returns the input err verbatim wrapped with the supplied prefix
// when no special-casing applies.
func describeRPCError(prefix string, err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unavailable:
			return fmt.Errorf("%s: server at %s is not reachable (make sure looms serve is running, or set --server / LOOM_SERVER): %s", prefix, serverAddr, st.Message())
		case codes.FailedPrecondition:
			return fmt.Errorf("%s: server rejected request (FailedPrecondition usually means a server-side configuration is missing, e.g., classify=true requires LOOM_CLASSIFY_PROVIDER + creds in the server env): %s", prefix, st.Message())
		case codes.NotFound:
			return fmt.Errorf("%s: %s", prefix, st.Message())
		case codes.InvalidArgument:
			return fmt.Errorf("%s: invalid request: %s", prefix, st.Message())
		}
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// buildSkillsTLSConfig mirrors pkg/tui/client.createTLSConfig but
// stays local to cmd/loom so this file can construct its own
// connection without depending on a tui-internal helper.
func buildSkillsTLSConfig() (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if tlsInsecure {
		cfg.InsecureSkipVerify = true //nolint:gosec // user opted into via --tls-insecure
		return cfg, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if tlsCAFile != "" {
		caCert, err := os.ReadFile(filepath.Clean(tlsCAFile)) // #nosec G304 -- user-supplied CA path is the documented contract
		if err != nil {
			return nil, fmt.Errorf("read CA file %s: %w", tlsCAFile, err)
		}
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("CA file %s contains no usable certificates", tlsCAFile)
		}
	}
	cfg.RootCAs = pool
	if tlsServerName != "" {
		cfg.ServerName = tlsServerName
	}
	return cfg, nil
}
