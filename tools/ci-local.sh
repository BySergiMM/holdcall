#!/usr/bin/env bash
# The CI pipeline, run here instead of on GitHub's runners.
#
# .github/workflows/ci.yml is the source of truth; this script runs the same
# commands, in the same order, with the same assertions, on whatever machine
# it is started on. It exists because a pipeline that only runs on hosted
# runners cannot run when those are unavailable -- which happened on
# 2026-09-16, when the account's billing state stopped every job at start --
# and a claim of "green" has to be reproducible without them.
#
# Runs every job the workflow has: test (gofmt, vet, tidy, go test -race
# -shuffle -v, the evidence script), cross-compile (five targets),
# vuln (govulncheck), dashboard (check and build, with the output scan), and
# the relay rig. Each prints one PASS/FAIL line; the exit code is 1 if any
# failed. What it cannot do is be another operating system: run it under
# tools/ci-linux.sh for the Linux leg, and nothing here runs Windows.
#
# Usage: tools/ci-local.sh [job ...]   (default: every job)
#   jobs: test cross-compile vuln dashboard relay-rig
#
# Needs: go (the version in engine/go.mod; go downloads the toolchain if
# the installed one is older), node and npm for the dashboard, python3 >=
# 3.10 for the rig. Set GO to a specific binary if go is not on PATH.

set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO="${GO:-go}"
LOG="${CI_LOCAL_LOG:-/tmp/nim-ci-local}"
mkdir -p "$LOG"

results=()
record() { results+=("$1 $2"); printf '\n=== %s: %s\n' "$2" "$1"; }

job_test() {
  cd "$ROOT/engine" || return 1
  local ok=0
  echo "-- gofmt"
  # gofmt lives beside go; PATH may only know one of the two.
  local gofmt="gofmt"; [ -x "$(dirname "$(command -v "$GO")")/gofmt" ] && gofmt="$(dirname "$(command -v "$GO")")/gofmt"
  local unformatted; unformatted=$("$gofmt" -l .)
  if [ -n "$unformatted" ]; then echo "These files are not gofmt'd:"; echo "$unformatted"; ok=1; fi
  echo "-- go vet"; "$GO" vet ./... || ok=1
  echo "-- go mod tidy"
  "$GO" mod tidy && git diff --exit-code go.mod go.sum || ok=1
  echo "-- go test -race -shuffle=on -count=1 -v"
  set -o pipefail
  "$GO" test -race -shuffle=on -count=1 -v ./... 2>&1 | tee "$LOG/gotest.log" | grep -E '^(ok|FAIL|--- FAIL|panic:)' || ok=1
  set +o pipefail
  echo "-- adversarial tests must have run, not skipped"
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*|Windows_NT)
      bash "$ROOT/.github/scripts/assert-evidence-ran.sh" "$LOG/gotest.log" "$ROOT/.github/scripts/windows-skip-allowlist.txt" || ok=1 ;;
    *)
      bash "$ROOT/.github/scripts/assert-evidence-ran.sh" "$LOG/gotest.log" || ok=1 ;;
  esac
  return $ok
}

job_cross_compile() {
  cd "$ROOT/engine" || return 1
  local ok=0 pair
  for pair in darwin:arm64 darwin:amd64 linux:amd64 linux:arm64 windows:amd64; do
    local goos="${pair%%:*}" goarch="${pair##*:}"
    printf -- '-- %s/%s: ' "$goos" "$goarch"
    if CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$GO" build ./... && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$GO" vet ./...; then
      echo OK
    else
      echo FAILED; ok=1
    fi
  done
  return $ok
}

job_vuln() {
  cd "$ROOT/engine" || return 1
  "$GO" run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
}

job_dashboard() {
  cd "$ROOT/dashboard" || return 1
  npm ci --no-audit --no-fund >/dev/null && npm run check && npm run build
}

job_relay_rig() {
  cd "$ROOT" || return 1
  local py="${PYTHON:-python3}"
  ( cd engine && "$GO" build -o bin/nim ./cmd/nim ) || return 1
  if [ ! -x tools/relay-rig/.venv/bin/python ]; then
    "$py" -m venv tools/relay-rig/.venv || return 1
    tools/relay-rig/.venv/bin/pip install -q fastmcp==4.0.3 || return 1
  fi
  # A scratch HOME, so the rig's daemon and relays never see the real install.
  local home; home="$(mktemp -d)"
  set -o pipefail
  HOME="$home" tools/relay-rig/.venv/bin/python tools/relay-rig/compare.py 2>&1 | tee "$LOG/relay-rig.log" | grep -E 'MATCH|DIFFERS|WRONG|RESULT'
  local status=$?
  set +o pipefail
  local last; last="$(tail -n1 "$LOG/relay-rig.log")"
  if [ "$status" -ne 0 ] || [ "$last" != "RESULT: IDENTICAL" ]; then
    echo "expected the last line to be 'RESULT: IDENTICAL', got: $last" >&2
    return 1
  fi
}

jobs=("$@")
[ ${#jobs[@]} -eq 0 ] && jobs=(test cross-compile vuln dashboard relay-rig)
for j in "${jobs[@]}"; do
  case "$j" in
    test)          job_test          && record PASS test          || record FAIL test ;;
    cross-compile) job_cross_compile && record PASS cross-compile || record FAIL cross-compile ;;
    vuln)          job_vuln          && record PASS vuln          || record FAIL vuln ;;
    dashboard)     job_dashboard     && record PASS dashboard     || record FAIL dashboard ;;
    relay-rig)     job_relay_rig     && record PASS relay-rig     || record FAIL relay-rig ;;
    *) echo "unknown job: $j (test cross-compile vuln dashboard relay-rig)" >&2; exit 2 ;;
  esac
done

echo
echo "== CI (local, $(uname -s)/$(uname -m), $(git -C "$ROOT" rev-parse --short HEAD)) =="
failed=0
for r in "${results[@]}"; do echo "  $r"; case "$r" in FAIL*) failed=1 ;; esac; done
echo "  logs in $LOG"
exit $failed
