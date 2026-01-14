# Package Manager Validation Guide

This guide covers validation tools and methods for ensuring package manifests comply with repository rules.

## Windows Package Managers

### Scoop

**Local Validation:**
```powershell
# Install scoop (if not already installed)
iwr -useb get.scoop.sh | iex

# Test manifest syntax
scoop checkver packaging/windows/scoop/loom.json
scoop checkver packaging/windows/scoop/loom-server.json

# Test installation locally
scoop install packaging/windows/scoop/loom.json
scoop uninstall loom
```

**Validation Checklist:**
- ✅ Valid JSON syntax
- ✅ Required fields: `version`, `description`, `license`, `architecture`, `url`, `hash`
- ✅ SHA256 hashes are lowercase
- ✅ URLs are accessible
- ✅ Binary names match expected patterns

**Official Documentation:**
- https://github.com/ScoopInstaller/Scoop/wiki/App-Manifests
- https://github.com/ScoopInstaller/Scoop/wiki/Criteria-for-including-apps-in-the-main-bucket

---

### winget

**Local Validation:**
```powershell
# Install winget validation tool
winget install --id Microsoft.WingetCreate

# Validate manifests
wingetcreate validate packaging/windows/winget/

# Test manifest parsing
winget validate packaging/windows/winget/Teradata.Loom.yaml
```

**Validation Checklist:**
- ✅ YAML syntax is valid
- ✅ Schema version matches (1.6.0)
- ✅ All three manifest files present: `.yaml`, `.installer.yaml`, `.locale.en-US.yaml`
- ✅ SHA256 hashes are UPPERCASE
- ✅ Package identifier follows reverse domain format: `Teradata.Loom`
- ✅ Version format matches (X.Y.Z)
- ✅ ReleaseDate is ISO 8601 format

**Online Validation:**
- Automated PR validation when submitted to https://github.com/microsoft/winget-pkgs
- CI checks run automatically on PR submission

**Official Documentation:**
- https://github.com/microsoft/winget-pkgs
- https://learn.microsoft.com/en-us/windows/package-manager/package/manifest

---

### Chocolatey

**Local Validation:**
```powershell
# Install Chocolatey (if not already installed)
Set-ExecutionPolicy Bypass -Scope Process -Force
iwr https://community.chocolatey.org/install.ps1 -UseBasicParsing | iex

# Pack and test locally
cd packaging/windows/chocolatey
choco pack

# Test installation
choco install loom -source . -y
choco uninstall loom -y

# Use the package validator (recommended before submission)
# https://docs.chocolatey.org/en-us/community-repository/moderation/package-validator/
```

**Validation Checklist:**
- ✅ Valid .nuspec XML syntax
- ✅ PowerShell scripts have no syntax errors
- ✅ SHA256 checksums are correct
- ✅ Package ID is lowercase: `loom`
- ✅ Version follows semantic versioning
- ✅ Install/uninstall scripts work correctly
- ✅ No hardcoded paths (use `$toolsDir`)
- ✅ Proper error handling in scripts

**Online Validation:**
Before submitting, use the Chocolatey Package Validator:
- https://docs.chocolatey.org/en-us/community-repository/moderation/package-validator/

**Official Documentation:**
- https://docs.chocolatey.org/en-us/create/create-packages
- https://docs.chocolatey.org/en-us/community-repository/moderation/

---

## macOS Package Manager

### Homebrew

**Local Validation:**
```bash
# Install Homebrew (if not already installed)
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"

# Audit formulas
brew audit --strict packaging/macos/homebrew/loom.rb
brew audit --strict packaging/macos/homebrew/loom-server.rb

# Test installation locally
brew install --build-from-source packaging/macos/homebrew/loom.rb
brew uninstall loom

# Run formula tests
brew test packaging/macos/homebrew/loom.rb
```

**Validation Checklist:**
- ✅ Valid Ruby syntax
- ✅ SHA256 hashes match downloaded files
- ✅ URLs are accessible
- ✅ Formula follows Homebrew naming conventions
- ✅ `desc` is under 80 characters
- ✅ License is valid SPDX identifier
- ✅ Test block works correctly
- ✅ Installation copies files to correct locations

**CI Validation:**
When submitting to homebrew-core, automated tests run:
- Syntax validation
- Formula audit
- Installation test on multiple macOS versions
- Test block execution

