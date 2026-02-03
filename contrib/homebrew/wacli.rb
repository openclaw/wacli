class Wacli < Formula
  desc "WhatsApp CLI — sync, send, and manage WhatsApp from the terminal"
  homepage "https://github.com/steipete/wacli"
  url "https://github.com/steipete/wacli/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "REPLACE_WITH_ACTUAL_SHA256"
  license "MIT"
  head "https://github.com/steipete/wacli.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}"), "./cmd/wacli"
  end

  service do
    run [opt_bin/"wacli", "sync", "--follow"]
    keep_alive true
    log_path var/"log/wacli-sync.log"
    error_log_path var/"log/wacli-sync.log"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/wacli version")
  end
end
