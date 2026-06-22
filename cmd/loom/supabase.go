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
	"fmt"
	"os"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/teradata-labs/loom/internal/supabaseauth"
)

const (
	keyringServiceLoom  = "loom"
	mgmtTokenKeyringKey = "supabase_mgmt_token" // #nosec G101 -- keyring entry name (lookup key), not a credential
)

var (
	connectClientID     string
	connectClientSecret string
	connectRedirectURI  string
	connectProjectRef   string
	connectOut          string
)

var supabaseCmd = &cobra.Command{
	Use:   "supabase",
	Short: "Supabase project integration helpers",
}

var supabaseConnectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Connect Loom storage to a Supabase project via Management OAuth",
	Long: `Use Supabase Management OAuth to list your projects and emit a ready-to-use
storage.postgres.supabase config block.

PREREQUISITE (one-time, manual): register an OAuth application in the Supabase
dashboard (Organization -> OAuth Apps) to obtain a client id/secret, and
register the redirect URI (default https://127.0.0.1:8976/callback — the
Management API rejects plain-HTTP redirect URIs, even loopback). Supply the
credentials via --client-id/--client-secret or the LOOM_SUPABASE_CLIENT_ID /
LOOM_SUPABASE_CLIENT_SECRET environment variables.

The loopback callback serves a one-hour self-signed certificate (public CAs
cannot issue for 127.0.0.1), so after authorizing, your browser shows a
certificate warning once — click "Advanced" and proceed. The OAuth code is
single-use and PKCE-bound, so the untrusted certificate does not weaken the
flow.

The Supabase Management API does NOT expose the database password, so the
emitted config omits it; set it out-of-band via LOOM_SUPABASE_DB_PASSWORD.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		clientID := firstNonEmpty(connectClientID, os.Getenv("LOOM_SUPABASE_CLIENT_ID"))
		clientSecret := firstNonEmpty(connectClientSecret, os.Getenv("LOOM_SUPABASE_CLIENT_SECRET"))
		if clientID == "" || clientSecret == "" {
			return fmt.Errorf("missing Supabase Management OAuth client id/secret " +
				"(set --client-id/--client-secret or LOOM_SUPABASE_CLIENT_ID/LOOM_SUPABASE_CLIENT_SECRET); " +
				"register an OAuth app at supabase.com/dashboard/org/_/apps")
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
		defer cancel()

		token, projects, err := managementProjects(ctx, clientID, clientSecret)
		if err != nil {
			return err
		}
		_ = keyring.Set(keyringServiceLoom, mgmtTokenKeyringKey, token) // best-effort cache

		if len(projects) == 0 {
			return fmt.Errorf("no Supabase projects visible to this OAuth app")
		}

		if connectProjectRef == "" {
			fmt.Println("Projects:")
			for _, p := range projects {
				fmt.Printf("  %-24s %s (%s)\n", p.ID, p.Name, p.Region)
			}
			fmt.Println("\nRe-run with --project-ref <ref> to emit its storage config block.")
			return nil
		}

		var chosen *supabaseauth.Project
		for i := range projects {
			if projects[i].ID == connectProjectRef {
				chosen = &projects[i]
				break
			}
		}
		if chosen == nil {
			return fmt.Errorf("project ref %q not found among your projects", connectProjectRef)
		}

		block := supabaseStorageYAML(chosen)
		if connectOut != "" {
			if err := os.WriteFile(connectOut, []byte(block), 0o600); err != nil {
				return fmt.Errorf("write %s: %w", connectOut, err)
			}
			fmt.Printf("Wrote storage config to %s\n", connectOut)
			return nil
		}
		fmt.Print(block)
		return nil
	},
}

// managementProjects returns a valid Management API token and the project list,
// reusing a cached token when it still works and re-authorizing otherwise.
func managementProjects(ctx context.Context, clientID, clientSecret string) (string, []supabaseauth.Project, error) {
	if cached, err := keyring.Get(keyringServiceLoom, mgmtTokenKeyringKey); err == nil && cached != "" {
		if projects, err := supabaseauth.ListProjects(ctx, nil, supabaseauth.DefaultManagementAPI, cached); err == nil {
			return cached, projects, nil
		}
	}
	token, err := supabaseauth.ManagementLogin(ctx, supabaseauth.ManagementConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURI:  connectRedirectURI,
		OpenBrowser:  browser.OpenURL,
		Logf:         func(format string, a ...any) { fmt.Fprintf(os.Stderr, format, a...) },
	})
	if err != nil {
		return "", nil, err
	}
	projects, err := supabaseauth.ListProjects(ctx, nil, supabaseauth.DefaultManagementAPI, token)
	if err != nil {
		return "", nil, err
	}
	return token, projects, nil
}

// supabaseStorageYAML renders a storage.postgres.supabase config block for a project.
// The pooler gateway (aws-0 vs aws-1) varies per project and is not exposed by
// the Management API scopes this flow requests, so it is emitted as a hint:
// connecting through the wrong gateway fails with "tenant not found".
func supabaseStorageYAML(p *supabaseauth.Project) string {
	return fmt.Sprintf(`# Generated by 'loom supabase connect'. Set the DB password out-of-band:
#   export LOOM_SUPABASE_DB_PASSWORD=...
storage:
  backend: postgres
  postgres:
    supabase:
      enabled: true
      project_ref: %s
      region: %s
      # The pooler gateway varies per project (newer projects are often on
      # aws-1, older on aws-0); the exact host is shown in the dashboard's
      # "Connect" dialog under "Session pooler". Uncomment if the aws-0
      # default fails with "tenant not found":
      # pooler_host: aws-1-%s.pooler.supabase.com
      # database: postgres
  migration:
    auto_migrate: true
`, p.ID, p.Region, p.Region)
}

func init() {
	supabaseConnectCmd.Flags().StringVar(&connectClientID, "client-id", "", "Supabase Management OAuth client id (or LOOM_SUPABASE_CLIENT_ID)")
	supabaseConnectCmd.Flags().StringVar(&connectClientSecret, "client-secret", "", "Supabase Management OAuth client secret (or LOOM_SUPABASE_CLIENT_SECRET)")
	supabaseConnectCmd.Flags().StringVar(&connectRedirectURI, "redirect-uri", "https://127.0.0.1:8976/callback", "OAuth redirect URI (must match the registered app; https is served with an ephemeral self-signed cert)")
	supabaseConnectCmd.Flags().StringVar(&connectProjectRef, "project-ref", "", "Project ref to emit a config block for (omit to list projects)")
	supabaseConnectCmd.Flags().StringVar(&connectOut, "out", "", "Write the config block to this file instead of stdout")

	supabaseCmd.AddCommand(supabaseConnectCmd)
	rootCmd.AddCommand(supabaseCmd)
}
