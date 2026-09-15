# Changelog

All notable changes to this project are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

No version has ever been tagged. `[Unreleased]` describes everything built so
far on `m1-bootstrap`; there is no prior release for it to diff against, so
read it as a status report rather than a list of deltas. `docs/milestones.md`
is the source of truth this section summarizes, and is where the detail and
the test names behind each claim live.

## [Unreleased]

### Fixed

- **Four findings from the 2026-09-15 review of the merged M4.5 work, closed
  before it shipped.** Two names could be enrolled against one executable and
  the lookup between them had no order, so a same-user process that enrolled a
  second name against the operator's client binary would have inherited that
  name's rules after a routine re-enrolment; one executable is one agent now.
  `nim init`'s dry run printed the `env` block where client configs keep their
  tokens; values are hidden. Two `--write` runs in one second overwrote the
  first backup; names carry nanoseconds and are created exclusively, and a
  symlinked config is written through rather than replaced. The relay could
  wait on its connector from two goroutines at once; it stops it once.

### Added

- **M1 -- pass-through relay.** `nim serve` spawns one downstream MCP server
  and relays stdio both ways, forwarding every byte unmodified except for
  recognising `tools/call` and reporting it to the daemon. Verified against a
  real MCP client: direct and relayed runs were identical on tool list,
  schemas, unicode, a 512 KiB payload, error propagation and ping
  (`tools/relay-rig`).
- **M1.5 -- an append-only, hash-chained journal.** Every call is two
  immutable entries (`call.request`, `call.outcome`) rather than one row
  updated in place; `nim log`, `nim verify` and `nim status` all read the
  same projection. The chain detects corruption and edits that did not
  recompute it. It does **not** detect a local attacker who can write to
  `nim.db`: nothing in the chain is secret, so it can be recomputed and a
  shorter chain verifies just as cleanly -- only a head recorded elsewhere
  (`nim verify --expect-head`) closes that. Detectable gaps (dropped events,
  unparseable frames, batches) are counted and reported; gaps that leave no
  trace are, by construction, not.
- **M2 -- minimal enforcement.** A `tools/call` is decided before it is
  forwarded, and the decision is written to the journal before the daemon
  answers. No daemon, a slow daemon, a closed socket, or a reply that does
  not match the question, are all denials: there is no path from an
  undecided call to one that reaches a server. The deny list lives in
  `config.toml`, which is deliberately weak scaffolding -- exact tool names
  only, no wildcards, no scopes -- and anything able to write that file can
  empty it. The decision costs p99 0.27 ms for one relay, durable write
  included (`docs/benchmarks.md`).
- **M2.5 -- daemon lifetime and socket identity.** The daemon detaches from
  the shim's process group and session, so killing a client's session does
  not take enforcement down with it. An `flock`-based startup lock keeps
  several shims racing after a crash from each becoming the daemon. Socket
  identity is now checked in both directions -- the shim verifies the daemon
  before writing a single byte to it, not only the reverse -- which closes an
  impostor-daemon path that was reproduced live against the merged binary and
  would otherwise have collected tool names and argument digests unnoticed.
- **M3 -- credentials.** Secrets move out of the client's configuration file
  and into the OS credential store (Keychain, Secret Service, DPAPI), never
  into SQLite and never onto argv, and are injected only into the one
  downstream command a connector is registered with. A command given on the
  command line is now ignored once a connector supplies one, closing a
  bypass where any caller could point a registered connector at a command of
  its own choosing and receive its secret. Two known limitations remain
  open: DPAPI is implemented but, like the rest of this project, has never
  been run on real Windows, only cross-compiled; and the registered command
  is still resolved through PATH, so a caller that already controls PATH can
  substitute its own binary for it.

