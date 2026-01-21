#!/usr/bin/env bash
set -e

# Loom Quickstart Installation Script
# Quick setup for Loom agent framework with LLM provider and web search

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║  Loom Quickstart Installation                             ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Get the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# CI Mode detection
if [ "${CI_MODE}" = "true" ] || [ "${GITHUB_ACTIONS}" = "true" ]; then
    echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║  Running in CI mode - skipping interactive prompts        ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    CI_MODE=true
    SKIP_CREDENTIALS=${SKIP_CREDENTIALS:-true}
else
    CI_MODE=false
    SKIP_CREDENTIALS=false
fi

# ========================================
# Step 0: Detect OS and check package managers
# ========================================
echo -e "${GREEN}[1/7] Checking system requirements...${NC}"
echo ""

OS="$(uname -s)"
case "$OS" in
    Darwin*)
        DETECTED_OS="macOS"
        PKG_MANAGER="brew"
        ;;
    Linux*)
        DETECTED_OS="Linux"
        PKG_MANAGER="cargo"
        ;;
    MINGW*|MSYS*|CYGWIN*)
        echo -e "${BLUE}Windows detected - redirecting to PowerShell installer...${NC}"
        echo ""

        # Check if quickstart.ps1 exists
        if [ ! -f "$SCRIPT_DIR/quickstart.ps1" ]; then
            echo -e "${RED}Error: quickstart.ps1 not found in $SCRIPT_DIR${NC}"
            exit 1
        fi

        # Run PowerShell script
        echo -e "${BLUE}Running: powershell.exe -ExecutionPolicy Bypass -File quickstart.ps1${NC}"
        echo ""

        # Try powershell.exe (Windows 10+) or pwsh.exe (PowerShell 7+)
        if command -v pwsh.exe &> /dev/null; then
            pwsh.exe -ExecutionPolicy Bypass -File "$SCRIPT_DIR/quickstart.ps1"
        elif command -v powershell.exe &> /dev/null; then
            powershell.exe -ExecutionPolicy Bypass -File "$SCRIPT_DIR/quickstart.ps1"
        else
            echo -e "${RED}Error: PowerShell not found${NC}"
            echo "Please run quickstart.ps1 directly in PowerShell:"
            echo "  powershell.exe -ExecutionPolicy Bypass -File quickstart.ps1"
            exit 1
        fi

        exit $?
        ;;
    *)
        echo -e "${RED}Error: Unsupported operating system: $OS${NC}"
        exit 1
        ;;
esac

echo -e "${BLUE}Detected OS: $DETECTED_OS${NC}"
echo ""

# Check for package manager
if [ "$DETECTED_OS" = "macOS" ]; then
    if ! command -v brew &> /dev/null; then
        echo -e "${RED}Error: Homebrew not found${NC}"
        echo "Please install Homebrew first: https://brew.sh"
        echo ""
        echo "Install command:"
        echo '  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'
        exit 1
    fi
    echo -e "${GREEN}✓ Found Homebrew${NC}"
elif [ "$DETECTED_OS" = "Linux" ]; then
    if ! command -v cargo &> /dev/null; then
        echo -e "${RED}Error: cargo (Rust) not found${NC}"
        echo "Please install Rust first: https://rustup.rs"
        echo ""
        echo "Install command:"
        echo "  curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh"
        exit 1
    fi
    echo -e "${GREEN}✓ Found cargo${NC}"
fi
echo ""

# ========================================
# Step 1: Prompt for installation directories
# ========================================
echo -e "${GREEN}[2/9] Configuring installation directories...${NC}"
echo ""

if [ "$CI_MODE" = false ]; then
    echo -e "${YELLOW}Where would you like to install Loom binaries?${NC}"
    echo -e "${BLUE}  Default: ~/.local/bin${NC}"
    echo ""
    read -p "Enter binary directory (press Enter for default): " bin_dir_input
    if [ -z "$bin_dir_input" ]; then
        BIN_DIR="$HOME/.local/bin"
    else
        BIN_DIR="$bin_dir_input"
    fi

    echo ""
    echo -e "${YELLOW}Where would you like to store Loom data (patterns, configs, databases)?${NC}"
    echo -e "${BLUE}  Default: ~/.loom${NC}"
    echo ""
    read -p "Enter data directory (press Enter for default): " data_dir_input
    if [ -z "$data_dir_input" ]; then
        DATA_DIR="$HOME/.loom"
    else
        DATA_DIR="$data_dir_input"
    fi
