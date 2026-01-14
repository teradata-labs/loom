$ErrorActionPreference = 'Stop'

$packageName = 'loom'
$toolsDir = "$(Split-Path -parent $MyInvocation.MyCommand.Definition)"
$version = '1.0.1'

$packageArgs = @{
  packageName   = $packageName
  unzipLocation = $toolsDir
  url64bit      = "https://github.com/teradata-labs/loom/releases/download/v$version/loom-windows-amd64.exe.zip"
  checksum64    = '2e6a7d0c6fe69dc5fd0d461c226352fed694ee0f157a3c024e0b318fc105cf0e'
  checksumType64= 'sha256'
}

# Download and extract loom TUI client
Install-ChocolateyZipPackage @packageArgs

$packageArgs['url64bit'] = "https://github.com/teradata-labs/loom/releases/download/v$version/looms-windows-amd64.exe.zip"
$packageArgs['checksum64'] = '3c538793746f2255a16e3e74f2377a7ee9a7126003bfc1b5ea14d6644aed1f6f'

# Download and extract looms server
Install-ChocolateyZipPackage @packageArgs

# Create shims for both binaries
$loomExe = Join-Path $toolsDir 'loom-windows-amd64.exe'
$loomsExe = Join-Path $toolsDir 'looms-windows-amd64.exe'

Install-BinFile -Name 'loom' -Path $loomExe
Install-BinFile -Name 'looms' -Path $loomsExe

# Create Loom data directory
$loomDataDir = Join-Path $env:USERPROFILE '.loom'
$patternsDir = Join-Path $loomDataDir 'patterns'

Write-Host "Creating Loom data directory at $loomDataDir..." -ForegroundColor Green
New-Item -ItemType Directory -Force -Path $patternsDir | Out-Null

# Download and extract patterns
Write-Host "Downloading patterns..." -ForegroundColor Green
$patternsUrl = "https://github.com/teradata-labs/loom/archive/refs/tags/v$version.zip"
$tempZip = Join-Path $env:TEMP 'loom-patterns.zip'
$tempExtract = Join-Path $env:TEMP 'loom-extract'

try {
    Invoke-WebRequest -Uri $patternsUrl -OutFile $tempZip
    Expand-Archive -Path $tempZip -DestinationPath $tempExtract -Force

    # Find the patterns directory (handle different archive structures)
    $extractedDir = Get-ChildItem -Path $tempExtract -Directory | Select-Object -First 1
    $patternsSource = Join-Path $extractedDir.FullName 'patterns'

    if (Test-Path $patternsSource) {
        Copy-Item -Path "$patternsSource\*" -Destination $patternsDir -Recurse -Force
        $patternCount = (Get-ChildItem -Path $patternsDir -Filter "*.yaml" -Recurse).Count
        Write-Host "✓ Installed $patternCount pattern files to $patternsDir" -ForegroundColor Green
    } else {
        Write-Warning "Patterns directory not found in archive, skipping pattern installation"
    }
} catch {
    Write-Warning "Failed to download patterns: $_"
    Write-Host "You can manually download patterns from: https://github.com/teradata-labs/loom/tree/main/patterns"
} finally {
    # Cleanup
    if (Test-Path $tempZip) { Remove-Item $tempZip -Force }
    if (Test-Path $tempExtract) { Remove-Item $tempExtract -Recurse -Force }
}

# Set environment variable
Install-ChocolateyEnvironmentVariable -VariableName 'LOOM_DATA_DIR' -VariableValue $loomDataDir -VariableType 'User'

Write-Host ""
Write-Host "═══════════════════════════════════════════════════════════" -ForegroundColor Cyan
Write-Host "  Loom installed successfully!" -ForegroundColor Green
Write-Host "═══════════════════════════════════════════════════════════" -ForegroundColor Cyan
Write-Host ""
Write-Host "Installed:" -ForegroundColor Yellow
Write-Host "  • loom - TUI client for connecting to agents"
Write-Host "  • looms - Multi-agent server with gRPC/HTTP APIs"
Write-Host "  • Patterns in: $patternsDir"
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