- **M4 -- agent identity, and policy in SQLite.** A decision is now
  `(agent, connector, tool)`, not just `tool`. An agent is a client program,
  identified by the executable the kernel reports for the process that
  spawned the relay -- nothing on the wire names one, and two windows of
  the same program are the same agent. `nim policy deny <tool> [--agent]
  [--connector]` replaces the `config.toml` deny list: rules live in
  `nim_rules`, `config.toml` is refused if it still carries a `[policy]`
  section, and a `rule.add`/`rule.remove` entry is written in the same
  SQLite transaction as the rule it describes. The acceptance test ran
  with real processes: two client programs, enrolled under two names,
  sending the same call to one connector, received different verdicts
  (`TestTwoRealAgentsAgainstOneConnectorReceiveDifferentVerdicts`). D-002
  -- what an unenrolled agent may do -- is answered in
  `docs/decisions/0002-what-an-unknown-agent-may-do.md`: a session no
  enrolment matched meets only the rules that name no agent, the same
  ceiling every session had before agents existed, because a rule could
  only deny.
- **2026-09-14 audit: closed the decision-path bypasses it found.** A
  repeated or case-variant JSON key could put a `tools/call` in front of a
  connector with no decision, no journal entry and no anomaly (F-013):
  objects are now read by exact key, a repeated key is refused, and a call
  with no readable tool name is refused before the daemon is asked.
  `nim connector set` trusted whatever had bound the socket rather than
  verifying it (F-015) -- every management command now dials through the
  same peer-verified path the relay does. A second connection could start
  a session under another's live id (F-014); the console answered to any
  `Host` header, not loopback names only (F-016); a connector could
  outlive a relay asked to stop with SIGTERM or SIGINT (F-020). Smaller
  fixes: `nim log --json` truncated at the read cap and looped on `-n 0`
  (F-019), `nim verify --expect-head` answered nothing with no
  `machine-id`, a `machine-id` with a trailing newline read as tampering,
  and the daemon's own log was lost on a fresh install. Each has a
  regression test that fails against the code as it was.
- **M4.5 -- allow rules, and a stated precedence.** `effect` is now `deny`
  or `allow`; `nim policy allow`, `nim policy default deny|allow`, `nim
  policy remove --default` and `nim policy explain <tool> [--agent]
  [--connector]` round out the CLI. `tool` may be `*`, meaning every tool,
  only through `nim policy default` -- everywhere else it is refused as an
  ordinary name, so there remains exactly one way to write a rule that
  matches more than one tool. Precedence is one function, `journal.Decide`:
  the most specific matching rule wins (an exact tool over a default,
  naming the agent or the connector over not naming it), a tie at equal
  specificity goes to deny, and no matching rule at all is allow -- the M4
  baseline, restated. D-002 is answered again in
  `docs/decisions/0003-allow-rules-and-precedence.md` for a model where a
  rule can grant: `nim policy default deny` plus an agent-scoped `allow`
  is now the allow-list M4's deny-only rules could not express, and an
  unenrolled program is denied by the default rather than merely
  unprivileged by an absent one.
- **`nim init` and `nim doctor`.** `nim init` rewrites Claude Code's,
  Cursor's and Claude Desktop's stdio `mcpServers` entries to route
  through Nim, preserving every other key -- `env` included -- via an
  order-preserving JSON rewrite (`internal/clientconfig`). Dry run by
  default; `--write` backs up the original file before writing it
  atomically, `--undo <backup>` restores one, and `--repoint` re-targets
  an entry already wrapped by a `nim` binary at a different path. `nim
  doctor` is a read-only health check across nine areas -- PATH,
  `config.toml`, the daemon, `machine-id`, the journal, the credential
  store, enrolments, rules and connectors, and every client config `nim
  init` knows about -- exiting 1 only on a hard `FAIL`.
