#!/usr/bin/env bash
# Print the import paths of packages with Windows-specific code, one per line.
#
# The Windows CI job runs the full suite without -race (too slow) and runs -race
# only on the packages this script prints. A package qualifies when it has
#   - a Go file (build-tagged or *_windows.go) that is compiled for windows but
#     not for linux, or
#   - non-test code that branches on runtime.GOOS.
#
# Usage: .github/scripts/windows-race-packages.sh   (run from anywhere in the repo)
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# "<import path> <file>" for every Go file the package compiles for $1,
# including cgo files and in-package and external test files.
files_for() {
  # CGO_ENABLED is pinned because go defaults it off when GOOS differs from the
  # host, which would report every cgo/!cgo build-tag pair as OS-specific.
  CGO_ENABLED=1 GOOS=$1 go list -e -tags fts5 -f '
{{- range .GoFiles}}{{$.ImportPath}} {{.}}{{"\n"}}{{end -}}
{{- range .CgoFiles}}{{$.ImportPath}} {{.}}{{"\n"}}{{end -}}
{{- range .TestGoFiles}}{{$.ImportPath}} {{.}}{{"\n"}}{{end -}}
{{- range .XTestGoFiles}}{{$.ImportPath}} {{.}}{{"\n"}}{{end -}}
' ./...
}

windows_only() {
  comm -23 <(files_for windows | sort -u) <(files_for linux | sort -u) | cut -d' ' -f1
}

goos_branching() {
  git grep -l 'runtime\.GOOS' -- '*.go' ':!*_test.go' |
    xargs -r -n1 dirname |
    sort -u |
    while read -r dir; do
      # -e and the GoFiles guard skip directories whose files are all excluded
      # for the host (e.g. a `//go:build ignore` generator) instead of failing.
      go list -e -tags fts5 -f '{{if .GoFiles}}{{.ImportPath}}{{end}}' "./$dir"
    done
}

{ windows_only; goos_branching; } | sort -u
