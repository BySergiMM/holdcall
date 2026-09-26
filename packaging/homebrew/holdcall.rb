# Homebrew formula for Holdcall. Not yet in a tap: install it as a file with
#   brew install --formula packaging/homebrew/holdcall.rb
# once the repository is public (Homebrew cannot fetch a private release).
# The sha256 values are the ones in the v0.1.1 release's SHA256SUMS. When a
# release is made by the workflow rather than by hand, regenerate this file
# from the new SHA256SUMS; the URLs and checksums are the whole formula.
class Holdcall < Formula
  desc "Local relay that decides every MCP tool call and holds the dangerous ones for a human"
  homepage "https://holdcall.vercel.app"
  version "0.1.1"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.1/holdcall_v0.1.1_darwin_arm64.tar.gz"
      sha256 "c50afdaa41990517fba0416f2ed97037493d3b8dc6b430d391ae3cc68fca6572"
    end
    on_intel do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.1/holdcall_v0.1.1_darwin_amd64.tar.gz"
      sha256 "91a1f8a97a803c52af1d4838c7b9d078db9b57c2d66b8a5c4898cf88fd4bb321"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.1/holdcall_v0.1.1_linux_arm64.tar.gz"
      sha256 "35db6d4de350431f6c74b3a02a11f77e92a67484cc32b2c25194be494b43b219"
    end
    on_intel do
      url "https://github.com/BySergiMM/holdcall/releases/download/v0.1.1/holdcall_v0.1.1_linux_amd64.tar.gz"
      sha256 "536b1958e52b8b4eb66c1a281f65a4c3feff968955c5adbfd566142b78aee2fc"
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
    assert_match "holdcall v0.1.1", shell_output("#{bin}/holdcall version")
  end
end
