# winget Manifests for Loom

winget (Windows Package Manager) is Microsoft's official command-line package manager for Windows 10 and Windows 11.

## Installation (for users)

Once submitted to the winget community repository:

```powershell
# Install Loom (includes both loom and looms binaries)
winget install Teradata.Loom

# Update Loom
winget upgrade Teradata.Loom

# Uninstall Loom
winget uninstall Teradata.Loom
```

## Manifest Structure

winget uses a multi-file YAML structure:

- **Teradata.Loom.yaml**: Version manifest (root file, tracked by version-manager)
- **Teradata.Loom.locale.en-US.yaml**: Package metadata and descriptions (tracked by version-manager)
- **Teradata.Loom.installer.yaml**: Download URLs and installation instructions (tracked by version-manager)

## Pre-Release vs Released Manifests

### SHA256 Placeholders

Before a release is built, the installer manifest contains a placeholder SHA256 hash (all zeros):

```yaml
InstallerSha256: 0000000000000000000000000000000000000000000000000000000000000000
```

**This is intentional.** The release workflow computes the actual hash from the combined package and updates it during the release process.

### Version Bumps

When running `just bump-{major|minor|patch}`, the version-manager automatically updates:
- ✅ `PackageVersion` in all 3 YAML files
- ✅ `InstallerUrl` in installer.yaml
- ✅ `ReleaseNotesUrl` in locale.yaml

**Not updated by version-manager** (set during release):
- InstallerSha256 (computed from actual binary)
- ReleaseDate (set to release date)

## Updating for New Releases

When releasing a new version:

1. **Update version numbers** in all three files:
   ```yaml
   PackageVersion: 1.0.1  # Change to new version
   ```

2. **Update ReleaseDate** in `Teradata.Loom.installer.yaml`:
   ```yaml
   ReleaseDate: 2026-01-14  # Update to release date
   ```

3. **Calculate SHA256 hashes** for the binaries:

   ```powershell
   # Download binaries
   $version = "1.0.1"
   Invoke-WebRequest "https://github.com/teradata-labs/loom/releases/download/v$version/loom-windows-amd64.exe.zip" -OutFile loom.zip
   Invoke-WebRequest "https://github.com/teradata-labs/loom/releases/download/v$version/looms-windows-amd64.exe.zip" -OutFile looms.zip

   # Calculate SHA256 hashes (uppercase, no dashes)
   (Get-FileHash loom.zip -Algorithm SHA256).Hash
   (Get-FileHash looms.zip -Algorithm SHA256).Hash
   ```

4. **Update InstallerSha256** fields in `Teradata.Loom.installer.yaml`:
   ```yaml
   InstallerSha256: 'ABC123...'  # Replace with actual hash
   ```

5. **Update URLs** in installer manifest:
   ```yaml
   InstallerUrl: https://github.com/teradata-labs/loom/releases/download/v1.0.1/...
   ```

## Validation

Test the manifests locally before submission:

```powershell
# Validate manifests
winget validate --manifest .

# Test installation from local manifest
winget install --manifest . --verbose
```

## Submission to winget-pkgs Repository

1. **Fork the repository**:
   - Go to https://github.com/microsoft/winget-pkgs
   - Click "Fork" to create your own copy

2. **Create a new branch**:
   ```powershell
   git clone https://github.com/YOUR_USERNAME/winget-pkgs
   cd winget-pkgs
   git checkout -b teradata-loom-1.0.1
   ```

3. **Add manifests**:
   ```powershell
   # Create directory structure
   mkdir -p manifests/t/Teradata/Loom/1.0.1

   # Copy manifests
   Copy-Item Teradata.Loom.yaml manifests/t/Teradata/Loom/1.0.1/
   Copy-Item Teradata.Loom.installer.yaml manifests/t/Teradata/Loom/1.0.1/
   Copy-Item Teradata.Loom.locale.en-US.yaml manifests/t/Teradata/Loom/1.0.1/
   ```

4. **Validate and commit**:
   ```powershell
   # Validate
   winget validate --manifest manifests/t/Teradata/Loom/1.0.1/

   # Commit
   git add manifests/t/Teradata/Loom/1.0.1/
   git commit -m "New version: Teradata.Loom version 1.0.1"
   git push origin teradata-loom-1.0.1
   ```

5. **Create Pull Request**:
   - Go to your fork on GitHub
   - Click "Pull Request" → "New Pull Request"
   - Set base repository to `microsoft/winget-pkgs`
   - Set base branch to `master`
   - Submit the PR

## Automated Updates

For future releases, you can use the winget manifest creator:

```powershell
# Install wingetcreate
winget install wingetcreate

# Update to new version (auto-detects changes)
wingetcreate update Teradata.Loom --version 1.0.2 --urls "https://github.com/teradata-labs/loom/releases/download/v1.0.2/loom-windows-amd64.exe.zip|x64" --submit
```

## References

- [winget Documentation](https://learn.microsoft.com/en-us/windows/package-manager/)
- [Manifest Schema](https://learn.microsoft.com/en-us/windows/package-manager/package/manifest)
- [Submitting Packages](https://github.com/microsoft/winget-pkgs/blob/master/CONTRIBUTING.md)
- [wingetcreate Tool](https://github.com/microsoft/winget-create)
