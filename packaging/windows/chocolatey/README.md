# Chocolatey Package for Loom

Chocolatey is the most popular package manager for Windows, with over 9,000 community-maintained packages.

## Security & Antivirus False Positives

### Known Issues

The Chocolatey install script may trigger false positives in antivirus software, particularly:
- **Microsoft Defender**: `Trojan:Script/Wacatac.B!ml`
- **Reason**: Legitimate installer behaviors (web downloads, binary extraction, environment modification)

### Mitigation Measures

The `chocolateyinstall.ps1` has been hardened against false positives:

1. ✅ **Comprehensive security comments** explaining all operations
2. ✅ **Real SHA256 checksums** (not placeholder zeros)
3. ✅ **Chocolatey helper functions** (`Get-ChocolateyWebFile` instead of `Invoke-WebRequest`)
4. ✅ **Official sources only** (downloads from `github.com/teradata-labs/loom`)

### If Flagged by Antivirus

1. **Update checksums** (critical): Use `update-checksums.ps1` script (see below)
2. **Submit false positive report** to Microsoft: https://www.microsoft.com/wdsi/filesubmission
3. **Include context**: Open-source project, Apache 2.0 license, official Teradata Labs repo

For detailed security information, see the "Addressing Antivirus False Positives" section below.

## Installation (for users)

Once published to Chocolatey.org:

```powershell
# Install Loom
choco install loom

# Update Loom
choco upgrade loom

# Uninstall Loom
choco uninstall loom
```

## Package Structure

```
chocolatey/
├── loom.nuspec                    # Package metadata (XML)
├── LICENSE.txt                    # Apache 2.0 license
├── tools/
│   ├── chocolateyinstall.ps1     # Installation script
│   └── chocolateyuninstall.ps1   # Uninstallation script
└── README.md                      # This file
```

## Building the Package (for maintainers)

### Prerequisites

```powershell
# Install Chocolatey (if not already installed)
Set-ExecutionPolicy Bypass -Scope Process -Force
[System.Net.ServicePointManager]::SecurityProtocol = [System.Net.ServicePointManager]::SecurityProtocol -bor 3072
iex ((New-Object System.Net.WebClient).DownloadString('https://community.chocolatey.org/install.ps1'))

# Install chocolatey package builder
choco install checksum
```

### Updating for New Releases

#### Automated Method (Recommended)

Use the `update-checksums.ps1` script to automatically fetch checksums from GitHub:

```powershell
# Update checksums for a specific version
.\update-checksums.ps1 -Version "1.0.2"

# This automatically updates:
# - Version in chocolateyinstall.ps1
# - SHA256 checksums for loom and looms binaries
```

Then update the version in `loom.nuspec` to match.

#### Manual Method

1. **Update version** in `loom.nuspec`:
   ```xml
   <version>1.0.1</version>
   ```

2. **Update URLs and version** in `tools/chocolateyinstall.ps1`:
   ```powershell
   $version = '1.0.1'
   ```

3. **Calculate checksums** for the release binaries:

   ```powershell
   # Download binaries
   $version = "1.0.1"
   Invoke-WebRequest "https://github.com/teradata-labs/loom/releases/download/v$version/loom-windows-amd64.exe.zip" -OutFile loom.zip
   Invoke-WebRequest "https://github.com/teradata-labs/loom/releases/download/v$version/looms-windows-amd64.exe.zip" -OutFile looms.zip

   # Calculate SHA256 checksums
   Get-FileHash -Algorithm SHA256 loom.zip
   Get-FileHash -Algorithm SHA256 looms.zip
   ```

4. **Update checksums** in `tools/chocolateyinstall.ps1`:
   ```powershell
   checksum64    = 'ABC123...' # loom TUI
   # and later in the file:
   checksum64    = 'DEF456...' # looms server
   ```

### Testing Locally

```powershell
# Navigate to chocolatey directory
cd chocolatey

# Pack the package
choco pack

# Test installation locally
choco install loom -source . -y

# Test the binaries
loom --help
looms --help

# Uninstall
choco uninstall loom -y
```

### Publishing to Chocolatey.org

1. **Create account** at https://community.chocolatey.org/account/Register

2. **Get API key** from https://community.chocolatey.org/account

3. **Set API key** (one-time setup):
   ```powershell
   choco apikey --key YOUR_API_KEY --source https://push.chocolatey.org/
   ```

4. **Build and push**:
   ```powershell
   # Build package
   choco pack

   # Push to Chocolatey
   choco push loom.1.0.1.nupkg --source https://push.chocolatey.org/
   ```

5. **Wait for moderation**: Community packages require manual approval (usually 1-3 days)

## Package Features

### What Gets Installed

