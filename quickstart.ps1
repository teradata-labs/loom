#Requires -Version 5.1

<#
.SYNOPSIS
    Loom Quickstart Installation Script for Windows

.DESCRIPTION
    Automated installation script for Loom agent framework with LLM provider and web search configuration

.NOTES
    Requires PowerShell 5.1 or higher

.EXAMPLE - If you get "Running scripts is disabled on this system"
    This is PowerShell's execution policy blocking the script.

    Option 1 (Recommended - No admin required):
        powershell -ExecutionPolicy Bypass -File .\quickstart.ps1

    Option 2 (Allow scripts for current user - No admin required):
        Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser
        .\quickstart.ps1

    Option 3 (Allow scripts system-wide - Requires admin):
        Run PowerShell as Administrator, then:
        Set-ExecutionPolicy -ExecutionPolicy RemoteSigned
        .\quickstart.ps1
#>

# Enable strict mode
Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# Colors for output
function Write-ColorOutput {
    param(
        [string]$Message,
        [string]$Color = "White"
    )
    Write-Host $Message -ForegroundColor $Color
}

function Write-Success { param([string]$Message) Write-ColorOutput "✓ $Message" "Green" }
function Write-Warning { param([string]$Message) Write-ColorOutput "⚠ $Message" "Yellow" }
function Write-Error { param([string]$Message) Write-ColorOutput "✗ $Message" "Red" }
function Write-Info { param([string]$Message) Write-ColorOutput "$Message" "Cyan" }
function Write-Step { param([string]$Message) Write-ColorOutput "`n$Message" "Blue" }

Write-ColorOutput "╔════════════════════════════════════════════════════════════╗" "Blue"
Write-ColorOutput "║  Loom Quickstart Installation for Windows                 ║" "Blue"
Write-ColorOutput "╚════════════════════════════════════════════════════════════╝" "Blue"
Write-Host ""

# Get script directory
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path

# CI Mode detection
$CIMode = $env:CI_MODE -eq "true" -or $env:GITHUB_ACTIONS -eq "true"
$SkipCredentials = $env:SKIP_CREDENTIALS -eq "true" -or $CIMode

if ($CIMode) {
    Write-ColorOutput "╔════════════════════════════════════════════════════════════╗" "Blue"
    Write-ColorOutput "║  Running in CI mode - skipping interactive prompts        ║" "Blue"
    Write-ColorOutput "╚════════════════════════════════════════════════════════════╝" "Blue"
    Write-Host ""
}

# ========================================
# Step 0: Check system requirements
# ========================================
Write-Step "[1/8] Checking system requirements..."
Write-Host ""

# Check if running as administrator (optional but recommended)
$IsAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $IsAdmin) {
    Write-Warning "Not running as Administrator - some features may be limited"
    Write-Info "  For best experience, run PowerShell as Administrator"
    Write-Host ""
}

Write-Success "Windows detected: $([System.Environment]::OSVersion.VersionString)"
Write-Host ""

# ========================================
# Step 1: Prompt for installation directories
# ========================================
Write-Step "[2/8] Configuring installation directories..."
Write-Host ""

if (-not $CIMode) {
    Write-Info "Where would you like to install Loom binaries?"
    Write-Info "  Default: $env:USERPROFILE\.local\bin"
    Write-Host ""
    $BinDirInput = Read-Host "Enter binary directory (press Enter for default)"
    if ([string]::IsNullOrWhiteSpace($BinDirInput)) {
        $BinDir = "$env:USERPROFILE\.local\bin"
    } else {
        $BinDir = $BinDirInput
    }

    Write-Host ""
    Write-Info "Where would you like to store Loom data (patterns, configs, databases)?"
    Write-Info "  Default: $env:USERPROFILE\.loom"
    Write-Host ""
    $DataDirInput = Read-Host "Enter data directory (press Enter for default)"
    if ([string]::IsNullOrWhiteSpace($DataDirInput)) {
        $DataDir = "$env:USERPROFILE\.loom"
    } else {
        $DataDir = $DataDirInput
    }
} else {
    $BinDir = "$env:USERPROFILE\.local\bin"
    $DataDir = "$env:USERPROFILE\.loom"
}