else
    BIN_DIR="$HOME/.local/bin"
    DATA_DIR="$HOME/.loom"
fi

echo ""
echo -e "${GREEN}✓ Binary directory: $BIN_DIR${NC}"
echo -e "${GREEN}✓ Data directory: $DATA_DIR${NC}"
echo ""

# Create directories
mkdir -p "$BIN_DIR"
mkdir -p "$DATA_DIR"

# Set environment variables (will be added to shell rc file later)
export LOOM_BIN_DIR="$BIN_DIR"
export LOOM_DATA_DIR="$DATA_DIR"

echo ""

# ========================================
# Step 2: Check and install prerequisites
# ========================================
echo -e "${GREEN}[3/9] Installing prerequisites (go, just, buf)...${NC}"
echo ""

NEEDS_INSTALL=false

# Check Go
if ! command -v go &> /dev/null; then
    echo -e "${YELLOW}⚠ Go not found - will install${NC}"
    NEEDS_INSTALL=true
    INSTALL_GO=true
else
    GO_VERSION=$(go version | awk '{print $3}')
    echo -e "${GREEN}✓ Go installed: $GO_VERSION${NC}"
    INSTALL_GO=false
fi

# Check Just
if ! command -v just &> /dev/null; then
    echo -e "${YELLOW}⚠ Just not found - will install${NC}"
    NEEDS_INSTALL=true
    INSTALL_JUST=true
else
    JUST_VERSION=$(just --version)
    echo -e "${GREEN}✓ Just installed: $JUST_VERSION${NC}"
    INSTALL_JUST=false
fi

# Check Buf
if ! command -v buf &> /dev/null; then
    echo -e "${YELLOW}⚠ Buf not found - will install${NC}"
    NEEDS_INSTALL=true
    INSTALL_BUF=true
else
    BUF_VERSION=$(buf --version)
    echo -e "${GREEN}✓ Buf installed: $BUF_VERSION${NC}"
    INSTALL_BUF=false
fi

echo ""

# Install missing prerequisites
if [ "$NEEDS_INSTALL" = true ]; then
    echo -e "${BLUE}Installing missing prerequisites...${NC}"
    echo ""

    if [ "$DETECTED_OS" = "macOS" ]; then
        [ "$INSTALL_GO" = true ] && echo "Installing Go..." && brew install go
        [ "$INSTALL_JUST" = true ] && echo "Installing Just..." && brew install just
        [ "$INSTALL_BUF" = true ] && echo "Installing Buf..." && brew install bufbuild/buf/buf
    elif [ "$DETECTED_OS" = "Linux" ]; then
        [ "$INSTALL_GO" = true ] && echo -e "${RED}Error: Go not found. Please install manually:${NC}" && echo "  sudo apt-get install golang-go  # Debian/Ubuntu" && echo "  sudo dnf install golang  # Fedora/RHEL" && exit 1
        [ "$INSTALL_JUST" = true ] && echo "Installing Just..." && cargo install just

        # Install Buf using direct binary download
        if [ "$INSTALL_BUF" = true ]; then
            echo "Installing Buf..."
            BUF_VERSION="1.58.0"
            ARCH="$(uname -m)"

            case "$ARCH" in
                x86_64|amd64)
                    BUF_ARCH="x86_64"
                    ;;
                aarch64|arm64)
                    BUF_ARCH="aarch64"
                    ;;
                *)
                    echo -e "${RED}Error: Unsupported architecture for buf: $ARCH${NC}"
                    exit 1
                    ;;
            esac

            echo "  Downloading buf v${BUF_VERSION} for Linux-${BUF_ARCH}..."
            curl -sSL "https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/buf-Linux-${BUF_ARCH}" -o /tmp/buf || {
                echo -e "${RED}Error: Failed to download buf${NC}"
                exit 1
            }

            # Install to ~/.local/bin (no sudo needed)
            mkdir -p "$HOME/.local/bin"
            mv /tmp/buf "$HOME/.local/bin/buf"
            chmod +x "$HOME/.local/bin/buf"

            echo -e "${GREEN}✓ Buf installed to ~/.local/bin/buf${NC}"
        fi
    fi

    echo ""
    echo -e "${GREEN}✓ Prerequisites installed${NC}"
    echo ""
fi

# ========================================
# Step 3: Build Loom
# ========================================
echo -e "${GREEN}[4/9] Building Loom...${NC}"
echo ""

