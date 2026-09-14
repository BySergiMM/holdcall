#!/bin/sh
# tools/install-rig/test.sh -- exercises install.sh's checksum-verification
# branch against a local server, without touching the network or depending
# on a real release existing.
#
# Two cases, against one fake archive built here:
#   - SHA256SUMS lists the archive's real checksum: install.sh must install it
#   - SHA256SUMS lists a wrong one: install.sh must refuse, and install nothing
#
# Needs python3 (for http.server, the same way tools/relay-rig does) and
# either sha256sum or shasum, same as install.sh itself.

set -eu

root="$(cd "$(dirname "$0")/../.." && pwd)"
install_sh="$root/install.sh"

work="$(mktemp -d)"
server_pid=""
cleanup() {
    [ -n "$server_pid" ] && kill "$server_pid" 2>/dev/null || true
    rm -rf "$work"
}
trap cleanup EXIT INT TERM

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

# Match install.sh's own os/arch detection, so the archive it goes looking
# for is the one this rig actually builds.
os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
    Darwin) goos="darwin" ;;
    Linux) goos="linux" ;;
    *)
        echo "install-rig: unsupported OS '$os' for this rig" >&2
        exit 1
        ;;
esac
case "$arch" in
    arm64 | aarch64) goarch="arm64" ;;
    x86_64 | amd64) goarch="amd64" ;;
    *)
        echo "install-rig: unsupported architecture '$arch' for this rig" >&2
        exit 1
        ;;
esac

version="v0.0.0-test"
name="nim_${version}_${goos}_${goarch}"
archive="$name.tar.gz"

server_dir="$work/server"
mkdir -p "$server_dir/$name"
# A fake binary is enough: install.sh only copies it, never runs it.
printf '#!/bin/sh\necho fake nim\n' >"$server_dir/$name/nim"
echo "fake readme" >"$server_dir/$name/README.md"
echo "fake license" >"$server_dir/$name/LICENSE"
(cd "$server_dir" && tar czf "$archive" "$name" && rm -rf "$name")

good_sum="$(sha256 "$server_dir/$archive")"

# ---- serve it ---------------------------------------------------------
# -u: unbuffered, so the "Serving HTTP on ... port N" line actually reaches
# the log file promptly instead of sitting in Python's stdout buffer because
# it is no longer a tty.

log="$work/server.log"
python3 -u -m http.server 0 --bind 127.0.0.1 --directory "$server_dir" >"$log" 2>&1 &
server_pid=$!

port=""
i=0
while [ "$i" -lt 20 ]; do
    port="$(sed -nE 's/.*port ([0-9]+).*/\1/p' "$log" | head -1)"
    [ -n "$port" ] && break
    sleep 0.2
    i=$((i + 1))
done
if [ -z "$port" ]; then
    echo "install-rig: local server never reported its port" >&2
    cat "$log" >&2
    exit 1
fi

base="http://127.0.0.1:$port"
i=0
until curl -fsS -o /dev/null "$base/$archive" 2>/dev/null; do
    i=$((i + 1))
    if [ "$i" -ge 20 ]; then
        echo "install-rig: local server never answered for $archive" >&2
        exit 1
    fi
    sleep 0.1
done

fail=0

# ---- case 1: the real checksum installs the binary -------------------

echo "$good_sum  $archive" >"$server_dir/SHA256SUMS"

install_dir_ok="$work/install-ok"
if NIM_VERSION="$version" NIM_DOWNLOAD_BASE="$base" NIM_INSTALL_DIR="$install_dir_ok" \
    sh "$install_sh" >"$work/ok.out" 2>&1; then
    if [ -f "$install_dir_ok/nim" ]; then
        echo "PASS: a correct checksum installed the binary"
    else
        echo "FAIL: a correct checksum reported success but installed nothing"
        cat "$work/ok.out" >&2
        fail=1
    fi
else
    echo "FAIL: a correct checksum was refused"
    cat "$work/ok.out" >&2
    fail=1
fi

# ---- case 2: a wrong checksum refuses, and installs nothing -----------

echo "deadbeef  $archive" >"$server_dir/SHA256SUMS"

install_dir_bad="$work/install-bad"
if NIM_VERSION="$version" NIM_DOWNLOAD_BASE="$base" NIM_INSTALL_DIR="$install_dir_bad" \
    sh "$install_sh" >"$work/bad.out" 2>&1; then
    echo "FAIL: a wrong checksum was accepted"
    cat "$work/bad.out" >&2
    fail=1
elif [ -f "$install_dir_bad/nim" ]; then
    echo "FAIL: a wrong checksum was refused but a binary was installed anyway"
    fail=1
elif ! grep -q "checksum mismatch" "$work/bad.out"; then
    echo "FAIL: refused for the wrong reason (no 'checksum mismatch' in its output)"
    cat "$work/bad.out" >&2
    fail=1
else
    echo "PASS: a wrong checksum was refused, and nothing was installed"
fi

exit "$fail"
