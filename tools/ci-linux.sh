#!/usr/bin/env bash
# The Linux leg of CI, run in a local Lima virtual machine.
#
# peer_linux.go and the linux credential store are separate implementations
# from their darwin counterparts, and until 2026-09-16 the only thing that
# had ever executed them was GitHub's ubuntu runner. When that is not
# available, this runs the same commands (tools/ci-local.sh) inside an
# Ubuntu VM on this machine. Lima (https://lima-vm.io) uses Apple's
# Virtualization framework on macOS, needs no root, and mounts this
# repository into the guest; the repository is copied inside the guest before
# running, so the rig's virtualenv and the build cache never touch the host.
#
# Usage: tools/ci-linux.sh [job ...]    (default: test cross-compile vuln relay-rig)
#   env: LIMA_INSTANCE (default holdcall-ci), LIMA_TEMPLATE (default template://ubuntu)
#
# The dashboard job is left out by default: it is platform-independent and
# tools/ci-local.sh already runs it on the host.

set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INSTANCE="${LIMA_INSTANCE:-holdcall-ci}"
TEMPLATE="${LIMA_TEMPLATE:-template://ubuntu}"
GOVERSION="$(sed -n 's/^go \(.*\)$/\1/p' "$ROOT/engine/go.mod")"
jobs=("$@"); [ ${#jobs[@]} -eq 0 ] && jobs=(test cross-compile vuln relay-rig)

command -v limactl >/dev/null || { echo "limactl is not installed; see https://lima-vm.io/docs/installation/" >&2; exit 2; }

if ! limactl list --format '{{.Name}} {{.Status}}' 2>/dev/null | grep -q "^$INSTANCE Running"; then
  if limactl list --format '{{.Name}}' 2>/dev/null | grep -qx "$INSTANCE"; then
    limactl start "$INSTANCE"
  else
    limactl start --name "$INSTANCE" --cpus 4 --memory 6 --disk 30 --tty=false "$TEMPLATE"
  fi
fi

arch="$(limactl shell "$INSTANCE" uname -m)"
case "$arch" in aarch64) goarch=arm64 ;; x86_64) goarch=amd64 ;; *) echo "unexpected guest arch $arch" >&2; exit 2 ;; esac

# Go at the version go.mod names, plus what ci.yml installs on ubuntu-latest.
limactl shell "$INSTANCE" bash -s -- "$GOVERSION" "$goarch" <<'GUEST'
set -euo pipefail
ver="$1"; goarch="$2"
if ! [ -x /usr/local/go/bin/go ] || ! /usr/local/go/bin/go version | grep -q "go$ver "; then
  curl -fsSL "https://go.dev/dl/go$ver.linux-$goarch.tar.gz" -o /tmp/go.tgz
  sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tgz
fi
if ! command -v secret-tool >/dev/null || ! command -v python3 >/dev/null || ! command -v git >/dev/null; then
  sudo apt-get update -qq && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq libsecret-tools python3 python3-venv git >/dev/null
fi
GUEST

# A writable copy of the working tree inside the guest, at the same commit.
limactl shell "$INSTANCE" bash -c 'rm -rf ~/holdcall-ci && mkdir -p ~/holdcall-ci'
limactl copy -r "$ROOT/." "$INSTANCE:holdcall-ci/" 2>/dev/null || {
  # limactl copy cannot always take a directory; fall back to tar over the shell.
  tar -C "$ROOT" --exclude ./dashboard/node_modules --exclude ./dashboard/out --exclude ./dashboard/.next \
      --exclude ./tools/relay-rig/.venv --exclude ./tools/relay-rig/holdcall-home --exclude ./engine/bin -cf - . \
    | limactl shell "$INSTANCE" tar -C "$HOME/holdcall-ci" -xf -
}

limactl shell "$INSTANCE" bash -lc "cd ~/holdcall-ci && PATH=/usr/local/go/bin:\$PATH GO=/usr/local/go/bin/go CI_LOCAL_LOG=/tmp/holdcall-ci-linux tools/ci-local.sh ${jobs[*]}"