Write-Host ""
Write-Success "Binary directory: $BinDir"
Write-Success "Data directory: $DataDir"
Write-Host ""

# Create directories
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null

# ========================================
# Step 2: Check and prompt for prerequisites
# ========================================
Write-Step "[3/8] Checking prerequisites (go, just, buf)..."
Write-Host ""

$NeedsInstall = $false

# Check Go
$GoInstalled = Get-Command go -ErrorAction SilentlyContinue
if (-not $GoInstalled) {
    Write-Warning "Go not found"
    $NeedsInstall = $true
} else {
    $GoVersion = & go version
    Write-Success "Go installed: $GoVersion"
}

# Check Just
$JustInstalled = Get-Command just -ErrorAction SilentlyContinue
if (-not $JustInstalled) {
    Write-Warning "Just not found"
    $NeedsInstall = $true
} else {
    $JustVersion = & just --version
    Write-Success "Just installed: $JustVersion"
}

# Check Buf
$BufInstalled = Get-Command buf -ErrorAction SilentlyContinue
if (-not $BufInstalled) {
    Write-Warning "Buf not found"
    $NeedsInstall = $true
} else {
    $BufVersion = & buf --version
    Write-Success "Buf installed: $BufVersion"
}

Write-Host ""

if ($NeedsInstall) {
    Write-ColorOutput "╔════════════════════════════════════════════════════════════╗" "Yellow"
    Write-ColorOutput "║  Missing Prerequisites - Manual Installation Required     ║" "Yellow"
    Write-ColorOutput "╚════════════════════════════════════════════════════════════╝" "Yellow"
    Write-Host ""

    Write-Info "Please install the following prerequisites:"
    Write-Host ""

    if (-not $GoInstalled) {
        Write-ColorOutput "1. Go (Golang):" "Yellow"
        Write-Info "   Download: https://go.dev/dl/"
        Write-Info "   Or with winget: winget install GoLang.Go"
        Write-Host ""
    }

    if (-not $JustInstalled) {
        Write-ColorOutput "2. Just (task runner):" "Yellow"
        Write-Info "   Download: https://github.com/casey/just/releases"
        Write-Info "   Or with cargo: cargo install just"
        Write-Info "   Or with scoop: scoop install just"
        Write-Host ""
    }

    if (-not $BufInstalled) {
        Write-ColorOutput "3. Buf (Protocol Buffers):" "Yellow"
        Write-Info "   Download: https://buf.build/docs/installation"
        Write-Info "   Or with winget: winget install bufbuild.buf"
        Write-Host ""
    }

    Write-Warning "After installing prerequisites, run this script again."
    exit 1
}

# ========================================
# Step 3: Build Loom
# ========================================
Write-Step "[4/8] Building Loom..."
Write-Host ""

