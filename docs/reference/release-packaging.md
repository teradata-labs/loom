# Release Packaging

How a Loom release reaches package managers, and how to recover when a
publisher fails. All workflows live in `.github/workflows/`.

## Publish chain

Pushing a `v*` tag triggers `release.yml`, which runs these jobs in order:

1. `create-release` — verifies the tag matches `VERSION`, generates the
   changelog, and publishes the GitHub release.
2. `build-binaries` — builds, signs, and uploads all platform tarballs/zips
   with SHA256 checksums and GPG signatures.
3. `create-combined-windows-package` — bundles `loom` + `looms` into
   `loom-windows-x64.zip`.
4. `create-attestations` — generates SLSA provenance for all artifacts.
5. `publish-packages` — dispatches `publish-homebrew.yml` and
   `publish-winget.yml` via `workflow_dispatch` with the release version.
   Skipped for alpha/beta/rc tags.

## Why publishers are dispatched explicitly

The GitHub release is created with the default `GITHUB_TOKEN`. GitHub
suppresses workflow triggers for events created by `GITHUB_TOKEN` (to
prevent recursive workflows), so the `release: types: [published]` triggers
on the publisher workflows **never fire** from the release workflow.
`workflow_dispatch` and `repository_dispatch` are exempt from this
suppression, which is why `publish-packages` dispatches them directly.

This was the root cause of the Homebrew tap staying at v1.0.1 while
v1.0.2–v1.3.0 shipped (January–June 2026): no publisher run ever triggered.

## Package managers

| Manager | Workflow | Trigger | Status |
|---|---|---|---|
| Homebrew | `publish-homebrew.yml` | dispatched by `publish-packages` | ✅ Implemented |
| winget | `publish-winget.yml` | dispatched by `publish-packages` | ✅ Implemented |
| Chocolatey | `chocolatey-build.yml` | `v*.*.*` tag push (independent) | ✅ Implemented |
| Scoop | `publish-scoop.yml` | disabled | 📋 Disabled |

### Homebrew tap (`teradata-labs/homebrew-tap`)

`publish-homebrew.yml`:

1. Verifies the release and its four darwin tarballs exist
   (`loom`/`looms` × `arm64`/`amd64`).
2. Downloads the tarballs plus the `v<version>` source tag tarball and
   computes SHA256 hashes for all five.
3. Updates `Formula/loom.rb` and `Formula/loom-server.rb` on a
   `loom-<version>` branch via the GitHub API (commits are GitHub-signed,
   satisfying the tap's required-signatures rule). Each formula carries
   three hashes: the two binary tarballs and the `loom-patterns` resource
   (the source tarball, from which patterns install — see
   teradata-labs/homebrew-tap#9). URLs derive from `v#{version}`, so only
   `version` and the sha256 values are rewritten; a verification step
   fails the run if any expected hash is missing afterward.
4. Opens a PR against the tap and merges it with `gh pr merge --admin`.

**Requirements:**

- `HOMEBREW_TAP_TOKEN` repo secret: a PAT with write access to the tap,
  belonging to a **tap repository admin**. If this expires, the workflow
  fails at the clone step — mint a new PAT and update the secret.
- The tap's `default` ruleset requires one approving PR review, which a bot
  PR never gets. The ruleset has a bypass entry for the Repository admin
  role; the `--admin` merge relies on it. If the merge step fails with
  "rule violations", check the ruleset's bypass actors:
  `gh api repos/teradata-labs/homebrew-tap/rulesets`.

### winget

`publish-winget.yml` submits a PR to `microsoft/winget-pkgs`. Merging is
controlled by Microsoft's review process, not by us.

## Manual recovery

If a publisher run was missed or failed, dispatch it by hand:

```bash
gh workflow run publish-homebrew.yml --repo teradata-labs/loom -f version=1.3.0
gh workflow run publish-winget.yml --repo teradata-labs/loom -f version=1.3.0
```

Verify the tap picked it up:

```bash
gh api repos/teradata-labs/homebrew-tap/contents/Formula/loom.rb \
  --jq '.content' | base64 -d | grep version
```