**Official Documentation:**
- https://docs.brew.sh/Formula-Cookbook
- https://docs.brew.sh/Acceptable-Formulae
- https://docs.brew.sh/How-To-Open-a-Homebrew-Pull-Request

---

## Automated Validation Workflow

Create a GitHub Actions workflow to validate all manifests:

```yaml
name: Validate Package Manifests

on:
  pull_request:
    paths:
      - 'packaging/**'
  push:
    branches:
      - main
    paths:
      - 'packaging/**'

jobs:
  validate-scoop:
    runs-on: windows-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install Scoop
        run: |
          iwr -useb get.scoop.sh | iex
      - name: Validate manifests
        run: |
          scoop checkver packaging/windows/scoop/loom.json
          scoop checkver packaging/windows/scoop/loom-server.json

  validate-winget:
    runs-on: windows-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install winget validation tools
        run: winget install Microsoft.WingetCreate
      - name: Validate manifests
        run: wingetcreate validate packaging/windows/winget/

  validate-chocolatey:
    runs-on: windows-latest
    steps:
      - uses: actions/checkout@v4
      - name: Validate package
        run: |
          cd packaging/windows/chocolatey
          choco pack
          # Verify pack succeeded
          if (!(Test-Path "*.nupkg")) { exit 1 }

  validate-homebrew:
    runs-on: macos-latest
    steps:
      - uses: actions/checkout@v4
      - name: Audit formulas
        run: |
          brew audit --strict packaging/macos/homebrew/loom.rb
          brew audit --strict packaging/macos/homebrew/loom-server.rb
```

---

## Pre-Submission Checklist

Before submitting to any package manager repository:

### All Package Managers
- [ ] Version number is correct and follows semantic versioning
- [ ] Release exists on GitHub with all binaries
- [ ] SHA256 hashes match the actual release binaries
- [ ] URLs are accessible (test with curl/wget)
- [ ] License is correct (Apache-2.0)
- [ ] Description is accurate and concise

### Scoop
- [ ] Manifest validates with `scoop checkver`
- [ ] Tested local installation: `scoop install ./loom.json`
- [ ] Binary executes correctly: `loom --help`
- [ ] Hashes are lowercase

### winget
- [ ] All three manifest files present
- [ ] Validated with `wingetcreate validate`
- [ ] Hashes are UPPERCASE
- [ ] Package identifier follows naming convention
- [ ] ReleaseDate is current

### Chocolatey
- [ ] Package builds: `choco pack` succeeds
- [ ] Tested local installation: `choco install loom -source .`
- [ ] Install/uninstall scripts work correctly
- [ ] No syntax errors in PowerShell scripts
- [ ] Used package validator tool

### Homebrew
- [ ] Formula audits cleanly: `brew audit --strict`
- [ ] Tested local installation: `brew install --build-from-source`
- [ ] Test block passes: `brew test loom`
- [ ] Binary executes correctly

---

## Quick Validation Commands

```bash
# Run all validations at once (requires all tools installed)

# Windows (PowerShell)
cd packaging/windows
scoop checkver scoop/loom.json
scoop checkver scoop/loom-server.json
wingetcreate validate winget/
cd chocolatey && choco pack && cd ..

# macOS
brew audit --strict packaging/macos/homebrew/loom.rb
brew audit --strict packaging/macos/homebrew/loom-server.rb
```

---

## Common Issues and Fixes

### Issue: SHA256 mismatch
**Solution:** Recalculate hash from the actual release binary:
```bash
# macOS/Linux
shasum -a 256 loom-darwin-amd64.tar.gz

# Windows (PowerShell)
Get-FileHash loom-windows-amd64.exe.zip -Algorithm SHA256
```

### Issue: URL not accessible
**Solution:** Ensure release is public and tag matches version:
```bash
curl -I https://github.com/teradata-labs/loom/releases/download/v1.0.1/loom-darwin-amd64.tar.gz
```

### Issue: Package version already exists
**Solution:** Update version number in all manifests, create new GitHub release

### Issue: Homebrew audit fails on desc length
**Solution:** Shorten description to under 80 characters

### Issue: winget validation fails
**Solution:** Check that hashes are UPPERCASE, version format is X.Y.Z

---

## Resources

- **Scoop**: https://github.com/ScoopInstaller/Scoop
- **winget**: https://github.com/microsoft/winget-pkgs
- **Chocolatey**: https://community.chocolatey.org/
- **Homebrew**: https://brew.sh/