- **Enrolment changes are entries in the chain (F-018, closed).**
  `agent.add` and `agent.remove` are now written by `AddAgent` and
  `RemoveAgent` in the same SQLite transaction as the change to
  `nim_agents`, exactly as `rule.add`/`rule.remove` already were for a
  rule -- closing the gap where re-enrolling a name moved every rule
  scoped to it with nothing in the chain saying so. Schema version 3
  (`canonical_encode_v3`) adds `exec_path` and `exec_id` to the fields a
  journal entry can carry; `nim_journal` and `nim_rules` are each rebuilt
  once, on open, to widen their `CHECK` constraints for the new kind and
  the new `allow` effect, copying every row verbatim -- a test holds both
  rebuilds to that.
- **The console and `nim status` show policy, not just the journal.** A
  Policy tab lists rules, enrolled agents (with `STALE` or `unknown`
  marked per platform) and connectors, served from the same read-only
  journal handle as everything else, over `GET /api/policy`. `nim status`
  gained a policy block -- rule, agent and connector counts, and how many
  enrolments are stale -- reading the same projection, so the CLI and the
  console can never disagree about what they show.
- **A way to get `nim` other than `go build`.** `install.sh` is a POSIX
  `sh` script -- `curl -fsSL .../install.sh | sh` -- that resolves a
  release (or `NIM_VERSION` to pin one), verifies the archive's SHA-256
  against that release's `SHA256SUMS`, and refuses, nothing written, on
  any mismatch; `tools/install-rig/test.sh` proves the refusal actually
  bites, by running the script against both a correct and a deliberately
  wrong checksum. `.github/workflows/release.yml` builds five
  cross-compiled targets, runs the engine's test suite against them first
  (the same checks `ci.yml` runs), and publishes a GitHub Release gated on
  a human pushing a tag matching `v*`; `workflow_dispatch` runs an
  identical dry run that stops short of publishing. `nim version` now
  reports the commit, build time and platform a release binary was built
  for, via `-ldflags -X`, rather than a bare `0.0.0-dev`. The MIT
  `LICENSE` file the README has promised since before this now exists, and
  is copied into every release archive.

### Security

- **A deployment was public despite protection being enabled.** Vercel's
  `ssoProtection.enabled: true` was read and reported as meaning a deployment
  would be team-only. It did not: the scope was `all_except_custom_domains`,
  and Vercel counts a project's own assigned production domain as a custom
  domain, so one of the three intended hostnames served the full dashboard --
  including the "Break Nim" section -- to anonymous requests for about two
  minutes before it was caught by fetching each hostname directly, rather
  than by trusting the setting, and removed (F-011).
- **Deployed again, deliberately public, and audited from outside.** The
  dashboard is meant to be readable by anyone, so protection was turned off
  on purpose and the result was fetched anonymously rather than assumed: all
  three hostnames return byte-identical content matching the local build (by
  hash), every intended security header is present including CSP, and the
  page satisfies its own CSP -- nothing it loads is off-host. `.env`,
  `.git`, `.vercel/project.json`, `vercel.json`, `package.json`,
  `data/state.json`, `generate.mjs`, `nim.db` and node_modules all 404; every
  write method is refused; no source maps exist; and nothing served matches
  a token, key block, or local filesystem path. One fix was needed before
  that held: `vercel build` writes CLI diagnostics
  (`diagnostics/cli_traces.json`) containing the local username, the npm
  cache path and the nvm node version, which are now stripped from the build
  artifact before upload rather than trusted not to be served.
- **Known, not fixed: stale deployments are public too.** Turning off
  deployment protection applies to every deployment the project has ever
  made, not only the current one. Sixteen historical preview URLs and a
  stale branch alias now also answer publicly -- fourteen show Vercel's
  generic failure page, one shows an abandoned placeholder -- and nothing
  sensitive is in any of them, but "only the dashboard is public" is not
  literally true until they are deleted. Deleting deployments is destructive
  and this repository does not make that call on its own (F-012, open).

No version has been tagged yet, so there is nothing to report under Fixed,
Deprecated or Removed relative to a prior release.
