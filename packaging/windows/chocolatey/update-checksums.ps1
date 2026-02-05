# Update chocolateyinstall.ps1 with real checksums from GitHub releases
# Usage: .\update-checksums.ps1 -Version "1.0.2"

param(
    [Parameter(Mandatory=$true)]
    [string]$Version
)

$ErrorActionPreference = 'Stop'

# GitHub release URLs
$baseUrl = "https://github.com/teradata-labs/loom/releases/download/v$Version"
$loomChecksumUrl = "$baseUrl/loom-windows-amd64.exe.zip.sha256"
$loomsChecksumUrl = "$baseUrl/looms-windows-amd64.exe.zip.sha256"

Write-Host "Fetching checksums for version $Version..." -ForegroundColor Cyan

try {
    # Fetch and validate loom checksum
    $loomResponse = Invoke-WebRequest -Uri $loomChecksumUrl -UseBasicParsing
    $loomChecksumRaw = [System.Text.Encoding]::UTF8.GetString($loomResponse.Content).Trim()
    $loomChecksum = ($loomChecksumRaw -split '\s+')[0]

    if ($loomChecksum.Length -ne 64 -or $loomChecksum -notmatch '^[0-9a-fA-F]{64}$') {
        throw "Invalid loom checksum: '$loomChecksum' (length: $($loomChecksum.Length))"
    }

    # Fetch and validate looms checksum
    $loomsResponse = Invoke-WebRequest -Uri $loomsChecksumUrl -UseBasicParsing
    $loomsChecksumRaw = [System.Text.Encoding]::UTF8.GetString($loomsResponse.Content).Trim()
    $loomsChecksum = ($loomsChecksumRaw -split '\s+')[0]

    if ($loomsChecksum.Length -ne 64 -or $loomsChecksum -notmatch '^[0-9a-fA-F]{64}$') {
        throw "Invalid looms checksum: '$loomsChecksum' (length: $($loomsChecksum.Length))"
    }

    Write-Host "✓ loom checksum: $loomChecksum" -ForegroundColor Green
    Write-Host "✓ looms checksum: $loomsChecksum" -ForegroundColor Green

    # Read install script line by line
    $scriptPath = Join-Path $PSScriptRoot "tools\chocolateyinstall.ps1"
    $lines = Get-Content $scriptPath

    # Track if we found what we need to replace
    $foundVersion = $false
    $foundLoomChecksum = $false
    $foundLoomsChecksum = $false

    # Process each line
    for ($i = 0; $i -lt $lines.Length; $i++) {
        $line = $lines[$i]

        # Update version
        if ($line -match "^\`$version = '.*'") {
            $lines[$i] = "`$version = '$Version'"
            $foundVersion = $true
            Write-Host "  Updated version on line $($i+1)" -ForegroundColor Gray
        }
        # Update loom checksum (first checksum64 line with "# loom TUI" comment)
        elseif ($line -match "^\s*checksum64\s*=\s*'[0-9a-fA-F]{64}'\s*#\s*loom TUI") {
            $indent = $line -replace "^(\s*).*", '$1'
            $lines[$i] = "${indent}checksum64    = '$loomChecksum' # loom TUI"
            $foundLoomChecksum = $true
            Write-Host "  Updated loom checksum on line $($i+1)" -ForegroundColor Gray
        }
        # Update looms checksum (packageArgs['checksum64'] line with "# looms server" comment)
        elseif ($line -match "^\s*\`$packageArgs\['checksum64'\]\s*=\s*'[0-9a-fA-F]{64}'\s*#\s*looms server") {
            $lines[$i] = "`$packageArgs['checksum64'] = '$loomsChecksum' # looms server"
            $foundLoomsChecksum = $true
            Write-Host "  Updated looms checksum on line $($i+1)" -ForegroundColor Gray
        }
    }

    # Verify all replacements were made
    if (-not $foundVersion) {
        throw "Could not find version line to update"
    }
    if (-not $foundLoomChecksum) {
        throw "Could not find loom checksum line to update"
    }
    if (-not $foundLoomsChecksum) {
        throw "Could not find looms checksum line to update"
    }

    # Write back to file
    $lines | Set-Content -Path $scriptPath -Encoding UTF8

    Write-Host ""
    Write-Host "✅ Successfully updated chocolateyinstall.ps1" -ForegroundColor Green
    Write-Host "   Version: $Version" -ForegroundColor Yellow
    Write-Host "   Loom checksum: $loomChecksum" -ForegroundColor Yellow
    Write-Host "   Looms checksum: $loomsChecksum" -ForegroundColor Yellow

} catch {
    Write-Error "Failed to update checksums: $_"
    Write-Host ""
    Write-Host "Possible reasons:" -ForegroundColor Yellow
    Write-Host "  1. Release v$Version doesn't exist on GitHub"
    Write-Host "  2. Checksum files not uploaded yet"
    Write-Host "  3. Network connectivity issues"
    Write-Host "  4. chocolateyinstall.ps1 format has changed"
    exit 1
}
