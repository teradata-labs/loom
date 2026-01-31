# Loom Quickstart Installation

Get up and running with Loom in under 5 minutes using our automated installation script.

## Prerequisites

- **macOS or Linux** (Windows users see below)
- **Homebrew** (macOS) or **Cargo/Rust** (Linux)
- An LLM provider (Anthropic, AWS Bedrock, Ollama, etc.)
- Optional: Web search API key (Tavily or Brave Search)

## One-Command Installation

### macOS / Linux

```bash
# Clone the repository
git clone https://github.com/teradata-labs/loom
cd loom

# Run the quickstart installer
./quickstart.sh
```

The script will:
1. ✓ Check and install prerequisites (Go, Just, Buf)
2. ✓ **Ask for installation directories** (binaries and data)
3. ✓ Build Loom binaries
4. ✓ Install patterns and documentation
5. ✓ Install binaries to your chosen directory
6. ✓ **Set environment variables** (LOOM_DATA_DIR, LOOM_BIN_DIR)
7. ✓ Configure your LLM provider interactively
8. ✓ Configure web search API keys (optional)
9. ✓ Create configuration files

### Windows

```powershell
# Clone the repository
git clone https://github.com/teradata-labs/loom
cd loom

# Run the quickstart installer
.\quickstart.ps1
```

The PowerShell script does the same as the bash version with Windows-specific:
- Automatic PATH configuration (user-level environment variables)
- Windows Credential Manager for secure API key storage
- PowerShell-friendly prompts and error handling

## What Gets Installed

### Binaries (Configurable Location)
**Default**: `$HOME/.local/bin/` (macOS/Linux) or `%USERPROFILE%\.local\bin\` (Windows)

- `looms` / `looms.exe` - Loom server
- `loom` / `loom.exe` - TUI client

**Custom Installation**: The installer prompts for a custom binary directory if desired.

### Data & Configuration (Configurable Location)
**Default**: `$LOOM_DATA_DIR` (defaults to `$HOME/.loom/` on macOS/Linux or `%USERPROFILE%\.loom\` on Windows)

- `patterns/` - 90+ reusable YAML patterns
- `documentation/` - Complete documentation
- `looms.yaml` - Server configuration
- `loom.db` - SQLite database (created on first run)

**Custom Installation**: The installer prompts for a custom data directory if desired.

### Environment Variables
Both installers set these environment variables:

- `LOOM_BIN_DIR` - Where binaries are installed
- `LOOM_DATA_DIR` - Where data and config files are stored
- `PATH` - Updated to include `LOOM_BIN_DIR`

**macOS/Linux**: Variables are added to `~/.bashrc` or `~/.zshrc`
**Windows**: Variables are set at user-level (persistent across sessions)

## LLM Provider Options

The installer supports 8 LLM providers:

| Provider | Authentication | Free Tier |
|----------|---------------|-----------|
| **AWS Bedrock** | SSO/Profile or Access Keys | Pay per use |
| **Anthropic** | API Key | Trial credits |
| **OpenAI** | API Key | Trial credits |
| **Azure OpenAI** | API Key or Entra ID | Enterprise |
| **Mistral AI** | API Key | Trial credits |
| **Google Gemini** | API Key | Free tier available |
| **HuggingFace** | Token | Free tier available |
| **Ollama** | Local (no auth) | Free (local) |

### Recommended for Beginners

1. **Ollama** (Free, Local)
   - No API key required
   - Runs locally on your machine
   - Requires models with tool calling (llama3.1, mistral, qwen2.5)

2. **Anthropic** (Trial Credits)
   - Best tool calling support
   - Claude 3.5 Sonnet or Claude 4.5
   - Get API key: https://console.anthropic.com/

3. **AWS Bedrock** (Pay-as-you-go)
   - Enterprise-grade
   - SSO integration
   - Best for organizations with AWS accounts

## Web Search Setup (Optional)

The installer can configure web search providers:

| Provider | Free Tier | Sign Up |
|----------|-----------|---------|
| **Tavily** | 1000 searches/month | https://tavily.com/ |
| **Brave Search** | 2000 searches/month | https://brave.com/search/api/ |

Both are AI-optimized and free for development use.

## After Installation

### 1. Start Loom Server

```bash
looms serve
```

This starts the gRPC server on port 60051.

### 2. Create Your First Agent

In another terminal, connect to the weaver (meta-agent):

```bash
loom --thread weaver
```

Then describe what you need in plain English:

```
Create a code review assistant that checks for:
- Security vulnerabilities
- Code style issues
- Performance problems
```

The weaver will:
- Analyze your requirements
- Select appropriate patterns
- Generate agent configuration
- Activate the agent thread

### 3. Connect to Your Agent

```bash
loom --thread <thread-id>
```

Now you can chat with your agent!

## Manual Configuration (Skip Installer)

If you prefer manual setup:

### 1. Install Prerequisites

**macOS:**
```bash
brew install go just bufbuild/buf/buf
```

**Linux:**
```bash
# Install Go manually
sudo apt-get install golang-go  # Debian/Ubuntu
sudo dnf install golang          # Fedora/RHEL

