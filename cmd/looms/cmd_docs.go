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
	"embed"
	"fmt"
	"html"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

//go:embed docs/public/*
var docsFS embed.FS

var docsCmd = &cobra.Command{
	Use:   "docs",
	Short: "View documentation in browser",
	Long: `Serves embedded documentation on a local web server and opens it in your default browser.

The documentation is embedded directly in the binary, so this works offline.

Examples:
  looms docs              # Serve on default port 6060
  looms docs --port 8080  # Serve on custom port
  looms docs --no-open    # Don't open browser automatically`,
	RunE: runDocs,
}

func init() {
	docsCmd.Flags().StringP("port", "p", "6060", "Port to serve docs on")
	docsCmd.Flags().Bool("no-open", false, "Don't open browser automatically")
	docsCmd.Flags().Bool("dev-mode", false, "Enable edit mode (opens files in editor) - DEV ONLY")
	rootCmd.AddCommand(docsCmd)
}

func runDocs(cmd *cobra.Command, args []string) error {
	port := cmd.Flag("port").Value.String()
	noOpen, _ := cmd.Flags().GetBool("no-open")
	devMode, _ := cmd.Flags().GetBool("dev-mode")

	// Try to serve pre-built Hugo site
	docs, err := fs.Sub(docsFS, "docs/public")
	if err != nil {
		return fmt.Errorf("documentation not embedded in binary (run 'just build-with-docs'): %w", err)
	}

	// Set up HTTP server
	mux := http.NewServeMux()

	// DEVELOPMENT ONLY: Edit mode endpoint
	if devMode {
		mux.HandleFunc("/edit", handleEdit)
		fmt.Println("⚠️  DEV MODE: Edit buttons enabled")
	}

	// Wrap file server with edit button injection in dev mode
	fileHandler := http.FileServer(http.FS(docs))

	// Redirect root to /en/ for landing page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/en/", http.StatusFound)
			return
		}
		injectEditButton(fileHandler, devMode).ServeHTTP(w, r)
	})

	// All other paths
	mux.Handle("/en/", injectEditButton(fileHandler, devMode))

	// Start server in background
	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Error starting docs server: %v\n", err)
		}
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	url := fmt.Sprintf("http://localhost:%s", port)
	fmt.Printf("📚 Documentation server running at %s\n", url)
	fmt.Println("Press Ctrl+C to stop")

	// Open browser if requested
	if !noOpen {
		if err := openBrowser(url); err != nil {
			fmt.Printf("⚠️  Could not open browser: %v\n", err)
			fmt.Printf("Please open %s manually\n", url)
		}
	}

	// Wait forever (until Ctrl+C)
	select {}
}

func openBrowser(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return cmd.Start()
}

// DEVELOPMENT ONLY: handleEdit opens a file in the user's editor
func handleEdit(w http.ResponseWriter, r *http.Request) {
	urlPath := r.URL.Query().Get("page")
	if urlPath == "" {
		http.Error(w, "Missing 'page' parameter", http.StatusBadRequest)
		return
	}

	// Map URL path to source file
	// URL: /docs/guides/quickstart/ → File: website/content/en/docs/guides/quickstart.md
	sourcePath := urlToSourceFile(urlPath)
	if sourcePath == "" {
		http.Error(w, "Could not map URL to source file", http.StatusNotFound)
		return
	}

	// Determine editor command
	editor := os.Getenv("EDITOR")
	var cmd *exec.Cmd

	if editor != "" {
		// Use $EDITOR if set
		// #nosec G204 -- Intentional: CLI uses $EDITOR env var (standard Unix practice)
		cmd = exec.Command(editor, sourcePath)
	} else {
		// Try common editors
		switch runtime.GOOS {
		case "darwin":
			// Try VS Code, then fallback to system open
			if _, err := exec.LookPath("code"); err == nil {
				// #nosec G204 -- Intentional: Launching VS Code with validated file path
				cmd = exec.Command("code", sourcePath)
			} else {
				// #nosec G204 -- Intentional: Launching system TextEdit with validated file path
				cmd = exec.Command("open", "-t", sourcePath) // TextEdit
			}
		case "linux":
			if _, err := exec.LookPath("code"); err == nil {
				// #nosec G204 -- Intentional: Launching VS Code with validated file path
				cmd = exec.Command("code", sourcePath)
			} else {
				// #nosec G204 -- Intentional: Launching xdg-open with validated file path
				cmd = exec.Command("xdg-open", sourcePath)
			}
		default:
			http.Error(w, "Unsupported platform for edit mode", http.StatusNotImplemented)
			return
		}
	}

	// Launch editor in background
	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to open editor: %v", err), http.StatusInternalServerError)
		return
	}

	// Return success message
	w.Header().Set("Content-Type", "text/html")
	// Escape sourcePath to prevent XSS
	escapedPath := html.EscapeString(sourcePath)
	fmt.Fprintf(w, `<html><body><h2>Opened in editor</h2><p>File: <code>%s</code></p><p><a href="javascript:history.back()">← Back to docs</a></p></body></html>`, escapedPath)
}

