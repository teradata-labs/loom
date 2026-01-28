# Windows Installation Guide for Loom

Complete guide for installing Loom on Windows with multiple installation methods.

## Quick Install (Recommended)

### Option 1: PowerShell Quickstart Script (Full Setup)

The easiest way to get started with Loom on Windows - installs everything with LLM provider configuration:

```powershell
# Clone the repository
git clone https://github.com/teradata-labs/loom
cd loom

# Run the quickstart script
.\quickstart.ps1

# If you get "Running scripts is disabled on this system", use:
powershell -ExecutionPolicy Bypass -File .\quickstart.ps1
```

> **Note**: If you encounter PowerShell execution policy errors, you can either:
> - Use the bypass command above (recommended, no admin required)
> - Or enable scripts for your user: `Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser`

**What it does**:
- ✅ Checks/installs prerequisites (Go, Just, Buf)
- ✅ Builds `looms.exe` and `loom.exe`
- ✅ Installs 90+ patterns to `$LOOM_DATA_DIR/patterns/`
- ✅ Creates configuration file
- ✅ Sets up environment variables
- ✅ Interactive LLM provider setup
- ✅ Optional web search API configuration

**From Git Bash**: You can also run `./quickstart.sh` which automatically redirects to `quickstart.ps1`.

---

### Option 2: Package Managers (Coming Soon)

Once published to package repositories, you'll be able to install with one command:

#### Scoop (Developer-Friendly)
```powershell
scoop install loom-server
# or install both client and server
scoop install loom loom-server
```

#### winget (Microsoft Official - Windows 10/11)
```powershell
winget install Teradata.Loom
```

#### Chocolatey (Most Popular)
```powershell
choco install loom
```

