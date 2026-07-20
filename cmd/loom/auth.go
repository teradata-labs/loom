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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/teradata-labs/loom/internal/supabaseauth"
	loomconfig "github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

// Login flags.
var (
	loginProvider   string
	loginURL        string
	loginProjectRef string
	loginAnonKey    string
)

// Lazily-initialized session store + token source shared across commands.
var (
	authOnce  sync.Once
	authStore *supabaseauth.Store
	authToken *supabaseauth.TokenSource
)

func initAuth() {
	authOnce.Do(func() {
		authStore = supabaseauth.NewStore(loomconfig.GetLoomDataDir())
		authToken = supabaseauth.NewTokenSource(authStore)
	})
}

// loomBearerToken returns the current access token for outgoing RPCs, or ("",
// nil) when the user is not logged in (so commands still work against an
// unauthenticated looms). Refreshes transparently when logged in.
func loomBearerToken() (string, error) {
	initAuth()
	sess, err := authStore.Load()
	if err != nil || sess == nil || sess.AccessToken == "" {
		return "", nil
	}
	return authToken.Token()
}

// loomClientConfig builds the gRPC client config from the persistent flags,
// attaching the bearer-token source so a `loom login` session authenticates RPCs.
func loomClientConfig() client.Config {
	return client.Config{
		ServerAddr:    serverAddr,
		TLSEnabled:    tlsEnabled,
		TLSInsecure:   tlsInsecure,
		TLSCAFile:     tlsCAFile,
		TLSServerName: tlsServerName,
		BearerToken:   loomBearerToken,
	}
}

func resolveSupabaseLogin() (urlBase, projectRef, anonKey string, err error) {
	projectRef = firstNonEmpty(loginProjectRef,
		os.Getenv("LOOM_SERVER_AUTH_SUPABASE_PROJECT_REF"), os.Getenv("SUPABASE_PROJECT_REF"))
	urlBase = firstNonEmpty(loginURL, os.Getenv("SUPABASE_URL"))
	if urlBase == "" && projectRef != "" {
		urlBase = fmt.Sprintf("https://%s.supabase.co", projectRef)
	}
	anonKey = firstNonEmpty(loginAnonKey, os.Getenv("SUPABASE_ANON_KEY"))
	if urlBase == "" || anonKey == "" {
		return "", "", "", fmt.Errorf("supabase URL and anon key are required " +
			"(set --url/--anon-key, or SUPABASE_URL/SUPABASE_ANON_KEY, or --project-ref)")
	}
	return urlBase, projectRef, anonKey, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Sign in to Loom via Supabase Auth (OAuth)",
	Long: `Sign in via a Supabase Auth OAuth provider using the PKCE flow.

Opens your browser to the provider (default: github), captures the result on a
local callback, exchanges it for tokens, and stores the session (OS keyring, or
a 0600 file fallback). Subsequent loom commands attach the access token to RPCs.

Configuration (flags or environment):
  --project-ref / LOOM_SERVER_AUTH_SUPABASE_PROJECT_REF / SUPABASE_PROJECT_REF
  --url         / SUPABASE_URL          (derived from project-ref when omitted)
  --anon-key    / SUPABASE_ANON_KEY`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		urlBase, projectRef, anonKey, err := resolveSupabaseLogin()
		if err != nil {
			return err
		}
		initAuth()

		ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
		defer cancel()

		sess, err := supabaseauth.Login(ctx, supabaseauth.LoginConfig{
			URL:         urlBase,
			ProjectRef:  projectRef,
			AnonKey:     anonKey,
			Provider:    loginProvider,
			OpenBrowser: browser.OpenURL,
			Logf:        func(format string, a ...any) { fmt.Fprintf(os.Stderr, format, a...) },
		})
		if err != nil {
			return fmt.Errorf("login failed: %w", err)
		}
		if err := authStore.Save(sess); err != nil {
			return fmt.Errorf("store session: %w", err)
		}
		who := sess.Email
		if who == "" {
			who = subjectFromJWT(sess.AccessToken)
		}
		fmt.Printf("✅ Signed in%s\n", labelSuffix(who))
		return nil
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Sign out and remove the stored Loom session",
	RunE: func(_ *cobra.Command, _ []string) error {
		initAuth()
		if err := authStore.Clear(); err != nil {
			return err
		}
		fmt.Println("✅ Signed out")
		return nil
	},
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show the locally stored Loom session identity",
	RunE: func(_ *cobra.Command, _ []string) error {
		initAuth()
		sess, err := authStore.Load()
		if err != nil {
			return err
		}
		if sess == nil || sess.AccessToken == "" {
			fmt.Println("Not signed in. Run 'loom login'.")
			return nil
		}
		fmt.Printf("Signed in:  %s\n", firstNonEmpty(sess.Email, subjectFromJWT(sess.AccessToken), "(unknown)"))
		if sess.ProjectRef != "" {
			fmt.Printf("Project:    %s\n", sess.ProjectRef)
		}
		if sess.ExpiresAt > 0 {
			exp := time.Unix(sess.ExpiresAt, 0)
			fmt.Printf("Expires:    %s (%s)\n", exp.Format(time.RFC3339), expiryHint(sess))
		}
		return nil
	},
}

func labelSuffix(who string) string {
	if who == "" {
		return ""
	}
	return " as " + who
}

func expiryHint(sess *supabaseauth.Session) string {
	if sess.Expiring(0) {
		return "expired; will refresh on next use"
	}
	return "valid"
}

// subjectFromJWT extracts the "sub" claim from a JWT WITHOUT verifying it.
// Client-side display only; the server validates the signature.
func subjectFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return firstNonEmpty(claims.Email, claims.Sub)
}

func init() {
	loginCmd.Flags().StringVar(&loginProvider, "provider", "github", "OAuth provider (github, google, ...)")
	loginCmd.Flags().StringVar(&loginURL, "url", "", "Supabase project URL (default: derived from --project-ref)")
	loginCmd.Flags().StringVar(&loginProjectRef, "project-ref", "", "Supabase project reference")
	loginCmd.Flags().StringVar(&loginAnonKey, "anon-key", "", "Supabase anon (public) key")

	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
	rootCmd.AddCommand(whoamiCmd)
}