# Build loom (with FTS5 support for SQLite full-text search)
just build

if [ ! -f "$SCRIPT_DIR/bin/looms" ]; then
    echo -e "${RED}Error: Build failed - bin/looms not found${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Loom built successfully${NC}"
echo ""

# Install patterns
echo "Installing patterns to $DATA_DIR/patterns..."
mkdir -p "$DATA_DIR/patterns"
rsync -av --delete "$SCRIPT_DIR/patterns/" "$DATA_DIR/patterns/"
PATTERN_COUNT=$(find "$DATA_DIR/patterns" -name '*.yaml' | wc -l | tr -d ' ')
echo -e "${GREEN}✓ Installed $PATTERN_COUNT pattern files to $DATA_DIR/patterns${NC}"
echo ""

# Install documentation
echo "Installing documentation to $DATA_DIR/documentation..."
mkdir -p "$DATA_DIR/documentation"
if [ -d "$SCRIPT_DIR/docs" ]; then
    rsync -av --delete "$SCRIPT_DIR/docs/" "$DATA_DIR/documentation/"
    DOC_COUNT=$(find "$DATA_DIR/documentation" -name '*.md' | wc -l | tr -d ' ')
    echo -e "${GREEN}✓ Installed $DOC_COUNT documentation files to $DATA_DIR/documentation${NC}"
else
    echo -e "${YELLOW}⚠ Documentation directory not found, skipping${NC}"
fi
echo ""

# Install binaries
echo "Installing binaries to $BIN_DIR..."

# Remove existing files/symlinks if present
rm -f "$BIN_DIR/looms"
rm -f "$BIN_DIR/loom"

# Copy binaries (use symlinks on macOS to avoid binary killing, copy on Linux)
if [ "$DETECTED_OS" = "macOS" ]; then
    ln -s "$SCRIPT_DIR/bin/looms" "$BIN_DIR/looms"
    ln -s "$SCRIPT_DIR/bin/loom" "$BIN_DIR/loom"
else
    cp "$SCRIPT_DIR/bin/looms" "$BIN_DIR/looms"
    cp "$SCRIPT_DIR/bin/loom" "$BIN_DIR/loom"
fi

# Make sure they're executable
chmod +x "$BIN_DIR/looms"
chmod +x "$BIN_DIR/loom"

echo -e "${GREEN}✓ Binaries installed to $BIN_DIR${NC}"

# Check if BIN_DIR is in PATH
if [[ ":$PATH:" != *":$BIN_DIR:"* ]]; then
    echo -e "${YELLOW}⚠ $BIN_DIR is not in your PATH${NC}"
    echo -e "${YELLOW}  Add this to your ~/.bashrc or ~/.zshrc:${NC}"
    echo -e "${BLUE}  export PATH=\"$BIN_DIR:\$PATH\"${NC}"
    echo ""
else
    echo -e "${GREEN}✓ $BIN_DIR is already in your PATH${NC}"
fi

echo ""

# ========================================
# Step 4: Configure environment variables
# ========================================
echo -e "${GREEN}[5/9] Configuring environment variables...${NC}"
echo ""

# Detect shell
SHELL_RC=""
if [ -n "$BASH_VERSION" ]; then
    SHELL_RC="$HOME/.bashrc"
elif [ -n "$ZSH_VERSION" ]; then
    SHELL_RC="$HOME/.zshrc"
fi

if [ -n "$SHELL_RC" ] && [ "$CI_MODE" = false ]; then
    echo -e "${YELLOW}Add environment variables to $SHELL_RC?${NC}"
    read -p "This will append export statements to your shell config (Y/n): " add_to_rc
    add_to_rc=${add_to_rc:-y}

    if [[ "$add_to_rc" =~ ^[Yy]$ ]]; then
        # Check if already added
        if ! grep -q "LOOM_DATA_DIR" "$SHELL_RC" 2>/dev/null; then
            echo "" >> "$SHELL_RC"
            echo "# Loom environment variables" >> "$SHELL_RC"
            echo "export LOOM_DATA_DIR=\"$DATA_DIR\"" >> "$SHELL_RC"
            echo "export LOOM_BIN_DIR=\"$BIN_DIR\"" >> "$SHELL_RC"
            echo "export PATH=\"$BIN_DIR:\$PATH\"" >> "$SHELL_RC"
            echo -e "${GREEN}✓ Added environment variables to $SHELL_RC${NC}"
            echo -e "${YELLOW}  Run 'source $SHELL_RC' to reload, or restart your terminal${NC}"
        else
            echo -e "${GREEN}✓ Environment variables already in $SHELL_RC${NC}"
        fi
    else
        echo -e "${YELLOW}⚠ Skipped shell config - add manually:${NC}"
        echo -e "${BLUE}  export LOOM_DATA_DIR=\"$DATA_DIR\"${NC}"
        echo -e "${BLUE}  export LOOM_BIN_DIR=\"$BIN_DIR\"${NC}"
        echo -e "${BLUE}  export PATH=\"$BIN_DIR:\$PATH\"${NC}"
    fi