- **Binaries**: `loom.exe` and `looms.exe` added to PATH via shims
- **Patterns**: 90+ YAML patterns downloaded to `$env:LOOM_DATA_DIR\patterns\` (default: `$env:USERPROFILE\.loom\patterns\`)
- **Environment Variable**: `LOOM_DATA_DIR` set to `$env:USERPROFILE\.loom` (if not already set)
- **Configuration**: Empty `looms.yaml` created (user configures LLM provider post-install)

### What Gets Uninstalled

- **Binaries**: Removed from PATH
- **Environment Variable**: `LOOM_DATA_DIR` removed (if set by installer)

**Preserved** (user data):
- `$env:LOOM_DATA_DIR\` directory (patterns, config, database)
- Users can manually remove with: `Remove-Item -Path "$env:LOOM_DATA_DIR" -Recurse -Force`

## Chocolatey Guidelines

- **Title Case**: Package title uses proper casing ("Loom AI Agent Framework")
- **Tags**: Space-separated, lowercase, relevant keywords
- **Description**: Markdown supported, comprehensive but concise
- **Dependencies**: None (standalone package)
- **License**: Must match upstream license (Apache 2.0)
- **Binaries**: Downloaded from official GitHub releases only

## Testing Checklist

Before publishing:

- [ ] Package builds without errors: `choco pack`
- [ ] Installation works: `choco install loom -source . -y`
- [ ] Binaries are in PATH: `where loom` and `where looms`
- [ ] Binaries execute: `loom --help` and `looms --help`
- [ ] Patterns installed: Check `$env:LOOM_DATA_DIR\patterns\`
- [ ] Environment variable set: `$env:LOOM_DATA_DIR`
- [ ] Uninstall works: `choco uninstall loom -y`
- [ ] Upgrade works: `choco upgrade loom -source . -y`

## Troubleshooting

### "Package already exists"
```powershell
# Remove old package
Remove-Item *.nupkg
choco pack
```

### "Checksum mismatch"
Recalculate checksums:
```powershell
checksum -t sha256 -f path/to/binary.zip
```

### "Cannot find package"
Make sure you're in the chocolatey directory:
```powershell
Get-Location  # Should show .../loom/chocolatey
```

## Addressing Antivirus False Positives

### Why False Positives Occur

PowerShell scripts that perform legitimate package manager operations can trigger heuristic malware detection:

1. **Web downloads during execution** - Common malware behavior, but required for package installation
2. **Binary extraction and execution** - Required to install software, but also used by trojans
3. **Environment variable modification** - Normal configuration, but can be suspicious
4. **Checksum bypassing** - Using zero/placeholder checksums tells AV "accept any file"

### Our Mitigation Strategy

The Loom Chocolatey package has been hardened:

| Security Measure | Implementation | Benefit |
|-----------------|----------------|---------|
| **Security documentation** | 20+ lines of comments in `chocolateyinstall.ps1` | Explains intent clearly |
| **Real checksums** | `update-checksums.ps1` script | Verifies authentic binaries |
| **Chocolatey helpers** | `Get-ChocolateyWebFile` | Trusted by security scanners |
| **Official sources** | Only `github.com/teradata-labs` | Prevents supply chain attacks |

### Step-by-Step: Submit to Microsoft

If Microsoft Defender flags the package:

1. **Navigate to submission portal**:
   - URL: https://www.microsoft.com/wdsi/filesubmission
   - Sign in with Microsoft account

2. **Select submission type**:
   - Choose: "Software developer - Submit suspected false positive"

3. **Provide package information**:
   ```
   Product name: Loom AI Agent Framework - Chocolatey Package
   Product version: 1.0.2
   Vendor: Teradata Labs
   Website: https://github.com/teradata-labs/loom
   License: Apache 2.0 Open Source
   ```

4. **Upload files**:
   - Upload the `.nupkg` file
   - Include `chocolateyinstall.ps1` separately

5. **Describe the issue**:
   ```
   This is the official Chocolatey package for Loom, an open-source LLM agent
   framework from Teradata Labs. The PowerShell install script performs standard
   package manager operations:

   1. Downloads binaries from official GitHub releases (with SHA256 verification)
   2. Extracts to Chocolatey tools directory
   3. Creates shims for PATH access
   4. Downloads YAML configuration files
   5. Sets LOOM_DATA_DIR environment variable

   All operations are documented with security comments. Source code available at:
   https://github.com/teradata-labs/loom/tree/main/packaging/windows/chocolatey
   ```

6. **Include checksums**:
   ```powershell
   # Generate package checksum
   Get-FileHash -Algorithm SHA256 loom.1.0.2.nupkg
   ```

7. **Wait for review**: Usually 2-5 business days

### Step-by-Step: Submit to VirusTotal

1. **Upload to VirusTotal**: https://www.virustotal.com/gui/home/upload
2. **Share results**: Add URL to `loom.nuspec` as `<packageScanResultsUrl>`
3. **Monitor**: Track false positive rate across AV vendors

### Future: Code Signing

For production releases, consider signing PowerShell scripts:

```powershell
# Obtain Authenticode certificate from trusted CA
$cert = Get-PfxCertificate -FilePath "codesigning.pfx"

# Sign the install script
Set-AuthenticodeSignature `
    -FilePath "tools\chocolateyinstall.ps1" `
    -Certificate $cert `
    -TimestampServer "http://timestamp.digicert.com"

# Verify signature
Get-AuthenticodeSignature "tools\chocolateyinstall.ps1"
```

Signed scripts are trusted more by Windows Defender and enterprise AV.

### Monitoring False Positives

Track detection rates:
- **VirusTotal**: Upload `.nupkg` for multi-engine scanning
- **Chocolatey Moderation**: Human reviewers validate package safety
- **GitHub Issues**: Users report AV flags

## References

- [Chocolatey Documentation](https://docs.chocolatey.org/)
- [Package Guidelines](https://docs.chocolatey.org/en-us/create/create-packages)
- [Helper Functions](https://docs.chocolatey.org/en-us/create/functions/)
- [Moderation Process](https://docs.chocolatey.org/en-us/community-repository/moderation)
- [Microsoft Security Intelligence](https://www.microsoft.com/wdsi/filesubmission)
- [VirusTotal](https://www.virustotal.com/)