# Install Just and Buf
cargo install just
# Buf: see https://buf.build/docs/installation
```

### 2. Build Loom

```bash
cd loom
just build
just install
```

### 3. Configure LLM Provider

```bash
# Example: Anthropic
looms config set llm.provider anthropic
looms config set-key anthropic_api_key

# Example: AWS Bedrock with SSO
looms config set llm.provider bedrock
looms config set llm.bedrock_profile default
looms config set llm.bedrock_region us-west-2
looms config set llm.bedrock_model_id us.anthropic.claude-sonnet-4-5-20250929-v1:0

# Example: Ollama (local)
looms config set llm.provider ollama
looms config set llm.ollama_endpoint http://localhost:11434
looms config set llm.ollama_model llama3.1:8b
```

### 4. Configure Web Search (Optional)

```bash
looms config set-key tavily_api_key
# or
looms config set-key brave_search_api_key
```

## Advanced Configuration

### Custom Installation Directories

Both installers prompt for installation directories:

**Interactive Mode** (default):
```bash
# You'll be asked:
# "Where would you like to install Loom binaries?" [$HOME/.local/bin]
# "Where would you like to store Loom data?" [$LOOM_DATA_DIR]
```

**Non-Interactive Mode** (use defaults):
```bash
# Set environment variables before running
export LOOM_BIN_DIR=/custom/bin
export LOOM_DATA_DIR=/custom/data
./quickstart.sh
```

### Environment Variables

The installer sets these automatically, but you can override them:

```bash
# Bash/Zsh (~/.bashrc or ~/.zshrc)
export LOOM_DATA_DIR="$HOME/.loom"
export LOOM_BIN_DIR="$HOME/.local/bin"
export PATH="$LOOM_BIN_DIR:$PATH"
```

```powershell
# PowerShell (added automatically by quickstart.ps1)
$env:LOOM_DATA_DIR="$env:USERPROFILE\.loom"
$env:LOOM_BIN_DIR="$env:USERPROFILE\.local\bin"
$env:PATH="$env:LOOM_BIN_DIR;$env:PATH"
```

## Manual Windows Installation (Alternative)

The automated PowerShell installer (`quickstart.ps1`) is recommended, but manual installation is also possible:

### 1. Install Prerequisites

- **Go**: https://go.dev/dl/
- **Just**: https://github.com/casey/just#windows
- **Buf**: https://buf.build/docs/installation

### 2. Build and Install

Open PowerShell or Command Prompt:

```powershell
# Clone repository
git clone https://github.com/teradata-labs/loom
cd loom

# Build
just build

# Manual install
mkdir $env:USERPROFILE\.local\bin
copy bin\looms.exe $env:USERPROFILE\.local\bin\
copy bin\loom.exe $env:USERPROFILE\.local\bin\

# Add to PATH
$env:PATH += ";$env:USERPROFILE\.local\bin"
```

### 3. Configure

Configuration files are stored in `%USERPROFILE%\.loom\`:

```powershell
mkdir $env:USERPROFILE\.loom