else
    echo -e "${YELLOW}⚠ Could not detect shell config file${NC}"
    echo -e "${YELLOW}  Add manually to your shell config:${NC}"
    echo -e "${BLUE}  export LOOM_DATA_DIR=\"$DATA_DIR\"${NC}"
    echo -e "${BLUE}  export LOOM_BIN_DIR=\"$BIN_DIR\"${NC}"
    echo -e "${BLUE}  export PATH=\"$BIN_DIR:\$PATH\"${NC}"
fi

echo ""

# ========================================
# Step 5: Initialize Loom configuration
# ========================================
echo -e "${GREEN}[6/9] Initializing Loom configuration...${NC}"
echo ""

# Create minimal config file
echo "Creating $DATA_DIR/looms.yaml..."

cat > "$DATA_DIR/looms.yaml" << EOF
# Loom Server Configuration
server:
  host: "0.0.0.0"
  port: 60051

# Database stored in Loom data directory
database:
  path: "$DATA_DIR/loom.db"

# Communication system (shared memory, message queue)
communication:
  store:
    backend: sqlite
    path: "$DATA_DIR/loom.db"

# Observability (optional - requires Hawk)
observability:
  enabled: false

# MCP servers (add your own)
mcp:
  servers: {}

# No pre-configured agents - use the weaver to create threads on demand
agents:
  agents: {}
EOF

echo -e "${GREEN}✓ Loom configuration initialized${NC}"
echo -e "${BLUE}  Config file: $DATA_DIR/looms.yaml${NC}"
echo ""

# ========================================
# Step 6: Configure LLM provider (interactive)
# ========================================
echo -e "${GREEN}[7/9] Configuring LLM provider...${NC}"
echo ""

if [ "$SKIP_CREDENTIALS" = "true" ]; then
    echo -e "${BLUE}Skipping LLM provider configuration (CI mode)${NC}"
    echo -e "${YELLOW}⚠ You'll need to configure a provider manually for actual use${NC}"
    echo ""
else
echo -e "${YELLOW}Which LLM provider do you want to configure?${NC}"
echo ""
echo "  1) AWS Bedrock (with SSO/Profile) - For users with AWS profiles configured"
echo "  2) AWS Bedrock (with Access Keys) - For users with AWS access keys"
echo "  3) Anthropic API - Direct Anthropic access"
echo "  4) OpenAI - GPT-4 models, o1 reasoning models"
echo "  5) Azure OpenAI - Enterprise Microsoft integration"
echo "  6) Mistral AI - Open & commercial models"
echo "  7) Google Gemini - Google's latest AI models"
echo "  8) HuggingFace - 1M+ open source models"
echo "  9) Ollama - Local/offline models (requires tool calling support)"
echo " 10) Skip for now - Configure later manually"
echo ""
read -p "Enter choice [1]: " llm_choice
llm_choice=${llm_choice:-1}

# Initialize configuration tracking variables
configure_llm="n"

