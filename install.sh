#!/bin/sh
# install.sh -- download, verify and install a holdcall release binary.
#
#   curl -fsSL https://raw.githubusercontent.com/BySergiMM/holdcall/m1-bootstrap/install.sh | sh
#
# POSIX sh on purpose: this runs as `sh` regardless of what shell piped it
# in, so it avoids bashisms rather than assuming bash is what is reading it.
#
# Safe by construction, not just by convention:
#   - never sudo, and never writes outside $HOLDCALL_INSTALL_DIR
#   - the archive's SHA-256 is checked against SHA256SUMS before anything is
#     extracted or copied; a mismatch is a hard refusal, not a warning
#   - every step is printed, so piping this into `sh` is not a leap of faith
#
# HOLDCALL_VERSION=vX.Y.Z   pin a release instead of installing the latest
# HOLDCALL_INSTALL_DIR       where the binary goes (default: $HOME/.local/bin)
# HOLDCALL_REPO               owner/repo releases are published from (see below)
# HOLDCALL_DOWNLOAD_BASE     override where archives are fetched from -- exists so
#                       tools/install-rig/test.sh can point this at a local
#                       server instead of the real network

set -eu

# The repository this script downloads from. A variable, not a literal baked
# into every curl call below, so a fork only has to change this one line.
REPO="${HOLDCALL_REPO:-BySergiMM/holdcall}"

VERSION="${HOLDCALL_VERSION:-}"
INSTALL_DIR="${HOLDCALL_INSTALL_DIR:-$HOME/.local/bin}"
DOWNLOAD_BASE="${HOLDCALL_DOWNLOAD_BASE:-}"

say() {
    echo "install.sh: $*" >&2
}

die() {
    echo "install.sh: $*" >&2
    exit 1
}

# ---- os/arch detection ----------------------------------------------------
# holdcall ships darwin/arm64, darwin/amd64, linux/amd64 and linux/arm64 archives.
# windows/amd64 exists too, but this script targets `sh`; a Windows user
# takes the zip from the release page directly.

os="$(uname -s)"
arch="$(uname -m)"

case "$os" in
    Darwin) goos="darwin" ;;
    Linux) goos="linux" ;;
    *) die "unsupported OS '$os' -- holdcall ships darwin and linux archives; grab the windows zip from https://github.com/$REPO/releases instead" ;;
esac

case "$arch" in
    arm64 | aarch64) goarch="arm64" ;;
    x86_64 | amd64) goarch="amd64" ;;
    *) die "unsupported architecture '$arch'" ;;
esac

# ---- resolve the version ---------------------------------------------------
# No jq dependency on purpose: this script has almost none, and the one field
# needed out of the release JSON is easy enough to pull with grep and sed.

if [ -z "$VERSION" ]; then
    say "resolving the latest release of $REPO"
    VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
        | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
    [ -n "$VERSION" ] || die "could not resolve the latest release tag"
fi

# ---- download ---------------------------------------------------------
# DOWNLOAD_BASE replaces the whole host+path prefix, not just the host, so
# the test rig can point it at a directory served by python3 -m http.server
# that has nothing else in common with a real GitHub release.

if [ -n "$DOWNLOAD_BASE" ]; then
    base="$DOWNLOAD_BASE"
else
    base="https://github.com/$REPO/releases/download/$VERSION"
fi

archive="holdcall_${VERSION}_${goos}_${goarch}.tar.gz"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT INT TERM

say "downloading $base/$archive"
curl -fsSL -o "$work_dir/$archive" "$base/$archive"

say "downloading $base/SHA256SUMS"
curl -fsSL -o "$work_dir/SHA256SUMS" "$base/SHA256SUMS"

# ---- verify -------------------------------------------------------------
# The checksum is the thing that makes piping this into sh defensible: a
# corrupted or substituted archive is refused before tar ever reads it.

expected="$(grep -F " $archive" "$work_dir/SHA256SUMS" | awk '{print $1}')"
[ -n "$expected" ] || die "$archive is not listed in SHA256SUMS -- refusing to install it"

if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$work_dir/$archive" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$work_dir/$archive" | awk '{print $1}')"
else
    die "neither sha256sum nor shasum is available -- refusing to install an unverified binary"
fi

if [ "$expected" != "$actual" ]; then
    die "checksum mismatch for $archive
  expected $expected
  got      $actual
This archive does not match SHA256SUMS and was not installed."
fi
say "checksum verified: $actual"

# ---- install ------------------------------------------------------------
# Never sudo, and never writes anywhere but INSTALL_DIR: whether that
# directory needs root is the caller's decision, made by the value of
# HOLDCALL_INSTALL_DIR, not this script's.

tar -xzf "$work_dir/$archive" -C "$work_dir"
extracted="$work_dir/holdcall_${VERSION}_${goos}_${goarch}"
[ -f "$extracted/holdcall" ] || die "archive did not contain a holdcall binary at the expected path"

mkdir -p "$INSTALL_DIR"
cp "$extracted/holdcall" "$INSTALL_DIR/holdcall"
chmod 755 "$INSTALL_DIR/holdcall"

say "installed holdcall $VERSION to $INSTALL_DIR/holdcall"

case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
        say "$INSTALL_DIR is not on your PATH. Add this to your shell profile:"
        say "  export PATH=\"$INSTALL_DIR:\$PATH\""
        ;;
esac
