class LoomServer < Formula
  desc "Loom agent server with multi-agent orchestration and gRPC/HTTP APIs"
  homepage "https://github.com/teradata-labs/loom"
  version "1.3.0"
  license "Apache-2.0"

  # sha256 placeholders are stamped with real hashes by
  # .github/workflows/publish-homebrew.yml at release time.
  resource "loom-patterns" do
    url "https://github.com/teradata-labs/loom/archive/refs/tags/v1.3.0.tar.gz"
    sha256 "0000000000000000000000000000000000000000000000000000000000000000"
  end

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/teradata-labs/loom/releases/download/v1.3.0/looms-darwin-arm64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    else
      url "https://github.com/teradata-labs/loom/releases/download/v1.3.0/looms-darwin-amd64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    end
  end

  def install
    bin.install "looms-darwin-arm64" => "looms" if Hardware::CPU.arm?
    bin.install "looms-darwin-amd64" => "looms" if Hardware::CPU.intel?

    # HOME is sandboxed during install, so patterns go into the keg;
    # users copy them to ~/.loom/patterns (see caveats). Config is
    # created by the binary itself via 'looms config init'.
    resource("loom-patterns").stage do
      src = Pathname.pwd
      unless src.join("patterns").directory?
        src = Pathname.glob("loom-*").find { |d| d.join("patterns").directory? }
      end
      odie "Could not find patterns/ in Loom source (archive layout may have changed)" if src.nil?
      pkgshare.install src/"patterns"
    end
  end

  def caveats
    <<~EOS
      Loom server has been installed.

      Pattern library is staged at:
        #{opt_pkgshare}/patterns

      To install patterns into your Loom data directory:
        mkdir -p ~/.loom/patterns
        cp -R #{opt_pkgshare}/patterns/. ~/.loom/patterns/

      Next steps:
        1. Create a starter configuration ($LOOM_DATA_DIR/looms.yaml,
           default: $HOME/.loom/looms.yaml):
             looms config init

        2. Configure an LLM provider:
             looms config set llm.provider anthropic
             looms config set-key anthropic_api_key

        3. Start the server:
             looms serve

           The server will run on:
             - gRPC: localhost:60051
             - HTTP: http://localhost:5006
             - Swagger UI: http://localhost:5006/swagger-ui

        4. Install the TUI client (if not already installed):
             brew install teradata-labs/tap/loom

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
    assert_predicate pkgshare/"patterns", :directory?

    # Test config command
    system "#{bin}/looms", "config", "list"
  end
end