if [ "$llm_choice" = "1" ]; then
    configure_llm="y"
    echo ""
    # Prompt for AWS profile
    read -p "Enter your AWS profile name [default]: " aws_profile
    aws_profile=${aws_profile:-default}

    # Prompt for AWS region
    read -p "Enter your AWS region [us-west-2]: " aws_region
    aws_region=${aws_region:-us-west-2}

    # Check if user needs to run AWS SSO login
    echo ""
    echo -e "${YELLOW}Do you need to authenticate with AWS SSO?${NC}"
    read -p "Run 'aws sso login --profile $aws_profile' now? (y/n) [n]: " run_sso_login
    run_sso_login=${run_sso_login:-n}

    if [[ "$run_sso_login" =~ ^[Yy]$ ]]; then
        echo ""
        echo "Running AWS SSO login..."
        if command -v aws &> /dev/null; then
            aws sso login --profile "$aws_profile" || {
                echo -e "${RED}Warning: AWS SSO login failed${NC}"
                echo "You may need to run this manually before using Loom"
            }
        else
            echo -e "${RED}Error: AWS CLI not found${NC}"
            echo "Please install AWS CLI and run: aws sso login --profile $aws_profile"
        fi
    fi

    # Prompt for model inference profile
    echo ""
    echo "Bedrock inference profile configuration:"
    echo "(Inference profiles use the 'us.' prefix for cross-region availability)"
    echo ""
    echo "Available Claude models on Bedrock:"
    echo "  1) us.anthropic.claude-sonnet-4-5-20250929-v1:0  (Sonnet 4.5 - balanced)"
    echo "  2) us.anthropic.claude-opus-4-5-20251101-v1:0   (Opus 4.5 - most capable)"
    echo "  3) us.anthropic.claude-3-5-sonnet-20241022-v2:0 (Sonnet 3.5 v2)"
    echo ""
    read -p "Enter Bedrock inference profile [1]: " model_choice
    model_choice=${model_choice:-1}
    case "$model_choice" in
        1) loom_model="us.anthropic.claude-sonnet-4-5-20250929-v1:0" ;;
        2) loom_model="us.anthropic.claude-opus-4-5-20251101-v1:0" ;;
        3) loom_model="us.anthropic.claude-3-5-sonnet-20241022-v2:0" ;;
        *) loom_model="$model_choice" ;;  # Allow custom input
    esac

    echo ""
    echo "Configuring AWS Bedrock with profile..."

    # Configure Bedrock
    "$BIN_DIR/looms" config set llm.provider bedrock
    "$BIN_DIR/looms" config set llm.bedrock_profile "$aws_profile"
    "$BIN_DIR/looms" config set llm.bedrock_region "$aws_region"
    "$BIN_DIR/looms" config set llm.bedrock_model_id "$loom_model"

    echo -e "${GREEN}✓ AWS Bedrock configured${NC}"
    echo -e "${GREEN}  Profile: $aws_profile${NC}"
    echo -e "${GREEN}  Region: $aws_region${NC}"
    echo -e "${GREEN}  Model: $loom_model${NC}"

elif [ "$llm_choice" = "2" ]; then
    configure_llm="y"
    echo ""
    echo "Configuring AWS Bedrock with access keys (stored securely in keyring)..."
    echo ""

    # Prompt for AWS region
    read -p "Enter your AWS region [us-west-2]: " aws_region
    aws_region=${aws_region:-us-west-2}

    # Prompt for model inference profile
    echo ""
    echo "Bedrock inference profile configuration:"
    echo "(Inference profiles use the 'us.' prefix for cross-region availability)"
    echo ""
    echo "Available Claude models on Bedrock:"
    echo "  1) us.anthropic.claude-sonnet-4-5-20250929-v1:0  (Sonnet 4.5 - balanced)"
    echo "  2) us.anthropic.claude-opus-4-5-20251101-v1:0   (Opus 4.5 - most capable)"
    echo "  3) us.anthropic.claude-3-5-sonnet-20241022-v2:0 (Sonnet 3.5 v2)"
    echo ""
    read -p "Enter Bedrock inference profile [1]: " model_choice
    model_choice=${model_choice:-1}
    case "$model_choice" in
        1) loom_model="us.anthropic.claude-sonnet-4-5-20250929-v1:0" ;;
        2) loom_model="us.anthropic.claude-opus-4-5-20251101-v1:0" ;;
        3) loom_model="us.anthropic.claude-3-5-sonnet-20241022-v2:0" ;;
        *) loom_model="$model_choice" ;;  # Allow custom input
    esac

    echo ""
    echo "Setting AWS credentials (securely stored in keyring)..."
    echo ""

    # Configure Bedrock
    "$BIN_DIR/looms" config set llm.provider bedrock
    "$BIN_DIR/looms" config set llm.bedrock_region "$aws_region"
    "$BIN_DIR/looms" config set llm.bedrock_model_id "$loom_model"

    # Set AWS keys via keyring
    "$BIN_DIR/looms" config set-key bedrock_access_key_id
    "$BIN_DIR/looms" config set-key bedrock_secret_access_key

    echo -e "${GREEN}✓ AWS Bedrock configured with access keys${NC}"
    echo -e "${GREEN}  Region: $aws_region${NC}"
    echo -e "${GREEN}  Model: $loom_model${NC}"
    echo -e "${GREEN}  Credentials: Stored securely in keyring${NC}"

