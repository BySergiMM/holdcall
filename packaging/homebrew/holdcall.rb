# Homebrew formula for Holdcall v0.1.2. It is not published: the tap
# BySergiMM/homebrew-tap exists (it holds the OmniMac cask) but has no
# Formula/holdcall.rb, so `brew install bysergimm/tap/holdcall` does not work, and
# nothing in this repository says it does. This file only records the release's
# URLs and checksums; checking it with `brew` has not been done.
# The sha256 values are the ones of the v0.1.2 release's assets (they match the
# digests GitHub reports for them). To publish it, this file would be added to
# that tap as Formula/holdcall.rb. Installing from a bare path
# (`brew install --formula packaging/homebrew/holdcall.rb`) depends on the
# Homebrew version, which may refuse it; that was not tried. When a release is
# made by the workflow rather than by hand, regenerate this file from the new
# SHA256SUMS; the URLs and checksums are the whole formula.
class Holdcall < Formula
  desc "Local relay that decides every MCP tool call and holds the dangerous ones for a human"
  homepage "https://holdcall.vercel.app"
  version "0.1.2"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.2/holdcall_v0.1.2_darwin_arm64.tar.gz"
      sha256 "24e87d114b1ad11b9a27a4d063cb8f107c64752b826e338505f8ad4eb0ee42a0"
    end
    on_intel do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.2/holdcall_v0.1.2_darwin_amd64.tar.gz"
      sha256 "077326ac0f045e96d6d6a13cc1399d6b5ae6c11c02f53beee45eb972d1dfb4f1"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.2/holdcall_v0.1.2_linux_arm64.tar.gz"
      sha256 "e4c7486b3b2b4de9a2918f4e6d17695fafa98e73a4b8b0d7c84f0c92cd58a293"
    end
    on_intel do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.2/holdcall_v0.1.2_linux_amd64.tar.gz"
      sha256 "d3881ffa6b4ae5b260735432b3e1f54dd69aa1b8b2c8ca9c099558d2a00051c6"
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
    assert_match "holdcall v0.1.2", shell_output("#{bin}/holdcall version")
  end
end
