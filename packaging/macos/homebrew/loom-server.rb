class LoomServer < Formula
  desc "Loom agent server with multi-agent orchestration and gRPC/HTTP APIs"
  homepage "https://github.com/teradata-labs/loom"
  version "1.1.0"
  license "Apache-2.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/teradata-labs/loom/releases/download/v1.1.0/looms-darwin-arm64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    else
      url "https://github.com/teradata-labs/loom/releases/download/v1.1.0/looms-darwin-amd64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    end
  end

  def install
    # Install binary
    bin.install "looms-darwin-arm64" => "looms" if Hardware::CPU.arm?
    bin.install "looms-darwin-amd64" => "looms" if Hardware::CPU.intel?

    # Create Loom data directory (respects LOOM_DATA_DIR env var)
    loom_dir = ENV["LOOM_DATA_DIR"] || "#{Dir.home}/.loom"
    patterns_dir = "#{loom_dir}/patterns"
    config_file = "#{loom_dir}/looms.yaml"

    system "mkdir", "-p", loom_dir
    system "mkdir", "-p", patterns_dir

    # Download and install patterns
    ohai "Downloading patterns..."
    patterns_url = "https://github.com/teradata-labs/loom/archive/refs/tags/v#{version}.tar.gz"
    patterns_tmp = "#{Dir.tmpdir}/loom-patterns-#{version}.tar.gz"

    system "curl", "-L", "-o", patterns_tmp, patterns_url
    system "tar", "xzf", patterns_tmp, "-C", Dir.tmpdir

    extracted_dir = "#{Dir.tmpdir}/loom-#{version}"
    if File.directory?("#{extracted_dir}/patterns")
      system "cp", "-R", "#{extracted_dir}/patterns/", patterns_dir
      pattern_count = Dir.glob("#{patterns_dir}/**/*.yaml").length
      ohai "Installed #{pattern_count} pattern files to #{patterns_dir}"
    end

    # Cleanup
    system "rm", "-rf", patterns_tmp, extracted_dir

    # Create default config if it doesn't exist
    unless File.exist?(config_file)
      ohai "Creating default configuration..."
      File.write(config_file, <<~YAML)
        # Loom Server Configuration
        server:
          host: "0.0.0.0"
          port: 60051

        # Database stored in Loom data directory
        database:
          path: "#{loom_dir}/loom.db"

        # Communication system (shared memory, message queue)
        communication:
          store:
            backend: sqlite
            path: "#{loom_dir}/loom.db"

        # Observability (optional - requires Hawk)
        observability:
          enabled: false

        # MCP servers (add your own)
        mcp:
          servers: {}

        # No pre-configured agents - use the weaver to create threads on demand
        agents:
          agents: {}
      YAML
      ohai "Configuration created at #{config_file}"
    end
  end

  def post_install
    ohai "Loom server installed successfully!"
    ohai ""
    ohai "Next steps:"
    ohai "  1. Configure an LLM provider:"
    ohai "       looms config set llm.provider anthropic"
    ohai "       looms config set-key anthropic_api_key"
    ohai ""
    ohai "  2. Start the server:"
    ohai "       looms serve"
    ohai ""
    ohai "  3. Install the TUI client (if not already installed):"
    ohai "       brew install loom"
    ohai ""
    ohai "  4. Create your first agent (in another terminal):"
    ohai "       loom --thread weaver"
  end

  def caveats
    <<~EOS
      Loom server has been installed.

      Configuration file: $LOOM_DATA_DIR/looms.yaml (default: $HOME/.loom/looms.yaml)
      Patterns directory: $LOOM_DATA_DIR/patterns (default: $HOME/.loom/patterns)

      To start the server:
        looms serve

      The server will run on:
        - gRPC: localhost:60051
        - HTTP: http://localhost:5006
        - Swagger UI: http://localhost:5006/swagger-ui

      To configure an LLM provider:
        looms config set llm.provider anthropic
        looms config set-key anthropic_api_key

      Documentation: https://github.com/teradata-labs/loom
    EOS
  end

  service do
    run [opt_bin/"looms", "serve"]
    keep_alive true
    log_path var/"log/loom.log"
    error_log_path var/"log/loom.error.log"
    working_dir HOMEBREW_PREFIX
  end

  test do
    assert_match "Usage:", shell_output("#{bin}/looms --help")

    # Test config command
    system "#{bin}/looms", "config", "list"
  end
end
