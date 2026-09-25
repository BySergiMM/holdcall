# Contributing

Nim is pre-alpha and built in the open about what it does and does not do.
The bar for a change is the same one the dashboard enforces on the project's
own claims: nothing is stated without a test behind it.

## Before you start

- Read `README.md`, then `docs/architecture.md` for where each layer lives
  and `docs/security.md` for the threat model. A change that moves the trust
  boundary needs a decision record in `docs/decisions/` first.
- The engine is Go with no C toolchain; `engine/go.mod` names the version.
  The dashboard is Node; `dashboard/package.json` pins its dependencies.

## The loop

```bash
tools/ci-local.sh          # every CI job, locally: format, vet, tidy, tests, cross-compile, vuln, dashboard, rig
tools/ci-local.sh test     # just the Go suite, with -race -shuffle and the evidence script
```

Tests are named as sentences that state the property they hold
(`TestARefusedCallNeverReachesTheConnector`). Comments say why, not what.
A guarantee or an attack the dashboard shows must cite a test that exists;
`cd dashboard && npm run check` refuses otherwise, and that refusal is the
point.

## What a pull request carries

- The change, with its tests, gofmt'd.
- `CHANGELOG.md` under *Unreleased*.
- If it closes or opens a finding, or changes what a guarantee claims,
  `dashboard/data/state.json` says so, with the test names.
- Nothing from a real installation: every test sets `NIM_HOME` and `HOME` to
  a temporary directory, and so should any script you add.

Security reports go through `SECURITY.md`, not the issue tracker.
