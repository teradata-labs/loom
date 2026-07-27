class Loom < Formula
  desc "LLM agent framework with natural language agent creation"
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
      url "https://github.com/teradata-labs/loom/releases/download/v1.3.0/loom-darwin-arm64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    else
      url "https://github.com/teradata-labs/loom/releases/download/v1.3.0/loom-darwin-amd64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    end
  end

  def install
    bin.install "loom-darwin-arm64" => "loom" if Hardware::CPU.arm?
    bin.install "loom-darwin-amd64" => "loom" if Hardware::CPU.intel?

    # HOME is sandboxed during install, so patterns go into the keg;
    # users copy them to ~/.loom/patterns (see caveats).
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
      Loom TUI client has been installed.

      Pattern library is staged at:
        #{opt_pkgshare}/patterns

      To install patterns into your Loom data directory:
        mkdir -p ~/.loom/patterns
        cp -R #{opt_pkgshare}/patterns/. ~/.loom/patterns/

      Next steps:
        1. Install the Loom server:
           brew install teradata-labs/tap/loom-server

        2. Start the server (in another terminal):
           looms serve

        3. Create your first agent:
           loom --thread weaver

        Then type: "Create a code review assistant"

      Documentation: https://github.com/teradata-labs/loom
    EOS
  end

  test do
    assert_match "Usage:", shell_output("#{bin}/loom --help")
    assert_predicate pkgshare/"patterns", :directory?
  end
end
