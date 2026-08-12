#!/usr/bin/env bash
#
# Every test the dashboard cites as evidence for a verified guarantee or a
# defended attack must actually have RUN on this platform -- not been skipped.
#
# This exists because of how much of this suite can quietly not run. Fourteen
# call sites reach for t.Skip, and they guard the load-bearing ones: the single
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

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
log="${1:?usage: assert-evidence-ran.sh <path to a go test -v log>}"
state="$root/dashboard/data/state.json"

[ -f "$log" ] || { echo "no test log at $log" >&2; exit 2; }
[ -f "$state" ] || { echo "no dashboard state at $state" >&2; exit 2; }

# The claims that carry weight. A guarantee at "partial" or an attack at
# "fail" is already telling the truth about itself, so its tests are not
# required to run for the page to be honest.
cited="$(jq -r '
  [ (.guarantees[] | select(.status == "verified") | .tests[]),
    (.attacks[]    | select(.status == "pass")     | .tests[]) ]
  | unique | .[]
' "$state")"

skipped=()
absent=()
ran=0

while IFS= read -r name; do
  [ -z "$name" ] && continue
  if grep -qE "^ *--- SKIP: ${name}( |\$)" "$log"; then
    skipped+=("$name")
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
echo "ok: every cited test either ran or does not exist on this platform."