elif [ "$llm_choice" = "3" ]; then
    configure_llm="y"
    echo ""
    echo "Configuring Anthropic API..."
    echo ""

    "$BIN_DIR/looms" config set llm.provider anthropic
    "$BIN_DIR/looms" config set-key anthropic_api_key

    echo -e "${GREEN}✓ Anthropic configured${NC}"

elif [ "$llm_choice" = "4" ]; then
    configure_llm="y"
    echo ""
    echo "Configuring OpenAI API..."
    echo ""

    "$BIN_DIR/looms" config set llm.provider openai
    "$BIN_DIR/looms" config set-key openai_api_key

    echo -e "${GREEN}✓ OpenAI configured${NC}"

elif [ "$llm_choice" = "5" ]; then
    configure_llm="y"
    echo ""
    echo "Configuring Azure OpenAI..."
    echo ""

    # Prompt for Azure OpenAI endpoint
    read -p "Enter your Azure OpenAI endpoint (e.g., https://myresource.openai.azure.com): " azure_endpoint

    # Prompt for deployment ID
    read -p "Enter your Azure OpenAI deployment ID (e.g., gpt-4o-deployment): " azure_deployment

    echo ""
    echo "Azure OpenAI supports two authentication methods:"
    echo "  1) API Key (from Azure Portal)"
    echo "  2) Microsoft Entra ID (OAuth2 token)"
    echo ""
    read -p "Choose authentication method [1]: " azure_auth_choice
    azure_auth_choice=${azure_auth_choice:-1}

    "$BIN_DIR/looms" config set llm.provider azure-openai
    "$BIN_DIR/looms" config set llm.azure_openai_endpoint "$azure_endpoint"
    "$BIN_DIR/looms" config set llm.azure_openai_deployment_id "$azure_deployment"

    if [ "$azure_auth_choice" = "1" ]; then
        "$BIN_DIR/looms" config set-key azure_openai_api_key
    else
        # Microsoft Entra ID authentication
        echo ""
        echo -e "${YELLOW}Do you need to authenticate with Azure CLI?${NC}"
        read -p "Run 'az login' now? (y/n) [n]: " run_az_login
        run_az_login=${run_az_login:-n}

        if [[ "$run_az_login" =~ ^[Yy]$ ]]; then
            echo ""
            echo "Running Azure CLI login..."
            if command -v az &> /dev/null; then
                az login || {
                    echo -e "${RED}Warning: Azure CLI login failed${NC}"
                    echo "You may need to run this manually before using Loom"
                }
            else
                echo -e "${RED}Error: Azure CLI not found${NC}"
                echo "Please install Azure CLI and run: az login"
                echo "Install: https://docs.microsoft.com/en-us/cli/azure/install-azure-cli"
            fi
        fi

        "$BIN_DIR/looms" config set-key azure_openai_entra_token
    fi

    echo -e "${GREEN}✓ Azure OpenAI configured${NC}"

elif [ "$llm_choice" = "6" ]; then
    configure_llm="y"
    echo ""
    echo "Configuring Mistral AI..."
    echo ""

    "$BIN_DIR/looms" config set llm.provider mistral
    "$BIN_DIR/looms" config set-key mistral_api_key

    echo -e "${GREEN}✓ Mistral AI configured${NC}"

elif [ "$llm_choice" = "7" ]; then
    configure_llm="y"
    echo ""
    echo "Configuring Google Gemini..."
    echo ""

    "$BIN_DIR/looms" config set llm.provider gemini
    "$BIN_DIR/looms" config set-key gemini_api_key

    echo -e "${GREEN}✓ Google Gemini configured${NC}"

elif [ "$llm_choice" = "8" ]; then
    configure_llm="y"
    echo ""
    echo "Configuring HuggingFace..."
    echo ""

    "$BIN_DIR/looms" config set llm.provider huggingface
    "$BIN_DIR/looms" config set-key huggingface_token

    echo -e "${GREEN}✓ HuggingFace configured${NC}"