Push-Location $ScriptDir
try {
    # Generate proto files
    Write-Info "Generating proto files..."
    & buf generate
    if ($LASTEXITCODE -ne 0) {
        Write-Error "buf generate failed"
        exit 1
    }

    # Generate weaver.yaml from template
    Write-Info "Generating weaver.yaml from template..."
    & go run ./cmd/generate-weaver
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Failed to generate weaver.yaml"
        exit 1
    }

    # Create bin directory
    New-Item -ItemType Directory -Force -Path "$ScriptDir\bin" | Out-Null

    # Build looms server
    Write-Info "Building Loom server (looms.exe)..."
    $env:GOWORK = "off"
    & go build -tags fts5 -o "$ScriptDir\bin\looms.exe" ./cmd/looms
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Failed to build looms.exe"
        exit 1
    }

    # Build loom TUI client
    Write-Info "Building Loom TUI client (loom.exe)..."
    & go build -tags fts5 -o "$ScriptDir\bin\loom.exe" ./cmd/loom
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Failed to build loom.exe"
        exit 1
    }

    # Verify binaries exist
    if (-not (Test-Path "$ScriptDir\bin\looms.exe")) {
        Write-Error "Build failed - bin\looms.exe not found"
        exit 1
    }
    if (-not (Test-Path "$ScriptDir\bin\loom.exe")) {
        Write-Error "Build failed - bin\loom.exe not found"
        exit 1
    }

    Write-Success "Loom built successfully"
    Write-Host ""

    # Install patterns
    Write-Info "Installing patterns to $DataDir\patterns..."
    $PatternsDir = "$DataDir\patterns"
    New-Item -ItemType Directory -Force -Path $PatternsDir | Out-Null

    if (Test-Path "$ScriptDir\patterns") {
        Copy-Item -Path "$ScriptDir\patterns\*" -Destination $PatternsDir -Recurse -Force
        $PatternCount = (Get-ChildItem -Path $PatternsDir -Filter "*.yaml" -Recurse).Count
        Write-Success "Installed $PatternCount pattern files to $PatternsDir"
    } else {
        Write-Warning "Patterns directory not found, skipping"
    }
    Write-Host ""

    # Install documentation
    Write-Info "Installing documentation to $DataDir\documentation..."
    $DocsDir = "$DataDir\documentation"
    New-Item -ItemType Directory -Force -Path $DocsDir | Out-Null

    if (Test-Path "$ScriptDir\docs") {
        Copy-Item -Path "$ScriptDir\docs\*" -Destination $DocsDir -Recurse -Force
        $DocCount = (Get-ChildItem -Path $DocsDir -Filter "*.md" -Recurse).Count
        Write-Success "Installed $DocCount documentation files to $DocsDir"
    } else {
        Write-Warning "Documentation directory not found, skipping"
    }
    Write-Host ""

    # Install binaries
    Write-Info "Installing binaries to $BinDir..."
    Copy-Item -Path "$ScriptDir\bin\looms.exe" -Destination "$BinDir\looms.exe" -Force
    Copy-Item -Path "$ScriptDir\bin\loom.exe" -Destination "$BinDir\loom.exe" -Force
    Write-Success "Binaries installed to $BinDir"
    Write-Host ""

} finally {
    Pop-Location
}

# ========================================
# Step 4: Configure PATH environment variable
# ========================================
Write-Step "[5/8] Configuring PATH environment variable..."
Write-Host ""

