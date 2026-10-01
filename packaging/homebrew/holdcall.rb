# Homebrew formula for Holdcall v0.1.3. It is not published: the tap
# BySergiMM/homebrew-tap exists (it holds the OmniMac cask) but has no
# Formula/holdcall.rb, so `brew install bysergimm/tap/holdcall` does not work, and
# nothing in this repository says it does. This file only records the release's
# URLs and checksums; checking it with `brew` has not been done.
# The sha256 values are the ones of the v0.1.3 release's assets (they match the
# digests GitHub reports for them). To publish it, this file would be added to
# that tap as Formula/holdcall.rb. Installing from a bare path
# (`brew install --formula packaging/homebrew/holdcall.rb`) depends on the
# Homebrew version, which may refuse it; that was not tried. When a release is
# made by the workflow rather than by hand, regenerate this file from the new
# SHA256SUMS; the URLs and checksums are the whole formula.
class Holdcall < Formula
  desc "Local relay that decides every MCP tool call and holds the dangerous ones for a human"
  homepage "https://holdcall.vercel.app"
  version "0.1.3"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.3/holdcall_v0.1.3_darwin_arm64.tar.gz"
      sha256 "1c1e4b1c0b9be6f65baf02a1dec36a22c7537a71ce30da7e262a1c0f134cea02"
    end
    on_intel do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.3/holdcall_v0.1.3_darwin_amd64.tar.gz"
      sha256 "d37863e98cda4787355fdc3842ffacc58b42620cf044fad09902c8a003c127e8"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.3/holdcall_v0.1.3_linux_arm64.tar.gz"
      sha256 "2a9b77bade2a167a41f6569b8e1da75b80c3126c0493d0991102fa8a14344fa4"
    end
    on_intel do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.3/holdcall_v0.1.3_linux_amd64.tar.gz"
      sha256 "67f99eb1b06e6e062042627b5a667eb16a55117152fc83c5556b98225681c63d"
    end
  end

  def install
    bin.install "holdcall"
  end

  def caveats
    <<~EOS
      If a daemon from a previous build is running: holdcall daemon restart
      Then point your MCP clients at it: holdcall init --write
    EOS
  end

  test do
    assert_match "holdcall v0.1.3", shell_output("#{bin}/holdcall version")
  end
end
