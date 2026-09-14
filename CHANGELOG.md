# Changelog

All notable changes to this project are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

No version has ever been tagged. `[Unreleased]` describes everything built so
far on `m1-bootstrap`; there is no prior release for it to diff against, so
read it as a status report rather than a list of deltas. `docs/milestones.md`
is the source of truth this section summarizes, and is where the detail and
the test names behind each claim live.

## [Unreleased]

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

### Changed

- **M4 reordered, and not yet built.** The milestone was "Grants (Cedar)"; it
  is now agent identity, followed by moving the `config.toml` deny list into
  SQLite -- a policy language has nothing to talk about while every decision
  is still global per tool. Neither step exists in the engine yet: every
  decision today is still `(connector, tool)`, with no notion of which agent
  asked, and the deny list can still be emptied by anything able to write
  `config.toml`.

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
