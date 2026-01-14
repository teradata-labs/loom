class Loom < Formula
  desc "LLM agent framework with natural language agent creation"
  homepage "https://github.com/teradata-labs/loom"
  version "1.0.1"
  license "Apache-2.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/teradata-labs/loom/releases/download/v1.0.1/loom-darwin-arm64.tar.gz"
      sha256 "50f9715a2310427c4419d942dd45450ea026321a8aee462df72becb65643f46a"
    else
      url "https://github.com/teradata-labs/loom/releases/download/v1.0.1/loom-darwin-amd64.tar.gz"
      sha256 "e4cd3476253bad48fd4cae3a4071510dccd26f98c9fbb92f118dab479bf73d9b"
    end
  end

  def install
    # Install binary
    bin.install "loom-darwin-arm64" => "loom" if Hardware::CPU.arm?
    bin.install "loom-darwin-amd64" => "loom" if Hardware::CPU.intel?

    # Create Loom data directory
    loom_dir = "#{Dir.home}/.loom"
    patterns_dir = "#{loom_dir}/patterns"

    # Download and install patterns
    ohai "Downloading patterns..."
    system "mkdir", "-p", patterns_dir

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

    # Set environment variable hint
    opoo "Loom TUI client installed successfully!"
    opoo "To use Loom, you also need to install the server:"
    opoo "  brew install loom-server"
    opoo ""
    opoo "Or start the server manually:"
    opoo "  looms serve"
  end

  def caveats
    <<~EOS
      Loom TUI client has been installed.

      Next steps:
        1. Install the Loom server:
           brew install loom-server

        2. Configure an LLM provider:
           export ANTHROPIC_API_KEY="your-key"
           # or configure Bedrock, OpenAI, etc.

        3. Start the server (in another terminal):
           looms serve

        4. Create your first agent:
           loom --thread weaver

        Then type: "Create a code review assistant"

      Documentation: https://github.com/teradata-labs/loom
      Patterns installed to: #{Dir.home}/.loom/patterns
    EOS
  end

  test do
    assert_match "Usage:", shell_output("#{bin}/loom --help")
  end
end
