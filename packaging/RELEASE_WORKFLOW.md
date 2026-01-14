# Package Manager Release Workflow

This document explains how package publishing is automated for Loom.

## Overview

**Two-phase workflow:**
1. **Validation** - Runs on every PR to verify manifests are correct
2. **Build & Publish** - Runs only on version releases to push to package managers

## GitHub Secrets Required

Add these secrets at: https://github.com/teradata-labs/loom/settings/secrets/actions

| Secret Name | Description | Where to Get |
|-------------|-------------|--------------|
| `CHOCOLATEY_API_KEY` | API key for Chocolatey | https://community.chocolatey.org/account |

## Workflow Triggers

### Validation Only (validate-packages.yml)

**Runs on:**
- ✅ Pull requests that change `packaging/**`
- ✅ Pushes to `main` branch
- ❌ Release tags (skipped)

**What it does:**
- Validates JSON/YAML/XML syntax
- Checks required fields
- Verifies hash formats (UPPERCASE for winget, lowercase for Homebrew)
- Checks version consistency
- Tests URL accessibility

### Build & Publish (chocolatey-build.yml)

**Runs on:**
- ✅ Release tags: `v1.0.1`, `v1.0.2`, etc.
- ✅ Manual trigger (with optional publish flag)

**What it does:**
1. **Build job:**
   - Creates `.nupkg` file
   - Tests installation locally
   - Verifies both binaries work
   - Checks pattern installation
   - Tests uninstallation
   - Uploads artifact

2. **Publish job** (only on release tags):
   - Downloads built package
   - Verifies API key exists
   - Pushes to Chocolatey community
   - Submits for human moderation

## Creating a Release

### Step 1: Update Version Numbers

Update version in all package manifests:
- `packaging/windows/scoop/loom.json`
- `packaging/windows/scoop/loom-server.json`
- `packaging/windows/winget/Teradata.Loom.installer.yaml`
- `packaging/windows/chocolatey/loom.nuspec`
- `packaging/windows/chocolatey/tools/chocolateyinstall.ps1`
- `packaging/macos/homebrew/loom.rb`
- `packaging/macos/homebrew/loom-server.rb`

### Step 2: Update SHA256 Hashes

Download binaries from the release and update hashes:

```bash
# Windows
sha256sum loom-windows-amd64.exe.zip
sha256sum looms-windows-amd64.exe.zip

# macOS
shasum -a 256 loom-darwin-amd64.tar.gz
shasum -a 256 loom-darwin-arm64.tar.gz
shasum -a 256 looms-darwin-amd64.tar.gz
shasum -a 256 looms-darwin-arm64.tar.gz
```

**Important:**
- winget requires UPPERCASE hashes
- Homebrew requires lowercase hashes
- Scoop and Chocolatey accept either

### Step 3: Create GitHub Release

```bash
# Tag the release
git tag -a v1.0.2 -m "Release v1.0.2"
git push origin v1.0.2
```

Or use GitHub UI:
1. Go to: https://github.com/teradata-labs/loom/releases/new
2. Create tag: `v1.0.2`
3. Generate release notes
4. Publish release

### Step 4: Automated Publishing

Once the tag is pushed:

1. **GitHub Release workflow** runs:
   - Builds binaries for all platforms
   - Uploads to GitHub Release

2. **Chocolatey workflow** runs automatically:
   - Builds `.nupkg`
   - Runs tests
   - **Automatically pushes to Chocolatey** (if API key configured)
   - Submits for human moderation

3. **Manual submissions still needed:**
   - Scoop: Submit PR to `ScoopInstaller/Main`
   - winget: Submit PR to `microsoft/winget-pkgs`
   - Homebrew: Update your tap or wait for automation

## Manual Package Submission

### Scoop

```bash
# Fork ScoopInstaller/Main
git clone https://github.com/YOUR_USERNAME/Main.git
cd Main
git checkout -b add-loom-v1.0.2

# Copy manifests
cp ../loom/packaging/windows/scoop/loom.json bucket/loom.json
cp ../loom/packaging/windows/scoop/loom-server.json bucket/loom-server.json

# Commit and push
git add bucket/
git commit -m "loom: Update to version 1.0.2"
git push origin add-loom-v1.0.2

# Create PR on GitHub
gh pr create --repo ScoopInstaller/Main
```

### winget

```bash
# Fork microsoft/winget-pkgs
git clone https://github.com/YOUR_USERNAME/winget-pkgs.git
cd winget-pkgs
git checkout -b teradata-loom-1.0.2

# Create directory structure
mkdir -p manifests/t/Teradata/Loom/1.0.2
cp ../loom/packaging/windows/winget/*.yaml manifests/t/Teradata/Loom/1.0.2/

# Commit and push
git add manifests/
git commit -m "New version: Teradata.Loom version 1.0.2"
git push origin teradata-loom-1.0.2

# Sign CLA and create PR
gh pr create --repo microsoft/winget-pkgs
```

### Chocolatey (Automatic)

✅ **Fully automated on release tags** if `CHOCOLATEY_API_KEY` secret is configured.

To manually push:
```powershell
cd packaging/windows/chocolatey
choco pack
choco push loom.1.0.2.nupkg --source https://push.chocolatey.org/
```

### Homebrew

Update your tap repository:
```bash
# Clone your tap
git clone https://github.com/teradata-labs/homebrew-loom.git
cd homebrew-loom

# Update formulas
cp ../loom/packaging/macos/homebrew/loom.rb Formula/
cp ../loom/packaging/macos/homebrew/loom-server.rb Formula/

# Commit and push
git add Formula/
git commit -m "Update to v1.0.2"
git push origin main
```

## Testing Before Release

### Manual Workflow Trigger

Test the build without publishing:

1. Go to: https://github.com/teradata-labs/loom/actions/workflows/chocolatey-build.yml
2. Click "Run workflow"
3. Select branch
4. **Do not** check "Publish to Chocolatey"
5. Click "Run workflow"

This builds and tests the package without pushing to Chocolatey.

### Local Testing

```powershell
# Chocolatey
cd packaging/windows/chocolatey
choco pack
choco install loom -source . -y

# Test
loom --version
looms --version

# Uninstall
choco uninstall loom -y
```

```bash
# Homebrew
brew install --build-from-source packaging/macos/homebrew/loom.rb
brew test packaging/macos/homebrew/loom.rb
```

## Troubleshooting

### Chocolatey push fails with "401 Unauthorized"

- Check that `CHOCOLATEY_API_KEY` secret is set correctly
- Verify API key is still valid at https://community.chocolatey.org/account
- Try regenerating the API key

### Validation fails on hashes

- winget requires UPPERCASE: `2E6A7D0C...`
- Homebrew requires lowercase: `2e6a7d0c...`
- Recalculate from actual release binaries, not local builds

### Package version already exists

- You cannot overwrite published packages
- Increment version number: `1.0.1` → `1.0.2`
- Retag and create new release

### Scoop/winget PR rejected

- Check automated feedback from bots
- Ensure all required fields present
- Verify URLs are accessible
- Check that manifest follows repository conventions

## Future Automation

Potential improvements:
- Auto-update Scoop manifests via bot
- Auto-submit winget PRs via GitHub Actions
- Homebrew tap auto-update on release
- Batch update all package managers in one workflow

## Support

- Chocolatey issues: https://community.chocolatey.org/packages/loom
- Scoop issues: https://github.com/ScoopInstaller/Main/issues
- winget issues: https://github.com/microsoft/winget-pkgs/issues
- Homebrew tap issues: https://github.com/teradata-labs/homebrew-loom/issues