# Copy patterns
xcopy /E /I patterns $env:USERPROFILE\.loom\patterns
```

Then follow the manual configuration steps above to set up your LLM provider.

## Troubleshooting

### Binary directory not in PATH

**macOS/Linux**:

The installer should add it automatically. If not, add to `~/.bashrc` or `~/.zshrc`:

```bash
export LOOM_BIN_DIR="$HOME/.local/bin"  # or your custom path
export PATH="$LOOM_BIN_DIR:$PATH"
```

Then reload:

```bash
source ~/.bashrc  # or ~/.zshrc
```

**Windows**:

The PowerShell installer sets user-level environment variables. Restart PowerShell or run:

```powershell
$env:PATH = [Environment]::GetEnvironmentVariable("PATH", "User") + ";" + [Environment]::GetEnvironmentVariable("PATH", "Machine")
```

### Environment variables not persisting

**macOS/Linux**: Make sure the installer added exports to your shell config file. Check:

```bash
grep LOOM $HOME/.bashrc  # or $HOME/.zshrc
```

**Windows**: Check user environment variables:

```powershell
[Environment]::GetEnvironmentVariable("LOOM_DATA_DIR", "User")
[Environment]::GetEnvironmentVariable("LOOM_BIN_DIR", "User")
```

### Ollama Connection Refused

Make sure Ollama is running:

```bash
ollama serve
```

And your model is pulled:

```bash
ollama pull llama3.1:8b
```

### AWS SSO Authentication

If using Bedrock with SSO:

```bash
aws sso login --profile <your-profile>
export AWS_REGION=us-west-2
looms serve
```

### Permission Denied on Scripts

Make scripts executable:

```bash
chmod +x quickstart.sh
```

### Build Errors

Make sure all prerequisites are installed:

```bash
just verify
```

If buf or just are not found, reinstall:

```bash
# macOS
brew reinstall just bufbuild/buf/buf

# Linux
cargo install just --force
```

## Next Steps

- **Read the docs**: https://github.com/teradata-labs/loom/tree/main/docs
- **Try examples**: `./examples/README.md`
- **Join the community**: https://github.com/teradata-labs/loom/issues
- **Explore patterns**: `$LOOM_DATA_DIR/patterns/`

## CI/CD Installation

For automated CI/CD pipelines:

```bash
export CI_MODE=true
export SKIP_CREDENTIALS=true
./quickstart.sh
```

This skips interactive prompts and credential configuration.

## Uninstall

To completely remove Loom:

**macOS/Linux**:

```bash
# Remove binaries (use your custom path if different)
rm -f "$LOOM_BIN_DIR/loom" "$LOOM_BIN_DIR/looms"

# Remove data and configuration
rm -rf "$LOOM_DATA_DIR"

# Remove environment variables from shell config
# Edit $HOME/.bashrc or $HOME/.zshrc and remove the Loom section

# Remove source
cd .. && rm -rf loom
```

**Windows**:

```powershell
# Remove binaries
Remove-Item "$env:LOOM_BIN_DIR\loom.exe"
Remove-Item "$env:LOOM_BIN_DIR\looms.exe"

# Remove data and configuration
Remove-Item -Recurse "$env:LOOM_DATA_DIR"

# Remove environment variables
[Environment]::SetEnvironmentVariable("LOOM_DATA_DIR", $null, "User")
[Environment]::SetEnvironmentVariable("LOOM_BIN_DIR", $null, "User")

# Update PATH (remove LOOM_BIN_DIR)
$path = [Environment]::GetEnvironmentVariable("PATH", "User")
$newPath = ($path -split ';' | Where-Object { $_ -ne $env:LOOM_BIN_DIR }) -join ';'
[Environment]::SetEnvironmentVariable("PATH", $newPath, "User")

# Remove source
cd ..
Remove-Item -Recurse loom
```

## Support

- **Documentation**: https://github.com/teradata-labs/loom/tree/main/docs
- **GitHub Issues**: https://github.com/teradata-labs/loom/issues
- **Examples**: https://github.com/teradata-labs/loom/tree/main/examples

---

Built by Teradata | Apache 2.0 License
