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
#   4. Installs the pattern library bundled inside this package
#   5. Creates user data directory (~\.loom)
#
# Security measures:
#   - Binary downloads verified with SHA256 checksums (Checksum64)
#   - Downloads only from official github.com/teradata-labs releases
#   - Pattern library is bundled in the package, not downloaded (no remote file)
#   - No code execution from downloaded files during install
#   - Uses Chocolatey's built-in security functions
# ==============================================================================

$ErrorActionPreference = 'Stop'

$packageName = 'loom'
$toolsDir = "$(Split-Path -parent $MyInvocation.MyCommand.Definition)"
$version = '1.3.0'

# SECURITY: Download official loom TUI binary from GitHub releases
# Checksum verified against official release artifacts
$packageArgs = @{
  packageName   = $packageName
  unzipLocation = $toolsDir
  url64bit      = "https://github.com/teradata-labs/loom/releases/download/v$version/loom-windows-amd64.exe.zip"
  checksum64    = 'F4EF42BF76EE231235D89E514C3F0AA4866F3006FA7730FB8EE2ABC1CFD48F35' # loom TUI
  checksumType64= 'sha256'
}

# Download and extract loom TUI client
Install-ChocolateyZipPackage @packageArgs

# SECURITY: Download official looms server binary from GitHub releases
# Checksum verified against official release artifacts
$packageArgs['url64bit'] = "https://github.com/teradata-labs/loom/releases/download/v$version/looms-windows-amd64.exe.zip"
$packageArgs['checksum64'] = '4C011824B431E44A3AE039541C614E0E2B05D528162666220F3CF5949A6E105E' # looms server

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

# Install pattern library from the files bundled inside this package.
# Patterns ship in the .nupkg itself (tools\patterns), so there is no remote
# download and nothing to checksum at install time. Only YAML configuration
# files are copied; no code is executed.
$patternsSource = Join-Path $toolsDir 'patterns'

if (Test-Path $patternsSource) {
    Write-Host "Installing patterns..." -ForegroundColor Green
    Copy-Item -Path "$patternsSource\*" -Destination $patternsDir -Recurse -Force
    $patternCount = (Get-ChildItem -Path $patternsDir -Filter "*.yaml" -Recurse).Count
    Write-Host "OK: Installed $patternCount pattern files to $patternsDir" -ForegroundColor Green
} else {
    Write-Warning "Bundled patterns not found at $patternsSource, skipping pattern installation"
    Write-Host "You can manually download patterns from: https://github.com/teradata-labs/loom/tree/main/patterns"
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