elif [ "$llm_choice" = "9" ]; then
    configure_llm="y"
    echo ""
    echo "Configuring Ollama (local/offline inference)..."
    echo ""

    # Prompt for Ollama endpoint
    read -p "Enter Ollama endpoint [http://localhost:11434]: " ollama_endpoint
    ollama_endpoint=${ollama_endpoint:-http://localhost:11434}

    # Prompt for model
    echo ""
    echo "Ollama model configuration:"
    echo ""
    echo -e "${YELLOW}⚠ IMPORTANT: Your model MUST support tool/function calling!${NC}"
    echo -e "${YELLOW}  Models without tool calling will perform poorly with Loom.${NC}"
    echo ""
    echo "Recommended models with tool calling support:"
    echo "  • llama3.1 (8B, 70B, 405B) - Good tool calling"
    echo "  • llama3.2 (1B, 3B) - Basic tool calling"
    echo "  • mistral - Decent tool calling"
    echo "  • qwen2.5 - Good tool calling"
    echo ""
    read -p "Enter Ollama model [llama3.1:8b]: " ollama_model
    ollama_model=${ollama_model:-llama3.1:8b}

    echo ""
    echo "Configuring Ollama..."

    # Configure Ollama
    "$BIN_DIR/looms" config set llm.provider ollama
    "$BIN_DIR/looms" config set llm.ollama_endpoint "$ollama_endpoint"
    "$BIN_DIR/looms" config set llm.ollama_model "$ollama_model"

    echo -e "${GREEN}✓ Ollama configured${NC}"
    echo -e "${GREEN}  Endpoint: $ollama_endpoint${NC}"
    echo -e "${GREEN}  Model: $ollama_model${NC}"
    echo ""
    echo -e "${YELLOW}Note: Make sure Ollama is running before starting Loom:${NC}"
    echo -e "  ${BLUE}ollama serve${NC}"
    echo -e "  ${BLUE}ollama pull $ollama_model${NC}"

else
    echo ""
    echo -e "${YELLOW}⚠ Skipping LLM configuration - you'll need to configure a provider manually${NC}"
fi
fi  # End of SKIP_CREDENTIALS check for LLM configuration

echo ""

# ========================================
# Step 7: Configure Web Search API Keys (optional)
# ========================================
echo -e "${GREEN}[8/9] Configuring Web Search API Keys (optional)...${NC}"
echo ""

if [ "$SKIP_CREDENTIALS" = "true" ]; then
    echo -e "${BLUE}Skipping web search API key configuration (CI mode)${NC}"
    echo -e "${YELLOW}⚠ You'll need to configure web search API keys manually for web search capabilities${NC}"
    echo ""
else
echo -e "${YELLOW}Configure web search API keys now?${NC}"
echo "This allows agents to search the web for current information."
echo ""
echo "Available web search providers:"
echo "  • Tavily (AI-optimized, 1000 searches/month FREE) - https://tavily.com/"
echo "  • Brave Search (excellent results, 2000 searches/month FREE) - https://brave.com/search/api/"
echo ""
read -p "Configure web search API keys? (y/n) [y]: " configure_web_search
configure_web_search=${configure_web_search:-y}

if [[ "$configure_web_search" =~ ^[Yy]$ ]]; then
    echo ""
    echo "Which web search providers would you like to configure?"
    echo "  1) Tavily (AI-optimized results, 1000/month free)"
    echo "  2) Brave Search (excellent results, 2000/month free)"
    echo "  3) Both Tavily and Brave"
    echo "  4) Skip for now"
    echo ""
    read -p "Enter choice [1]: " search_provider_choice
    search_provider_choice=${search_provider_choice:-1}

    if [ "$search_provider_choice" = "1" ] || [ "$search_provider_choice" = "3" ]; then
        echo ""
        echo "Configuring Tavily API key..."
        echo "Get your FREE API key from: https://tavily.com/"
        "$BIN_DIR/looms" config set-key tavily_api_key
        echo -e "${GREEN}✓ Tavily API key configured${NC}"
    fi

    if [ "$search_provider_choice" = "2" ] || [ "$search_provider_choice" = "3" ]; then
        echo ""
        echo "Configuring Brave Search API key..."
        echo "Get your FREE API key from: https://brave.com/search/api/"
        "$BIN_DIR/looms" config set-key brave_search_api_key
        echo -e "${GREEN}✓ Brave Search API key configured${NC}"
    fi

    if [ "$search_provider_choice" = "4" ]; then
        echo -e "${YELLOW}⚠ Skipping web search API key configuration${NC}"
        echo "You can configure later using:"
        echo "  looms config set-key tavily_api_key"
        echo "  looms config set-key brave_search_api_key"
    fi
else
    echo -e "${YELLOW}⚠ Skipping web search API key configuration - you can configure later${NC}"
    echo "Configure later using:"
    echo "  looms config set-key tavily_api_key"
    echo "  looms config set-key brave_search_api_key"
fi
fi  # End of SKIP_CREDENTIALS check for web search configuration

echo ""

# ========================================
# Step 8: Verify installation
# ========================================
echo -e "${GREEN}[9/9] Verifying installation...${NC}"
echo ""

# Check all binaries are working
if command -v go &> /dev/null && command -v just &> /dev/null && command -v buf &> /dev/null; then
    echo -e "${GREEN}✓ All prerequisites verified${NC}"
else
    echo -e "${RED}⚠ Some prerequisites may not be available${NC}"
fi

if [ -f "$BIN_DIR/looms" ] && [ -f "$BIN_DIR/loom" ]; then
    echo -e "${GREEN}✓ Loom binaries verified${NC}"
else
    echo -e "${RED}⚠ Loom binaries not found${NC}"
fi

echo ""

# ========================================
# Installation complete
# ========================================
echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║  Installation Complete!                                    ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "${GREEN}✓ Loom binaries installed to $BIN_DIR${NC}"
echo -e "${GREEN}✓ Patterns installed to $DATA_DIR/patterns/ ($PATTERN_COUNT patterns)${NC}"
echo -e "${GREEN}✓ Configuration file: $DATA_DIR/looms.yaml${NC}"
echo -e "${GREEN}✓ Environment variables set:${NC}"
echo -e "${BLUE}  LOOM_DATA_DIR=$DATA_DIR${NC}"
echo -e "${BLUE}  LOOM_BIN_DIR=$BIN_DIR${NC}"

# Show LLM config status
if [[ "$configure_llm" =~ ^[Yy]$ ]]; then
    echo -e "${GREEN}✓ LLM provider configured${NC}"
else
    echo -e "${YELLOW}⚠ LLM provider not configured${NC}"
fi

echo ""
echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║  Quick Start                                               ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Show appropriate next steps
STEP_NUM=1

# If Ollama was configured, remind them to start it first
if [ "$llm_choice" = "9" ] && [[ "$configure_llm" =~ ^[Yy]$ ]]; then
    echo -e "$STEP_NUM. Start Ollama (required for local inference):"
    echo ""
    echo -e "   In a terminal window:"
    echo -e "   ${BLUE}ollama serve${NC}"
    echo ""
    echo -e "   Make sure your models are pulled:"
    echo -e "   ${BLUE}ollama pull $ollama_model${NC}"
    echo ""
    STEP_NUM=$((STEP_NUM + 1))
fi

# If LLM not configured, show config steps
if [[ ! "$configure_llm" =~ ^[Yy]$ ]]; then
    echo -e "$STEP_NUM. Configure an LLM provider:"
    echo ""
    echo -e "   ${BLUE}looms config set llm.provider <provider>${NC}"
    echo ""
    echo -e "   Available providers: anthropic, bedrock, openai, azure-openai,"
    echo -e "   mistral, gemini, huggingface, ollama"
    echo ""
    echo -e "   See: https://teradata-labs.github.io/loom/en/docs/guides/llm-providers/"
    echo ""
    STEP_NUM=$((STEP_NUM + 1))
fi

echo -e "$STEP_NUM. Start the Loom server:"
echo -e "   ${BLUE}looms serve${NC}"
echo ""
STEP_NUM=$((STEP_NUM + 1))

echo -e "$STEP_NUM. Create your first agent (in another terminal):"
echo -e "   ${BLUE}loom --thread weaver${NC}"
echo ""
echo -e "   Then describe what you need:"
echo -e "   ${YELLOW}\"Create a code review assistant that checks for security issues\"${NC}"
echo ""
STEP_NUM=$((STEP_NUM + 1))

echo -e "$STEP_NUM. Connect to your agent:"
echo -e "   ${BLUE}loom --thread <thread-id>${NC}"
echo ""

echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║  Additional Resources                                      ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "${GREEN}Documentation:${NC} https://teradata-labs.github.io/loom/"
echo -e "${GREEN}GitHub:${NC} https://github.com/teradata-labs/loom"
echo -e "${GREEN}Issues:${NC} https://github.com/teradata-labs/loom/issues"
echo ""
echo -e "${YELLOW}Optional integrations:${NC}"
echo -e "  • Hawk - Observability platform: https://github.com/teradata-labs/hawk"
echo -e "  • Promptio - Prompt management: https://github.com/teradata-labs/promptio"
echo ""
echo -e "${GREEN}For more information, see the README.md${NC}"
echo ""
