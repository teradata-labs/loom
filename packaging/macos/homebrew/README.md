# Homebrew Formulas for Loom

Homebrew formulas for installing Loom on macOS (and Linux via Homebrew on Linux).

## Installation (for users)

Once published to a Homebrew tap:

```bash
# Add the tap
brew tap teradata-labs/loom

# Install Loom server
brew install loom-server

# Install Loom TUI client
brew install loom

# Or install both
brew install loom loom-server
```

### Current Installation (Before Publishing)

Install directly from this repository:

```bash
# Install server
brew install https://raw.githubusercontent.com/teradata-labs/loom/main/packaging/macos/homebrew/loom-server.rb

# Install client
brew install https://raw.githubusercontent.com/teradata-labs/loom/main/packaging/macos/homebrew/loom.rb
```

## Updating for New Releases (for maintainers)

When releasing a new version:

### 1. Update Version

Edit both `loom.rb` and `loom-server.rb`:

```ruby
version "1.0.1"  # Update to new version
```

### 2. Update URLs

Update download URLs in both formulas:

```ruby
url "https://github.com/teradata-labs/loom/releases/download/v1.0.1/..."
```

### 3. Calculate SHA256 Hashes

For each binary:

```bash
# Download binaries
version="1.0.1"

# macOS ARM64
curl -LO "https://github.com/teradata-labs/loom/releases/download/v${version}/loom-darwin-arm64.tar.gz"
curl -LO "https://github.com/teradata-labs/loom/releases/download/v${version}/looms-darwin-arm64.tar.gz"

# macOS AMD64
curl -LO "https://github.com/teradata-labs/loom/releases/download/v${version}/loom-darwin-amd64.tar.gz"
curl -LO "https://github.com/teradata-labs/loom/releases/download/v${version}/looms-darwin-amd64.tar.gz"

# Linux AMD64
curl -LO "https://github.com/teradata-labs/loom/releases/download/v${version}/loom-linux-amd64.tar.gz"
curl -LO "https://github.com/teradata-labs/loom/releases/download/v${version}/looms-linux-amd64.tar.gz"

# Calculate SHA256 hashes
shasum -a 256 *.tar.gz
```

### 4. Update SHA256 in Formulas

Replace empty `sha256 ""` values with calculated hashes:

```ruby
sha256 "abc123..."  # Actual hash
```

## Testing Locally

```bash
# Install from local formula
brew install --build-from-source ./loom-server.rb
brew install --build-from-source ./loom.rb

# Test installation
looms --version
looms --help
loom --help

# Test server starts
looms serve &
sleep 3
pkill looms

# Uninstall
brew uninstall loom loom-server
```

## Creating a Homebrew Tap (for publishing)

### Option 1: Official Homebrew Core (Difficult)

Requirements:
- 30+ forks/watchers on GitHub
- Stable release history
- Notable user base

Submit to: https://github.com/Homebrew/homebrew-core

### Option 2: Teradata Labs Tap (Recommended)

Create `homebrew-loom` repository:

```bash
# Create new GitHub repository: teradata-labs/homebrew-loom
# Repository structure:
homebrew-loom/
├── README.md
├── loom.rb
└── loom-server.rb
```

Users install with:

```bash
brew tap teradata-labs/loom
brew install loom loom-server
```

#### Setup Steps:

1. **Create repository**: https://github.com/new
   - Name: `homebrew-loom`
   - Description: "Homebrew formulas for Loom AI agent framework"
   - Public

2. **Add formulas**:
   ```bash
   cd /path/to/homebrew-loom
   cp /path/to/loom/packaging/macos/homebrew/*.rb .
   git add *.rb
   git commit -m "Add Loom formulas"
   git push
   ```

3. **Test tap**:
   ```bash
   brew tap teradata-labs/loom
   brew install loom-server
   ```

4. **Announce to users**:
   ```bash
   brew tap teradata-labs/loom
   brew install loom loom-server
   ```

## Formula Features

### loom.rb (TUI Client)
- Installs `loom` binary to PATH
- Downloads and installs patterns to `$LOOM_DATA_DIR/patterns/`
- Cross-platform (macOS ARM64/AMD64, Linux AMD64)
- Provides helpful post-install instructions

### loom-server.rb (Server)
- Installs `looms` binary to PATH
- Downloads and installs patterns
- Creates default configuration file
- Includes Homebrew service integration:
  ```bash
  brew services start loom-server  # Run as background service
  brew services stop loom-server
  ```
- Cross-platform support

## Homebrew Services

The server formula includes service integration:

```bash
# Start server as background service
brew services start loom-server

# Stop service
brew services stop loom-server

# Restart service
brew services restart loom-server

# View logs
tail -f $(brew --prefix)/var/log/loom.log
```

## Formula Guidelines

Homebrew formulas must follow these guidelines:

- **Single binary per formula** (separate formulas for loom and looms)
- **Minimal dependencies** (Loom has none)
- **No sudo required** (installs to user directories)
- **Stable releases only** (no beta/alpha in main formulas)
- **Cross-platform support** (macOS ARM64/AMD64, Linux when applicable)
- **Test blocks required** (verify binary works)

## Common Issues

### "Binary not found" after install

**Solution**: Binary name might be wrong. Check with:
```bash
brew list loom-server
```

### SHA256 mismatch

**Solution**: Recalculate hash:
```bash
shasum -a 256 binary.tar.gz
```

### "Formula already installed"

**Solution**: Uninstall first:
```bash
brew uninstall --force loom loom-server
brew cleanup
```

## Updating Tap After Release

```bash
# In homebrew-loom repository
git pull origin main

# Update formulas with new version and hashes
# ... edit loom.rb and loom-server.rb ...

git add *.rb
git commit -m "Update to v1.0.1"
git push

# Users update with:
brew update
brew upgrade loom loom-server
```

## References

- [Homebrew Formula Cookbook](https://docs.brew.sh/Formula-Cookbook)
- [Homebrew Taps](https://docs.brew.sh/Taps)
- [How to Create a Homebrew Tap](https://docs.brew.sh/How-to-Create-and-Maintain-a-Tap)
- [Formula Class Reference](https://rubydoc.brew.sh/Formula)
