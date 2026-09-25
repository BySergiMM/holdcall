#!/usr/bin/env bash
#
# Every test the dashboard cites as evidence for a verified guarantee or a
# defended attack must actually have RUN on this platform -- not been skipped.
#
# This exists because of how much of this suite can quietly not run. A dozen
# or so call sites reach for t.Skip, and they guard the load-bearing ones: the single
# test proving peer identity survives a path swap skips if the attacker process
# never connects, the impostor and non-Nim-process tests skip without python3,
# the agent derivation test skips if it cannot read its own parent. `go test`
# without -v prints "ok" for a package where every one of those skipped, so the
# dashboard could keep saying "verified" about a property nothing had checked
# in weeks.
#
# The rule, per cited test, against a `go test -v` log:
#
#   --- PASS   ran, and passed                                        fine
#   --- SKIP   compiled in, chose not to run                          FAIL
#   absent     build-tagged out for this platform                     fine
#
# Absent is fine because a darwin-only test genuinely does not exist on linux,
# and the dashboard's per-platform columns are what carry that. Skipped is not
# fine, because it is indistinguishable from passing in a non-verbose log --
# which is the entire failure mode this closes.
#
# A cited test that exists nowhere is already caught earlier, by the dashboard
# generator refusing to build.
#
# The second, optional argument is an allow-list for a platform where a
# specific, small set of cited tests are skipped BY DESIGN rather than by
# accident -- windows, where peer identity has no kernel-verified answer at
# all (see internal/peer/peer_windows.go) and agent enrolment's image lookup
# is an admitted stub (internal/peer/image_windows.go). Without this a
# platform that cannot demonstrate a property at all could never run this
# script clean, which would either block CI on windows forever or -- worse --
# pressure someone into deleting the check instead. Only a test named in the
# allow-list, with its own stated reason, is excused; everything else a
# skipped test does here is exactly as fatal as it always was. Linux and
# macOS invoke this script with one argument, as before, so the allow-list
# changes nothing for them: it exists, it is read, or it is not built at all.
#
# Allow-list format: one test per non-blank, non-'#'-comment line,
#   TESTNAME<whitespace>the reason this platform cannot run it
# A name with no reason after it is refused -- an unexplained exception is
# exactly the kind of asserted-not-argued status docs/dashboard.md exists to
# catch everywhere else on this page, and this file is no different.
#
# Plain indexed arrays rather than an associative one, deliberately: this
# runs on whatever `bash` a runner has, and macOS still ships bash 3.2, which
# has no `declare -A`. A handful of linear scans over a handful of names costs
# nothing worth avoiding that for.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
log="${1:?usage: assert-evidence-ran.sh <path to a go test -v log> [allow-list file]}"
allowlist="${2:-}"
state="$root/dashboard/data/state.json"

[ -f "$log" ] || { echo "no test log at $log" >&2; exit 2; }
[ -f "$state" ] || { echo "no dashboard state at $state" >&2; exit 2; }

allowed_names=()
allowed_reasons=()

# reason_for NAME: prints the stored reason and returns 0 if NAME is on the
# allow-list, otherwise returns 1 and prints nothing.
reason_for() {
  local target="$1" i
  for i in "${!allowed_names[@]}"; do
    if [ "${allowed_names[$i]}" = "$target" ]; then
      printf '%s' "${allowed_reasons[$i]}"
      return 0
    fi
  done
  return 1
}

if [ -n "$allowlist" ]; then
  [ -f "$allowlist" ] || { echo "no allow-list at $allowlist" >&2; exit 2; }
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    [ -z "$line" ] && continue
    case "$line" in \#*) continue ;; esac
    name="${line%%[[:space:]]*}"
    reason="$(printf '%s' "$line" | sed -E 's/^[^[:space:]]+[[:space:]]+//')"
    if [ -z "$name" ] || [ "$reason" = "$line" ] || [ -z "$reason" ]; then
      echo "malformed allow-list entry (need 'TESTNAME <reason>'): $line" >&2
      exit 2
    fi
    if reason_for "$name" >/dev/null; then
      echo "duplicate allow-list entry: $name" >&2
      exit 2
    fi
    allowed_names+=("$name")
    allowed_reasons+=("$reason")
  done < "$allowlist"
fi

# The claims that carry weight. A guarantee at "partial" or an attack at
# "fail" is already telling the truth about itself, so its tests are not
# required to run for the page to be honest.
cited="$(jq -r '
  [ (.guarantees[] | select(.status == "verified") | .tests[]),
    (.attacks[]    | select(.status == "pass")     | .tests[]) ]
  | unique | .[]
' "$state")"

skipped=()
excused_names=()
excused_reasons=()
absent=()
ran=0

while IFS= read -r name; do
  [ -z "$name" ] && continue
  if grep -qE "^ *--- SKIP: ${name}( |\$)" "$log"; then
    if reason="$(reason_for "$name")"; then
      excused_names+=("$name")
      excused_reasons+=("$reason")
    else
      skipped+=("$name")
    fi
  elif grep -qE "^ *--- PASS: ${name}( |\$)" "$log"; then
    ran=$((ran + 1))
  else
    absent+=("$name")
  fi
done <<< "$cited"

echo "cited tests that ran here:            $ran"
echo "cited tests build-tagged out here:    ${#absent[@]}"
for name in "${absent[@]:-}"; do
  [ -n "$name" ] && echo "    - $name"
done

if [ "${#excused_names[@]}" -gt 0 ]; then
  echo "cited tests skipped, by documented exception: ${#excused_names[@]}"
  for i in "${!excused_names[@]}"; do
    echo "    - ${excused_names[$i]}: ${excused_reasons[$i]}"
  done
fi

# An allow-list entry that never matched a SKIP is not a passing grade for
# the platform, it is a stale exception -- the test started running again (or
# never existed here to begin with) and nothing is checking the excuse is
# still true. Refuse rather than let it rot silently.
if [ -n "$allowlist" ]; then
  stale=()
  for name in "${allowed_names[@]:-}"; do
    [ -z "$name" ] && continue
    matched=false
    for e in "${excused_names[@]:-}"; do
      [ "$e" = "$name" ] && matched=true
    done
    [ "$matched" = false ] && stale+=("$name")
  done
  if [ "${#stale[@]}" -gt 0 ]; then
    echo
    echo "FAIL: ${#stale[@]} allow-list entr$([ "${#stale[@]}" -eq 1 ] && echo y || echo ies) never matched a SKIP here:"
    for name in "${stale[@]}"; do
      echo "    - $name"
    done
    echo
    echo "Either it now runs (drop the entry) or it is not cited here at all"
    echo "(drop the entry) -- an allow-list exception that excuses nothing is"
    echo "not evidence of anything."
    exit 1
  fi
fi

if [ "${#skipped[@]}" -gt 0 ]; then
  echo
  echo "FAIL: ${#skipped[@]} test(s) the dashboard cites as evidence were SKIPPED here."
  for name in "${skipped[@]}"; do
    echo "    - $name"
    grep -E "^ *--- SKIP: ${name}( |\$)" -A1 "$log" | sed 's/^/        /'
  done
  echo
  echo "The dashboard calls the property these back 'verified'. It did not run,"
  echo "so that word is not earned. Either fix the environment so it runs, or"
  echo "downgrade the claim -- do not delete this check."
  exit 1
fi

echo
echo "ok: every cited test either ran, does not exist on this platform, or is a documented platform exception."
