$ErrorActionPreference = 'Stop'

$packageName = 'loom'

# Remove shims
Uninstall-BinFile -Name 'loom'
Uninstall-BinFile -Name 'looms'

# Remove environment variable
Uninstall-ChocolateyEnvironmentVariable -VariableName 'LOOM_DATA_DIR' -VariableType 'User'

Write-Host "Loom has been uninstalled." -ForegroundColor Green
Write-Host ""
$loomDataDir = if ($env:LOOM_DATA_DIR) { $env:LOOM_DATA_DIR } else { "$env:USERPROFILE\.loom" }
Write-Host "Note: The following were NOT removed (manual cleanup required):" -ForegroundColor Yellow
Write-Host "  - Loom data directory: $loomDataDir"
Write-Host "  - Patterns: $loomDataDir\patterns"
Write-Host "  - Configuration: $loomDataDir\looms.yaml"
Write-Host "  - Database: $loomDataDir\loom.db"
Write-Host ""
Write-Host "To remove all Loom data:" -ForegroundColor Cyan
Write-Host "  Remove-Item -Path `"$loomDataDir`" -Recurse -Force"
Write-Host ""