// urlToSourceFile maps a Hugo URL path to the source markdown file
func urlToSourceFile(urlPath string) string {
	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Clean up URL path
	// URL format: /en/docs/guides/quickstart/
	urlPath = strings.TrimPrefix(urlPath, "/")
	urlPath = strings.TrimSuffix(urlPath, "/")

	// Strip the /en/docs/ prefix if present
	urlPath = strings.TrimPrefix(urlPath, "en/docs/")
	urlPath = strings.TrimPrefix(urlPath, "en/")

	// Also handle URLs that don't have /en/ prefix
	urlPath = strings.TrimPrefix(urlPath, "docs/")

	// Clean the path to remove any .. or . components (prevents path traversal)
	urlPath = filepath.Clean(urlPath)

	// Map to source file
	// URL: guides/quickstart → website/content/en/docs/guides/quickstart.md
	sourcePath := filepath.Join(cwd, "website", "content", "en", "docs", urlPath+".md")

	// Security: Verify the resolved path is still within the expected directory
	expectedBase := filepath.Join(cwd, "website", "content", "en", "docs")
	resolvedPath, err := filepath.Abs(sourcePath)
	if err != nil {
		return ""
	}
	resolvedBase, err := filepath.Abs(expectedBase)
	if err != nil {
		return ""
	}

	// Check if the resolved path is within the expected base directory
	if !strings.HasPrefix(resolvedPath, resolvedBase+string(filepath.Separator)) &&
		resolvedPath != resolvedBase {
		return "" // Path traversal attempt detected
	}

	// Construct a clean path from validated components to satisfy CodeQL
	// Get the relative path from base to resolved path
	relPath, err := filepath.Rel(resolvedBase, resolvedPath)
	if err != nil || strings.HasPrefix(relPath, "..") {
		return "" // Invalid relative path
	}

	// Reconstruct the path using safe components
	// This creates a clean path not directly tainted by user input
	cleanPath := filepath.Join(resolvedBase, relPath)

	// Verify file exists using the clean path
	if _, err := os.Stat(cleanPath); err != nil {
		return ""
	}

	return cleanPath
}

// DEVELOPMENT ONLY: injectEditButton wraps HTTP handler to inject edit buttons
func injectEditButton(handler http.Handler, devMode bool) http.Handler {
	if !devMode {
		return handler
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only inject for HTML pages
		if !strings.HasSuffix(r.URL.Path, "/") && !strings.HasSuffix(r.URL.Path, ".html") {
			handler.ServeHTTP(w, r)
			return
		}

		// Create response recorder to capture HTML
		recorder := &responseRecorder{
			ResponseWriter: w,
			body:           &strings.Builder{},
		}

		handler.ServeHTTP(recorder, r)

		// Inject edit button before closing </body> tag
		htmlContent := recorder.body.String()
		if strings.Contains(htmlContent, "</body>") {
			// Escape the URL path to prevent XSS
			escapedPath := html.EscapeString(r.URL.Path)
			editButton := fmt.Sprintf(`
<div style="position: fixed; bottom: 20px; right: 20px; z-index: 9999;">
  <a href="/edit?page=%s" style="display: inline-block; padding: 12px 20px; background: #007bff; color: white; text-decoration: none; border-radius: 4px; box-shadow: 0 2px 8px rgba(0,0,0,0.2); font-family: sans-serif; font-size: 14px; font-weight: 500;">
    ✏️ Edit This Page
  </a>
</div>
`, escapedPath)
			htmlContent = strings.Replace(htmlContent, "</body>", editButton+"</body>", 1)
		}

		// Write modified HTML
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(htmlContent)))
		w.WriteHeader(recorder.statusCode)
		if _, err := w.Write([]byte(htmlContent)); err != nil {
			log.Printf("Failed to write response: %v", err)
		}
	})
}

// responseRecorder captures HTTP response for modification
type responseRecorder struct {
	http.ResponseWriter
	body       *strings.Builder
	statusCode int
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	r.body.Write(b)
	return len(b), nil
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
}

func (r *responseRecorder) Header() http.Header {
	return r.ResponseWriter.Header()
}
