# Homebrew formula for Holdcall. Not yet in a tap: install it as a file with
#   brew install --formula packaging/homebrew/holdcall.rb
# once the repository is public (Homebrew cannot fetch a private release).
# The sha256 values are the ones in the v0.1.0 release's SHA256SUMS. When a
# release is made by the workflow rather than by hand, regenerate this file
# from the new SHA256SUMS; the URLs and checksums are the whole formula.
class Holdcall < Formula
  desc "Local relay that decides every MCP tool call and holds the dangerous ones for a human"
  homepage "https://holdcall.vercel.app"
  version "0.1.0"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.0/holdcall_v0.1.0_darwin_arm64.tar.gz"
      sha256 "a9aa5508c1dc2694e07d1ee65fd997442abbbf03869d77937b264f158919661d"
    end
    on_intel do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.0/holdcall_v0.1.0_darwin_amd64.tar.gz"
      sha256 "714bd228814f4874eb0c0faea1463f3a8ae9b110c9c39c3aa1ce0d7cb1985284"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.0/holdcall_v0.1.0_linux_arm64.tar.gz"
      sha256 "74cb7fec73cd112bb754ea8d2ec6f4f39aad47da80f058f57e253aea39442a74"
    end
    on_intel do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.0/holdcall_v0.1.0_linux_amd64.tar.gz"
      sha256 "48be03dab9f42fef495162f282ac0bb35197d81e89342fb97867a8d2686d358d"
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
    assert_match "holdcall v0.1.0", shell_output("#{bin}/holdcall version")
  end
end