$CurrentPath = [Environment]::GetEnvironmentVariable("PATH", "User")
if ($CurrentPath -notlike "*$BinDir*") {
    Write-Info "Adding $BinDir to user PATH..."

    if (-not $CIMode) {
        $AddToPath = Read-Host "Add $BinDir to your PATH? (Y/n)"
        if ([string]::IsNullOrWhiteSpace($AddToPath) -or $AddToPath -match '^[Yy]') {
            $NewPath = "$BinDir;$CurrentPath"
            [Environment]::SetEnvironmentVariable("PATH", $NewPath, "User")
            $env:PATH = "$BinDir;$env:PATH"
            Write-Success "Added to PATH (user-level)"
            Write-Warning "You may need to restart PowerShell for PATH changes to take effect"
        } else {
            Write-Warning "Skipped PATH configuration"
            Write-Info "  Add manually: `$env:PATH += `";$BinDir`""
        }
    } else {
        $NewPath = "$BinDir;$CurrentPath"
        [Environment]::SetEnvironmentVariable("PATH", $NewPath, "User")
        Write-Success "Added to PATH (user-level)"
    }
} else {
    Write-Success "$BinDir is already in PATH"
}
Write-Host ""

# ========================================
# Step 5: Create environment variable file
# ========================================
Write-Info "Setting Loom environment variables..."

# Set LOOM_DATA_DIR environment variable
[Environment]::SetEnvironmentVariable("LOOM_DATA_DIR", $DataDir, "User")
$env:LOOM_DATA_DIR = $DataDir
Write-Success "Set LOOM_DATA_DIR=$DataDir"

# Set LOOM_BIN_DIR environment variable
[Environment]::SetEnvironmentVariable("LOOM_BIN_DIR", $BinDir, "User")
$env:LOOM_BIN_DIR = $BinDir
Write-Success "Set LOOM_BIN_DIR=$BinDir"

Write-Host ""

# ========================================
# Step 6: Initialize Loom configuration
# ========================================
Write-Step "[6/8] Initializing Loom configuration..."
Write-Host ""

Write-Info "Creating $DataDir\looms.yaml..."

$ConfigContent = @"
# Loom Server Configuration
server:
  host: "0.0.0.0"
  port: 60051

# Database stored in Loom data directory
database:
  path: "$($DataDir -replace '\\', '/')/loom.db"

# Communication system (shared memory, message queue)
communication:
  store:
    backend: sqlite
    path: "$($DataDir -replace '\\', '/')/loom.db"

# Observability (optional - requires Hawk)
observability:
  enabled: false

# MCP servers (add your own)
mcp:
  servers: {}

# No pre-configured agents - use the weaver to create threads on demand
agents:
  agents: {}
"@

$ConfigContent | Out-File -FilePath "$DataDir\looms.yaml" -Encoding UTF8
Write-Success "Loom configuration initialized"
Write-Info "  Config file: $DataDir\looms.yaml"
Write-Host ""

# ========================================
# Step 7: Configure LLM provider (interactive)
# ========================================
Write-Step "[7/8] Configuring LLM provider..."
Write-Host ""

$ConfigureLLM = "n"

if (-not $SkipCredentials) {
    Write-ColorOutput "Which LLM provider do you want to configure?" "Yellow"
    Write-Host ""
    Write-Host "  1) AWS Bedrock (with SSO/Profile) - For users with AWS profiles configured"
    Write-Host "  2) AWS Bedrock (with Access Keys) - For users with AWS access keys"
    Write-Host "  3) Anthropic API - Direct Anthropic access"
    Write-Host "  4) OpenAI - GPT-4 models, o1 reasoning models"
    Write-Host "  5) Azure OpenAI - Enterprise Microsoft integration"
    Write-Host "  6) Mistral AI - Open & commercial models"
    Write-Host "  7) Google Gemini - Google's latest AI models"
    Write-Host "  8) HuggingFace - 1M+ open source models"
    Write-Host "  9) Ollama - Local/offline models (requires tool calling support)"
    Write-Host " 10) Skip for now - Configure later manually"
    Write-Host ""

    $LLMChoice = Read-Host "Enter choice [1]"
    if ([string]::IsNullOrWhiteSpace($LLMChoice)) { $LLMChoice = "1" }

    switch ($LLMChoice) {
        "1" {
            # AWS Bedrock with SSO/Profile
            $ConfigureLLM = "y"
            Write-Host ""

            $AWSProfile = Read-Host "Enter your AWS profile name [default]"
            if ([string]::IsNullOrWhiteSpace($AWSProfile)) { $AWSProfile = "default" }

            $AWSRegion = Read-Host "Enter your AWS region [us-west-2]"
            if ([string]::IsNullOrWhiteSpace($AWSRegion)) { $AWSRegion = "us-west-2" }

            Write-Host ""
            Write-ColorOutput "Do you need to authenticate with AWS SSO?" "Yellow"
            $RunSSO = Read-Host "Run 'aws sso login --profile $AWSProfile' now? (y/N)"

            if ($RunSSO -match '^[Yy]') {
                Write-Host ""
                Write-Info "Running AWS SSO login..."
                if (Get-Command aws -ErrorAction SilentlyContinue) {
                    & aws sso login --profile $AWSProfile
                } else {
                    Write-Error "AWS CLI not found"
                    Write-Info "Please install AWS CLI and run: aws sso login --profile $AWSProfile"
                }
            }

            Write-Host ""
            Write-Info "Bedrock inference profile configuration:"
            Write-Info "(Inference profiles use the 'us.' prefix for cross-region availability)"
            Write-Host ""
            Write-Host "Available Claude models on Bedrock:"
            Write-Host "  1) us.anthropic.claude-sonnet-4-5-20250929-v1:0  (Sonnet 4.5 - balanced)"
            Write-Host "  2) us.anthropic.claude-opus-4-5-20251101-v1:0   (Opus 4.5 - most capable)"
            Write-Host "  3) us.anthropic.claude-3-5-sonnet-20241022-v2:0 (Sonnet 3.5 v2)"
            Write-Host ""

            $ModelChoice = Read-Host "Enter Bedrock inference profile [1]"
            if ([string]::IsNullOrWhiteSpace($ModelChoice)) { $ModelChoice = "1" }

            $LoomModel = switch ($ModelChoice) {
                "1" { "us.anthropic.claude-sonnet-4-5-20250929-v1:0" }
                "2" { "us.anthropic.claude-opus-4-5-20251101-v1:0" }
                "3" { "us.anthropic.claude-3-5-sonnet-20241022-v2:0" }
                default { $ModelChoice }
            }

            Write-Host ""
            Write-Info "Configuring AWS Bedrock with profile..."

            & "$BinDir\looms.exe" config set llm.provider bedrock
            & "$BinDir\looms.exe" config set llm.bedrock_profile $AWSProfile
            & "$BinDir\looms.exe" config set llm.bedrock_region $AWSRegion
            & "$BinDir\looms.exe" config set llm.bedrock_model_id $LoomModel

            Write-Success "AWS Bedrock configured"
            Write-Success "  Profile: $AWSProfile"
            Write-Success "  Region: $AWSRegion"
            Write-Success "  Model: $LoomModel"
        }

        "2" {
            # AWS Bedrock with Access Keys
            $ConfigureLLM = "y"
            Write-Host ""
            Write-Info "Configuring AWS Bedrock with access keys (stored securely)..."
            Write-Host ""

            $AWSRegion = Read-Host "Enter your AWS region [us-west-2]"
            if ([string]::IsNullOrWhiteSpace($AWSRegion)) { $AWSRegion = "us-west-2" }

            Write-Host ""
            Write-Info "Bedrock inference profile configuration:"
            Write-Host "Available Claude models on Bedrock:"
            Write-Host "  1) us.anthropic.claude-sonnet-4-5-20250929-v1:0  (Sonnet 4.5)"
            Write-Host "  2) us.anthropic.claude-opus-4-5-20251101-v1:0   (Opus 4.5)"
            Write-Host "  3) us.anthropic.claude-3-5-sonnet-20241022-v2:0 (Sonnet 3.5 v2)"
            Write-Host ""

            $ModelChoice = Read-Host "Enter Bedrock inference profile [1]"
            if ([string]::IsNullOrWhiteSpace($ModelChoice)) { $ModelChoice = "1" }

            $LoomModel = switch ($ModelChoice) {
                "1" { "us.anthropic.claude-sonnet-4-5-20250929-v1:0" }
                "2" { "us.anthropic.claude-opus-4-5-20251101-v1:0" }
                "3" { "us.anthropic.claude-3-5-sonnet-20241022-v2:0" }
                default { $ModelChoice }
            }

            Write-Host ""
            Write-Info "Setting AWS credentials..."

            & "$BinDir\looms.exe" config set llm.provider bedrock
            & "$BinDir\looms.exe" config set llm.bedrock_region $AWSRegion
            & "$BinDir\looms.exe" config set llm.bedrock_model_id $LoomModel
            & "$BinDir\looms.exe" config set-key bedrock_access_key_id
            & "$BinDir\looms.exe" config set-key bedrock_secret_access_key

            Write-Success "AWS Bedrock configured with access keys"
            Write-Success "  Region: $AWSRegion"
            Write-Success "  Model: $LoomModel"
        }

        "3" {
            # Anthropic
            $ConfigureLLM = "y"
            Write-Host ""
            Write-Info "Configuring Anthropic API..."
            Write-Host ""

            & "$BinDir\looms.exe" config set llm.provider anthropic
            & "$BinDir\looms.exe" config set-key anthropic_api_key

            Write-Success "Anthropic configured"
        }

        "4" {
            # OpenAI
            $ConfigureLLM = "y"
            Write-Host ""
            Write-Info "Configuring OpenAI API..."
            Write-Host ""

            & "$BinDir\looms.exe" config set llm.provider openai
            & "$BinDir\looms.exe" config set-key openai_api_key

            Write-Success "OpenAI configured"
        }

        "5" {
            # Azure OpenAI
            $ConfigureLLM = "y"
            Write-Host ""
            Write-Info "Configuring Azure OpenAI..."
            Write-Host ""

            $AzureEndpoint = Read-Host "Enter your Azure OpenAI endpoint (e.g., https://myresource.openai.azure.com)"
            $AzureDeployment = Read-Host "Enter your Azure OpenAI deployment ID (e.g., gpt-4o-deployment)"

            Write-Host ""
            Write-Info "Azure OpenAI supports two authentication methods:"
            Write-Host "  1) API Key (from Azure Portal)"
            Write-Host "  2) Microsoft Entra ID (OAuth2 token)"
            Write-Host ""

            $AuthChoice = Read-Host "Choose authentication method [1]"
            if ([string]::IsNullOrWhiteSpace($AuthChoice)) { $AuthChoice = "1" }

            & "$BinDir\looms.exe" config set llm.provider azure-openai
            & "$BinDir\looms.exe" config set llm.azure_openai_endpoint $AzureEndpoint
            & "$BinDir\looms.exe" config set llm.azure_openai_deployment_id $AzureDeployment

            if ($AuthChoice -eq "1") {
                & "$BinDir\looms.exe" config set-key azure_openai_api_key
            } else {
                Write-Host ""
                Write-ColorOutput "Do you need to authenticate with Azure CLI?" "Yellow"
                $RunAZ = Read-Host "Run 'az login' now? (y/N)"

                if ($RunAZ -match '^[Yy]') {
                    if (Get-Command az -ErrorAction SilentlyContinue) {
                        & az login
                    } else {
                        Write-Error "Azure CLI not found"
                        Write-Info "Install: https://docs.microsoft.com/en-us/cli/azure/install-azure-cli"
                    }
                }

                & "$BinDir\looms.exe" config set-key azure_openai_entra_token
            }

            Write-Success "Azure OpenAI configured"
        }

        "6" {
            # Mistral
            $ConfigureLLM = "y"
            Write-Host ""
            Write-Info "Configuring Mistral AI..."
            Write-Host ""

            & "$BinDir\looms.exe" config set llm.provider mistral
            & "$BinDir\looms.exe" config set-key mistral_api_key

            Write-Success "Mistral AI configured"
        }

        "7" {
            # Gemini
            $ConfigureLLM = "y"
            Write-Host ""
            Write-Info "Configuring Google Gemini..."
            Write-Host ""

            & "$BinDir\looms.exe" config set llm.provider gemini
            & "$BinDir\looms.exe" config set-key gemini_api_key

            Write-Success "Google Gemini configured"
        }

        "8" {
            # HuggingFace
            $ConfigureLLM = "y"
            Write-Host ""
            Write-Info "Configuring HuggingFace..."
            Write-Host ""

            & "$BinDir\looms.exe" config set llm.provider huggingface
            & "$BinDir\looms.exe" config set-key huggingface_token

            Write-Success "HuggingFace configured"
        }

        "9" {
            # Ollama
            $ConfigureLLM = "y"
            Write-Host ""
            Write-Info "Configuring Ollama (local/offline inference)..."
            Write-Host ""

            $OllamaEndpoint = Read-Host "Enter Ollama endpoint [http://localhost:11434]"
            if ([string]::IsNullOrWhiteSpace($OllamaEndpoint)) { $OllamaEndpoint = "http://localhost:11434" }

            Write-Host ""
            Write-Info "Ollama model configuration:"
            Write-Host ""
            Write-ColorOutput "⚠ IMPORTANT: Your model MUST support tool/function calling!" "Yellow"
            Write-ColorOutput "  Models without tool calling will perform poorly with Loom." "Yellow"
            Write-Host ""
            Write-Host "Recommended models with tool calling support:"
            Write-Host "  • llama3.1 (8B, 70B, 405B) - Good tool calling"
            Write-Host "  • llama3.2 (1B, 3B) - Basic tool calling"
            Write-Host "  • mistral - Decent tool calling"
            Write-Host "  • qwen2.5 - Good tool calling"
            Write-Host ""

            $OllamaModel = Read-Host "Enter Ollama model [llama3.1:8b]"
            if ([string]::IsNullOrWhiteSpace($OllamaModel)) { $OllamaModel = "llama3.1:8b" }

            Write-Host ""
            Write-Info "Configuring Ollama..."

            & "$BinDir\looms.exe" config set llm.provider ollama
            & "$BinDir\looms.exe" config set llm.ollama_endpoint $OllamaEndpoint
            & "$BinDir\looms.exe" config set llm.ollama_model $OllamaModel

            Write-Success "Ollama configured"
            Write-Success "  Endpoint: $OllamaEndpoint"
            Write-Success "  Model: $OllamaModel"
            Write-Host ""
            Write-Warning "Note: Make sure Ollama is running before starting Loom:"
            Write-Info "  ollama serve"
            Write-Info "  ollama pull $OllamaModel"
        }

        default {
            Write-Host ""
            Write-Warning "Skipping LLM configuration - you'll need to configure a provider manually"
        }
    }
} else {
    Write-Info "Skipping LLM provider configuration (CI mode)"
    Write-Warning "You'll need to configure a provider manually for actual use"
}

Write-Host ""

# ========================================
# Step 8: Configure Web Search API Keys (optional)
# ========================================
Write-Step "[8/8] Configuring Web Search API Keys (optional)..."
Write-Host ""

if (-not $SkipCredentials) {
    Write-ColorOutput "Configure web search API keys now?" "Yellow"
    Write-Info "This allows agents to search the web for current information."
    Write-Host ""
    Write-Host "Available web search providers:"
    Write-Host "  • Tavily (AI-optimized, 1000 searches/month FREE) - https://tavily.com/"
    Write-Host "  • Brave Search (excellent results, 2000 searches/month FREE) - https://brave.com/search/api/"
    Write-Host ""

    $ConfigureWebSearch = Read-Host "Configure web search API keys? (Y/n)"
    if ([string]::IsNullOrWhiteSpace($ConfigureWebSearch)) { $ConfigureWebSearch = "y" }

    if ($ConfigureWebSearch -match '^[Yy]') {
        Write-Host ""
        Write-Host "Which web search providers would you like to configure?"
        Write-Host "  1) Tavily (AI-optimized results, 1000/month free)"
        Write-Host "  2) Brave Search (excellent results, 2000/month free)"
        Write-Host "  3) Both Tavily and Brave"
        Write-Host "  4) Skip for now"
        Write-Host ""

        $SearchChoice = Read-Host "Enter choice [1]"
        if ([string]::IsNullOrWhiteSpace($SearchChoice)) { $SearchChoice = "1" }

        if ($SearchChoice -eq "1" -or $SearchChoice -eq "3") {
            Write-Host ""
            Write-Info "Configuring Tavily API key..."
            Write-Info "Get your FREE API key from: https://tavily.com/"
            & "$BinDir\looms.exe" config set-key tavily_api_key
            Write-Success "Tavily API key configured"
        }

        if ($SearchChoice -eq "2" -or $SearchChoice -eq "3") {
            Write-Host ""
            Write-Info "Configuring Brave Search API key..."
            Write-Info "Get your FREE API key from: https://brave.com/search/api/"
            & "$BinDir\looms.exe" config set-key brave_search_api_key
            Write-Success "Brave Search API key configured"
        }

        if ($SearchChoice -eq "4") {
            Write-Warning "Skipping web search API key configuration"
            Write-Info "You can configure later using:"
            Write-Info "  looms config set-key tavily_api_key"
            Write-Info "  looms config set-key brave_search_api_key"
        }
    } else {
        Write-Warning "Skipping web search API key configuration - you can configure later"
        Write-Info "Configure later using:"
        Write-Info "  looms config set-key tavily_api_key"
        Write-Info "  looms config set-key brave_search_api_key"
    }
} else {
    Write-Info "Skipping web search API key configuration (CI mode)"
    Write-Warning "You'll need to configure web search API keys manually"
}

Write-Host ""

# ========================================
# Installation complete
# ========================================
Write-ColorOutput "╔════════════════════════════════════════════════════════════╗" "Blue"
Write-ColorOutput "║  Installation Complete!                                    ║" "Blue"
Write-ColorOutput "╚════════════════════════════════════════════════════════════╝" "Blue"
Write-Host ""

Write-Success "Loom binaries installed to $BinDir"
if (Test-Path "$DataDir\patterns") {
    $PatternCount = (Get-ChildItem -Path "$DataDir\patterns" -Filter "*.yaml" -Recurse).Count
    Write-Success "Patterns installed to $DataDir\patterns ($PatternCount patterns)"
}
Write-Success "Configuration file: $DataDir\looms.yaml"
Write-Success "Environment variables set:"
Write-Info "  LOOM_DATA_DIR=$DataDir"
Write-Info "  LOOM_BIN_DIR=$BinDir"

if ($ConfigureLLM -eq "y") {
    Write-Success "LLM provider configured"
} else {
    Write-Warning "LLM provider not configured"
}

Write-Host ""
Write-ColorOutput "╔════════════════════════════════════════════════════════════╗" "Blue"
Write-ColorOutput "║  Quick Start                                               ║" "Blue"
Write-ColorOutput "╚════════════════════════════════════════════════════════════╝" "Blue"
Write-Host ""

$StepNum = 1

# If Ollama, show start instructions
if ((Get-Variable -Name "LLMChoice" -ErrorAction SilentlyContinue) -and $LLMChoice -eq "9" -and $ConfigureLLM -eq "y") {
    Write-ColorOutput "$StepNum. Start Ollama (required for local inference):" "Cyan"
    Write-Host ""
    Write-Info "   In a terminal window:"
    Write-Info "   ollama serve"
    Write-Host ""
    Write-Info "   Make sure your models are pulled:"
    if (Get-Variable -Name "OllamaModel" -ErrorAction SilentlyContinue) {
        Write-Info "   ollama pull $OllamaModel"
    } else {
        Write-Info "   ollama pull llama3.1:8b"
    }
    Write-Host ""
    $StepNum++
}

# If LLM not configured
if ($ConfigureLLM -ne "y") {
    Write-ColorOutput "$StepNum. Configure an LLM provider:" "Cyan"
    Write-Host ""
    Write-Info "   looms config set llm.provider <provider>"
    Write-Host ""
    Write-Info "   Available providers: anthropic, bedrock, openai, azure-openai,"
    Write-Info "   mistral, gemini, huggingface, ollama"
    Write-Host ""
    Write-Info "   See: https://teradata-labs.github.io/loom/en/docs/guides/llm-providers/"
    Write-Host ""
    $StepNum++
}

Write-ColorOutput "$StepNum. Start the Loom server:" "Cyan"
Write-Info "   looms serve"
Write-Host ""
$StepNum++

Write-ColorOutput "$StepNum. Create your first agent (in another terminal):" "Cyan"
Write-Info "   loom --thread weaver"
Write-Host ""
Write-Info "   Then describe what you need:"
Write-ColorOutput '   "Create a code review assistant that checks for security issues"' "Yellow"
Write-Host ""
$StepNum++

Write-ColorOutput "$StepNum. Connect to your agent:" "Cyan"
Write-Info "   loom --thread <thread-id>"
Write-Host ""

Write-ColorOutput "╔════════════════════════════════════════════════════════════╗" "Blue"
Write-ColorOutput "║  Additional Resources                                      ║" "Blue"
Write-ColorOutput "╚════════════════════════════════════════════════════════════╝" "Blue"
Write-Host ""

Write-Success "Documentation: https://teradata-labs.github.io/loom/"
Write-Success "GitHub: https://github.com/teradata-labs/loom"
Write-Success "Issues: https://github.com/teradata-labs/loom/issues"
Write-Host ""
Write-ColorOutput "Optional integrations:" "Yellow"
Write-Info "  • Hawk - Observability platform: https://github.com/teradata-labs/hawk"
Write-Info "  • Promptio - Prompt management: https://github.com/teradata-labs/promptio"
Write-Host ""
Write-Success "For more information, see the README.md"
Write-Host ""
Write-Warning "Note: You may need to restart PowerShell for PATH and environment variable changes to take effect"
Write-Host ""
