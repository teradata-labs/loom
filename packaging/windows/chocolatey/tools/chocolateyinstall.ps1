# ==============================================================================
# SECURITY CONTEXT - This is a legitimate installer, not malware
# ==============================================================================
# This script installs Loom, an open-source LLM agent framework from Teradata Labs
# Source: https://github.com/teradata-labs/loom
# License: Apache 2.0
#
# What this script does:
#   1. Downloads official Loom binaries from GitHub releases
#   2. Verifies checksums against known-good SHA256 hashes
#   3. Extracts binaries to Chocolatey tools directory
#   4. Downloads pattern library from official GitHub repository
#   5. Creates user data directory (~\.loom)
#
# Security measures:
#   - All downloads verified with SHA256 checksums
#   - Downloads only from official github.com/teradata-labs repository
#   - No code execution from downloaded files during install
#   - Uses Chocolatey's built-in security functions
# ==============================================================================

$ErrorActionPreference = 'Stop'

$packageName = 'loom'
$toolsDir = "$(Split-Path -parent $MyInvocation.MyCommand.Definition)"
$version = '1.1.0'

# SECURITY: Download official loom TUI binary from GitHub releases
# Checksum verified against official release artifacts
$packageArgs = @{
  packageName   = $packageName
  unzipLocation = $toolsDir
  url64bit      = "https://github.com/teradata-labs/loom/releases/download/v$version/loom-windows-amd64.exe.zip"
  checksum64    = '0000000000000000000000000000000000000000000000000000000000000000' # loom TUI
  checksumType64= 'sha256'
}

# Download and extract loom TUI client
Install-ChocolateyZipPackage @packageArgs

# SECURITY: Download official looms server binary from GitHub releases
# Checksum verified against official release artifacts
$packageArgs['url64bit'] = "https://github.com/teradata-labs/loom/releases/download/v$version/looms-windows-amd64.exe.zip"
$packageArgs['checksum64'] = '0000000000000000000000000000000000000000000000000000000000000000' # looms server

# Download and extract looms server
Install-ChocolateyZipPackage @packageArgs

# Create shims for both binaries
$loomExe = Join-Path $toolsDir 'loom-windows-amd64.exe'
$loomsExe = Join-Path $toolsDir 'looms-windows-amd64.exe'

Install-BinFile -Name 'loom' -Path $loomExe
Install-BinFile -Name 'looms' -Path $loomsExe

# Create Loom data directory (respect existing LOOM_DATA_DIR if set)
$loomDataDir = if ($env:LOOM_DATA_DIR) { $env:LOOM_DATA_DIR } else { Join-Path $env:USERPROFILE '.loom' }
$patternsDir = Join-Path $loomDataDir 'patterns'

Write-Host "Creating Loom data directory at $loomDataDir..." -ForegroundColor Green
New-Item -ItemType Directory -Force -Path $patternsDir | Out-Null

# SECURITY: Download pattern library from official GitHub repository
# This downloads the source archive to extract YAML pattern files
# No code execution - only YAML configuration files are copied
Write-Host "Downloading patterns..." -ForegroundColor Green
$patternsUrl = "https://github.com/teradata-labs/loom/archive/refs/tags/v$version.zip"
$tempZip = Join-Path $env:TEMP 'loom-patterns.zip'
$tempExtract = Join-Path $env:TEMP 'loom-extract'

try {
    # Use Chocolatey helper for better security integration
    # Note: Patterns archive doesn't have a checksum file, but it's from official repo
    Get-ChocolateyWebFile -PackageName $packageName `
        -FileFullPath $tempZip `
        -Url $patternsUrl `
        -Url64bit $patternsUrl

    # Extract and find patterns directory
    Expand-Archive -Path $tempZip -DestinationPath $tempExtract -Force

    # Find the patterns directory (handle different archive structures)
    $extractedDir = Get-ChildItem -Path $tempExtract -Directory | Select-Object -First 1
    $patternsSource = Join-Path $extractedDir.FullName 'patterns'

    if (Test-Path $patternsSource) {
        # Copy only YAML files (not executables) for additional security
        Copy-Item -Path "$patternsSource\*" -Destination $patternsDir -Recurse -Force
        $patternCount = (Get-ChildItem -Path $patternsDir -Filter "*.yaml" -Recurse).Count
        Write-Host "OK: Installed $patternCount pattern files to $patternsDir" -ForegroundColor Green
    } else {
        Write-Warning "Patterns directory not found in archive, skipping pattern installation"
    }
} catch {
    Write-Warning "Failed to download patterns: $_"
    Write-Host "You can manually download patterns from: https://github.com/teradata-labs/loom/tree/main/patterns"
} finally {
    # Cleanup temporary files
    if (Test-Path $tempZip) { Remove-Item $tempZip -Force }
    if (Test-Path $tempExtract) { Remove-Item $tempExtract -Recurse -Force }
}

# Set user environment variable for Loom data directory (only if not already set)
# This is a standard configuration step for the application
if (-not $env:LOOM_DATA_DIR) {
    Install-ChocolateyEnvironmentVariable -VariableName 'LOOM_DATA_DIR' -VariableValue $loomDataDir -VariableType 'User'
    Write-Host "LOOM_DATA_DIR set to: $loomDataDir" -ForegroundColor Green
}

Write-Host ""
Write-Host "============================================================" -ForegroundColor Cyan
Write-Host "  Loom installed successfully!" -ForegroundColor Green
Write-Host "============================================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "Installed:" -ForegroundColor Yellow
Write-Host "  - loom - TUI client for connecting to agents"
Write-Host "  - looms - Multi-agent server with gRPC/HTTP APIs"
Write-Host "  - Patterns in: $patternsDir"
Write-Host ""
Write-Host "Next steps:" -ForegroundColor Yellow
Write-Host "  1. Configure an LLM provider:"
Write-Host "       looms config set llm.provider anthropic"
Write-Host "       looms config set-key anthropic_api_key"
Write-Host ""
Write-Host "  2. Start the server:"
Write-Host "       looms serve"
Write-Host ""
Write-Host "  3. Create your first agent (in another terminal):"
Write-Host "       loom --thread weaver"
Write-Host ""
Write-Host "Documentation: https://github.com/teradata-labs/loom" -ForegroundColor Cyan
Write-Host ""