**Current Status**: Manifests are ready in `packaging/windows/`. See [Publishing](#publishing-to-package-managers) section below.

---

## Manual Installation

### Prerequisites

1. **Go 1.25+**
   ```powershell
   # With winget
   winget install GoLang.Go

   # With Chocolatey
   choco install golang

   # Or download from https://go.dev/dl/
   ```

2. **Just (Task Runner)**
   ```powershell
   # With Chocolatey
   choco install just

   # With Scoop
   scoop install just

   # Or download from https://github.com/casey/just/releases
   ```

3. **Buf (Protocol Buffers)**
   ```powershell
   # With winget
   winget install bufbuild.buf

   # Or download from https://buf.build/docs/installation
   ```

### Build from Source

```powershell
# Clone repository
git clone https://github.com/teradata-labs/loom
cd loom

# Generate proto files
buf generate

# Generate weaver template
go run ./cmd/generate-weaver

# Build binaries
go build -tags fts5 -o bin/looms.exe ./cmd/looms
go build -tags fts5 -o bin/loom.exe ./cmd/loom

# Install patterns
xcopy /E /I /Y patterns %LOOM_DATA_DIR%\patterns

# Add to PATH (PowerShell - Admin required)
$binDir = "$PWD\bin"
[Environment]::SetEnvironmentVariable("Path", "$binDir;$env:Path", "User")
```

### Install from Pre-built Binaries

Download from [GitHub Releases](https://github.com/teradata-labs/loom/releases):

```powershell
# Download binaries
$version = "1.0.1"
Invoke-WebRequest "https://github.com/teradata-labs/loom/releases/download/v$version/loom-windows-amd64.exe.zip" -OutFile loom.zip
Invoke-WebRequest "https://github.com/teradata-labs/loom/releases/download/v$version/looms-windows-amd64.exe.zip" -OutFile looms.zip

# Extract
Expand-Archive loom.zip -DestinationPath . -Force
Expand-Archive looms.zip -DestinationPath . -Force

# Move to user bin directory
mkdir -Force $env:USERPROFILE\.local\bin
Move-Item loom-windows-amd64.exe $env:USERPROFILE\.local\bin\loom.exe -Force
Move-Item looms-windows-amd64.exe $env:USERPROFILE\.local\bin\looms.exe -Force

# Add to PATH
$binDir = "$env:USERPROFILE\.local\bin"
[Environment]::SetEnvironmentVariable("Path", "$binDir;$env:Path", "User")

# Reload environment in current session
$env:Path = [Environment]::GetEnvironmentVariable("Path", "User")
```

---

## Configuration

### Configure LLM Provider

```powershell
# Set provider (choose one)
looms config set llm.provider anthropic     # Claude
looms config set llm.provider bedrock       # AWS Bedrock
looms config set llm.provider openai        # GPT-4
looms config set llm.provider ollama        # Local models

# Set API key (interactive prompt)
looms config set-key anthropic_api_key

# Or use environment variable
$env:ANTHROPIC_API_KEY = "your-api-key"
```

### Configuration File Location

Default: `%LOOM_DATA_DIR%\looms.yaml` (usually `%USERPROFILE%\.loom\looms.yaml`)

Example configuration:
```yaml
server:
  host: "0.0.0.0"
  port: 60051

database:
  path: "%LOOM_DATA_DIR%/loom.db"

llm:
  provider: "anthropic"
  anthropic_api_key: "sk-ant-..."
```

---

## Quick Start

### Start the Server

```powershell
# Start Loom server
looms serve

# Server starts on:
# - gRPC: localhost:60051
# - HTTP: http://localhost:5006
# - Swagger UI: http://localhost:5006/swagger-ui
```

### Create Your First Agent

```powershell
# In another PowerShell window
loom --thread weaver

# Then type your request:
# "Create a code review assistant that checks for security issues"

# The weaver will:
# 1. Analyze your requirements
# 2. Select appropriate patterns
# 3. Generate complete YAML configuration
# 4. Activate the agent thread
```

### Connect to Existing Agent

```powershell
loom --thread your-agent-name
```

---

## Troubleshooting

### Common Issues

#### "command not found: looms"

**Solution**: PATH not updated. Restart PowerShell or add to PATH manually:
```powershell
$env:Path = [Environment]::GetEnvironmentVariable("Path", "User")
```

#### "cannot execute binary file"

**Solution**: You may have downloaded the wrong architecture. Windows requires `-windows-amd64.exe` binaries.

#### "go: cannot find main module"

**Solution**: Run commands from the loom repository root, or set `GOWORK=off`:
```powershell
$env:GOWORK = "off"
```

#### SQLite FTS5 errors

**Solution**: Always build/run with `-tags fts5`:
```powershell
go build -tags fts5 ./cmd/looms
go test -tags fts5 ./...
```

### Build Issues

#### MinGW/MSYS GCC Required

SQLite with FTS5 requires CGO (C compiler). If you see compilation errors:

```powershell
# Install MinGW via Chocolatey
choco install mingw

# Or via MSYS2
choco install msys2
# Then install gcc: pacman -S mingw-w64-x86_64-gcc
```

#### Buf Generate Fails

```powershell
# Update buf modules
buf mod update

# Regenerate
buf generate
```

---

## Publishing to Package Managers

### For Maintainers

When releasing a new version, update and submit package manifests:

#### 1. Update Scoop Manifests

```powershell
cd scoop

# Update version in loom.json and loom-server.json
# Calculate SHA256 hashes
$version = "1.0.1"
Invoke-WebRequest "https://github.com/teradata-labs/loom/releases/download/v$version/loom-windows-amd64.exe.zip" -OutFile loom.zip
(Get-FileHash loom.zip -Algorithm SHA256).Hash

# Update hash in loom.json
# Submit PR to https://github.com/ScoopInstaller/Extras
```

#### 2. Update winget Manifests

```powershell
cd winget

# Update version in all three YAML files
# Update InstallerSha256 with calculated hashes
# Validate
winget validate --manifest .

# Submit PR to https://github.com/microsoft/winget-pkgs
```

#### 3. Update Chocolatey Package

```powershell
cd chocolatey

# Update version in loom.nuspec
# Update checksums in tools/chocolateyinstall.ps1
# Build and test
choco pack
choco install loom -source . -y

# Push to Chocolatey (requires API key)
choco push loom.1.0.1.nupkg --source https://push.chocolatey.org/
```

See individual README files in `packaging/windows/scoop/`, `packaging/windows/winget/`, and `packaging/windows/chocolatey/` directories for detailed instructions.

---

## Testing

### Automated Testing

The Windows installation is tested automatically via GitHub Actions:

- **Workflow**: `.github/workflows/windows-quickstart.yml`
- **Runs on**: `windows-latest` (Windows Server 2022)
- **Tests**: Full installation, pattern download, binary functionality

View test results: [GitHub Actions](https://github.com/teradata-labs/loom/actions/workflows/windows-quickstart.yml)

### Manual Testing

```powershell
# Test binaries
looms --version
looms --help
loom --help

# Test configuration
looms config list

# Test pattern installation
Get-ChildItem -Path "$env:LOOM_DATA_DIR\patterns" -Filter "*.yaml" -Recurse

# Test server start (Ctrl+C to stop)
looms serve
```

---

## Uninstallation

### Package Manager Uninstall

```powershell
# Scoop
scoop uninstall loom loom-server

# winget
winget uninstall Teradata.Loom

# Chocolatey
choco uninstall loom
```

### Manual Uninstall

```powershell
# Remove binaries
Remove-Item "$env:USERPROFILE\.local\bin\loom.exe"
Remove-Item "$env:USERPROFILE\.local\bin\looms.exe"

# Remove from PATH (manually edit or use GUI)
# System Properties → Advanced → Environment Variables

# Remove data directory (optional - contains patterns, config, database)
Remove-Item -Path "$env:LOOM_DATA_DIR" -Recurse -Force
```

---

## Additional Resources

- **Main README**: [README.md](../../README.md)
- **Documentation**: [docs/](../../docs/)
- **Scoop Manifests**: [packaging/windows/scoop/](../../packaging/windows/scoop/)
- **winget Manifests**: [packaging/windows/winget/](../../packaging/windows/winget/)
- **Chocolatey Package**: [packaging/windows/chocolatey/](../../packaging/windows/chocolatey/)
- **GitHub Issues**: https://github.com/teradata-labs/loom/issues

---

## Contributing

Found an issue with Windows installation? Please report it:
- [Open an Issue](https://github.com/teradata-labs/loom/issues/new)
- Include your Windows version and error messages
- Mention if you're using Git Bash, PowerShell, or cmd.exe

---

## License

Apache 2.0 - See [LICENSE](LICENSE)
