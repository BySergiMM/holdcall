# Changelog

All notable changes to this project are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

`0.1.0` was the first tagged version and describes everything built so far
on `m1-bootstrap`; there is no prior release for it to diff against, so read
it as a status report rather than a list of deltas. `0.1.1` is the first
release the workflow itself built. `docs/milestones.md` is the
source of truth this section summarizes, and is where the detail and the
test names behind each claim live.

## [Unreleased]

Nothing since 0.1.1.

## [0.1.1] - 2026-09-26

Built and published by `.github/workflows/release.yml` on GitHub's runners,
the first release that was: the repository is public since 2026-09-26 and
the runners can start. The suite ran on ubuntu-latest before the build; the
same commit passed on macos-latest in CI. Every archive comes from the same
commit; darwin/arm64 is also the build the maintainer runs. Windows archives
are still cross-compiled from a tree whose tests fail there (F-029).

### Changed

- **Linux is executed, not just compiled.** The repository went public on
  2026-09-26, GitHub's runners started, and the whole suite, end-to-end
  relay tests included, passes on ubuntu-latest and macos-latest. Windows
  ran for the first time the same day and fails 52 tests (F-029); its job
  no longer decides the run until those are fixed.

### Fixed

- **A daemon upgraded before its first peer check no longer accepts the
  new build as itself (F-026).** On macOS the daemon resolved its own
  image lazily, on the first checkable connection; rebuilt in place before
  that, it fell back to the path comparison for good and served the new
  build's relays silently, with no F-001 diagnosis. It resolves its image
  at startup now, before listening. Found through a test that flaked for
  exactly this reason; the test also keeps each probe alive until the old
  daemon has looked at it.
- **The daemon's tests create their own machine-id (F-027, second part).**
  Tests that enrol through the journal before the daemon has created the
  identifier lost that race on CI; the helpers now create it first.
- **CI's test job runs in `engine/` on every platform.** A job-level
  `defaults.run` replaces the workflow-level one, so the working directory
  was lost; the first run on a real runner failed on `go vet` at the root,
  and the Windows pre-checkout step must not use it.
- **`install.sh` no longer overwrites the binary in place (F-028).** On
  macOS that left a `holdcall` the kernel killed on every exec once a daemon
  from the previous build was running. The installer now copies beside the
  target and renames over it, and says to run `holdcall daemon restart`.

## [0.1.0] - 2026-09-26

Built and published by hand on macOS, because GitHub Actions could not run
on this account that week; `.github/workflows/release.yml` is the intended
path and was not used. The darwin/arm64 binary is the one the whole suite
ran against. The other four archives (darwin/amd64, linux/amd64, linux/arm64
and the windows/amd64 zip) are cross-compiled from the same commit and were
never executed: the platform claims in `docs/milestones.md` stand as they
are. The repository is private, so `install.sh` cannot fetch this release
anonymously yet.

### Changed

- **Nim is now Holdcall.** The name collided with the Nim programming
  language, and "holdcall" says what the product does: it holds the call
  until a human decides. Everything a user sees follows: the binary and
  command are `holdcall`, the home is `HOLDCALL_HOME` or
  `~/Library/Application Support/holdcall` (and the `holdcall` directory
  under XDG or AppData elsewhere), the daemon's socket and the keychain
  service carry the new prefix, the Go module is
  `github.com/BySergiMM/holdcall/engine`, release archives are
  `holdcall_<tag>_<os>_<arch>`, and the repository, the site and the docs
  are renamed. The on-disk format is unchanged: the journal is still schema
  v4 and its tables keep the `nim_` prefix, as does the mirror schema under
  `supabase/`, so an existing journal keeps verifying. An install made under
  the old name is not migrated automatically: stop the daemon, move
  `~/Library/Application Support/nim` to `.../holdcall` and rename
  `data/nim.db` to `data/holdcall.db` inside it to keep the journal, rules
  and enrolments, re-run `holdcall init --write` so client configs
  spawn the new binary, and set each connector's credential again with
  `holdcall connector set`, because the keychain items were stored under
  the old service name.
- **The console was redesigned around the calls that need a human.** One
  page, light and dark, with five views. *Overview*: the chain check, the
  decisions this page has read, the newest calls, the gaps the record can
  detect in itself and the policy in effect. *Held*: every call an `ask`
  rule stopped, read from the daemon over the verified socket, with its
  real arguments (bidirectional overrides and control characters shown as
  escapes) and the `holdcall approve` / `holdcall reject` lines to copy; a strip at
  the top says how many are waiting from any view. *Journal*: every entry
  as it is written, with search and kind/decision filters, agent and
  connector joined from the session, and a drawer showing an entry's
  `prev_hash` and `hash`. *Sessions*: a timeline per session. *Policy*:
  rules, budgets, agents and connectors, and an "explain a call" form that
  runs the daemon's own decision function and names the deciding rule and
  the budgets that would apply. `#journal/<chain_seq>` and
  `#sessions/<id>` open an entry or a session directly. Two read-only
  routes were added for this, `/api/explain` and `/api/pending`; there is
  still no route that writes, and approving stays on the CLI on purpose.
  The page is served with `Cache-Control: no-store`, so a browser never
  drives a daemon with a page from a previous build.

### Fixed

- **The daemon's tests no longer reach the real install (F-027).** The
  in-process daemon tests gave each daemon a temporary data directory but
  not a home, so the machine-id was read from, and on an empty journal
  written to, the operator's own `Application Support` directory. Both
  helpers now set `HOLDCALL_HOME`, and a `TestMain` points the package at a
  temporary home before any test runs.
- **Holdcall's refusals now follow the handshake the session negotiated (F-021).**
  A connector that speaks the 2026-07-28 revision is negotiated through
  `server/discover` rather than `initialize`, and every result on that
  revision must name its `resultType`. Holdcall relayed such sessions byte for byte
  but wrote its own refusals in the older shape, so a client on that revision
  (fastmcp 4 in its default mode) raised a local validation error instead of
  reading "Holdcall denied this call". The relay now watches `server/discover` as
  it watches `initialize`, records the negotiated version on `session.end`
  for those sessions too, and writes a refusal with `resultType: "complete"`
  when the version requires it; older clients get the older shape unchanged.
  The relay rig runs its denial case under both handshakes.
- **An in-place upgrade is diagnosed instead of read as an impostor (F-001).**
  Replacing the `holdcall` binary while its daemon kept running made the old
  daemon and every new client compare as different images, correctly, by
  the same check that catches a real impostor, but with no way to tell the
  two apart: the daemon logged an unverified peer and the client saw "not
  Holdcall". `peer.Diagnose` now tells a peer running a different file at exactly
  our own launch path apart from a program at another path. `holdcall status`
  prints `daemon   OLDER BUILD -- run holdcall daemon restart`, `holdcall doctor` fails
  a `daemon build` check the same way, and `holdcall daemon restart` is the
  remedy: it signals the old daemon, which now shuts down cleanly, closing
  its journal and removing its socket file, then starts a new one and
  confirms it answers. Not supported on Windows. The trust boundary does not
  move.
- **A held call cannot be approved before its arguments have arrived.** The
  M6 review found that `holdcall approve <id>` could be recorded and the call
  forwarded in the moment between the daemon holding a call and the relay's
  report of its arguments reaching it, and that `holdcall approve` showed such a
  call as one with no arguments. The daemon now refuses an approve until the
  report has arrived (a reject goes through regardless), ignores a second
  report for the same call, and `holdcall approve` says "not received yet" for the
  one and "(none)" for the other. An `Event` formats without its arguments,
  the guard `Request` and `Response` already had.
- **Budgets are shown wherever the policy is shown.** The M5 review found
  that `holdcall policy list` showed budgets while `holdcall status` and the console's
  Policy tab, which read the same projection, did not, so an operator reading
  either would have believed a session unbounded that was not. The read model
  carries budgets now, and a scope-less budget request is refused in a
  budget's words rather than a rule's.
- **`holdcall status` verifies that what answers on the socket is Holdcall.** It used
  to report `running` for whatever was bound there, so the impostor of
  `impostor_test.go` -- any process that binds the path first -- read as the
  daemon on the one line an operator trusts. It now performs the verification
  every relay does and says `NOT HOLDCALL` when it fails. The bare connect-and-close
  it used before also left a connection the daemon logged as an unverified
  peer on every `holdcall status`; that line is gone with it.
- **Four findings from the 2026-09-15 review of the merged M4.5 work, closed
  before it shipped.** Two names could be enrolled against one executable and
  the lookup between them had no order, so a same-user process that enrolled a
  second name against the operator's client binary would have inherited that
  name's rules after a routine re-enrolment; one executable is one agent now.
  `holdcall init`'s dry run printed the `env` block where client configs keep their
  tokens; values are hidden. Two `--write` runs in one second overwrote the
  first backup; names carry nanoseconds and are created exclusively, and a
  symlinked config is written through rather than replaced. The relay could
  wait on its connector from two goroutines at once; it stops it once.

### Added

- **Console tabs have URLs.** `/#journal`, `/#sessions` and `/#policy` open a
  tab directly and survive a reload; the rules header now says what rules
  are, allowed and denied, rather than what they were on M4.

- **M1 -- pass-through relay.** `holdcall serve` spawns one downstream MCP server
  and relays stdio both ways, forwarding every byte unmodified except for
  recognising `tools/call` and reporting it to the daemon. Verified against a
  real MCP client: direct and relayed runs were identical on tool list,
  schemas, unicode, a 512 KiB payload, error propagation and ping
  (`tools/relay-rig`).
- **M1.5 -- an append-only, hash-chained journal.** Every call is two
  immutable entries (`call.request`, `call.outcome`) rather than one row
  updated in place; `holdcall log`, `holdcall verify` and `holdcall status` all read the
  same projection. The chain detects corruption and edits that did not
  recompute it. It does **not** detect a local attacker who can write to
  `holdcall.db`: nothing in the chain is secret, so it can be recomputed and a
  shorter chain verifies just as cleanly -- only a head recorded elsewhere
  (`holdcall verify --expect-head`) closes that. Detectable gaps (dropped events,
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
  the same program are the same agent. `holdcall policy deny <tool> [--agent]
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
  `holdcall connector set` trusted whatever had bound the socket rather than
  verifying it (F-015) -- every management command now dials through the
  same peer-verified path the relay does. A second connection could start
  a session under another's live id (F-014); the console answered to any
  `Host` header, not loopback names only (F-016); a connector could
  outlive a relay asked to stop with SIGTERM or SIGINT (F-020). Smaller
  fixes: `holdcall log --json` truncated at the read cap and looped on `-n 0`
  (F-019), `holdcall verify --expect-head` answered nothing with no
  `machine-id`, a `machine-id` with a trailing newline read as tampering,
  and the daemon's own log was lost on a fresh install. Each has a
  regression test that fails against the code as it was.
- **M4.5 -- allow rules, and a stated precedence.** `effect` is now `deny`
  or `allow`; `holdcall policy allow`, `holdcall policy default deny|allow`, `holdcall
  policy remove --default` and `holdcall policy explain <tool> [--agent]
  [--connector]` round out the CLI. `tool` may be `*`, meaning every tool,
  only through `holdcall policy default` -- everywhere else it is refused as an
  ordinary name, so there remains exactly one way to write a rule that
  matches more than one tool. Precedence is one function, `journal.Decide`:
  the most specific matching rule wins (an exact tool over a default,
  naming the agent or the connector over not naming it), a tie at equal
  specificity goes to deny, and no matching rule at all is allow -- the M4
  baseline, restated. D-002 is answered again in
  `docs/decisions/0003-allow-rules-and-precedence.md` for a model where a
  rule can grant: `holdcall policy default deny` plus an agent-scoped `allow`
  is now the allow-list M4's deny-only rules could not express, and an
  unenrolled program is denied by the default rather than merely
  unprivileged by an absent one.
- **M5 -- budgets.** A budget caps the number of *allowed* calls one session
  may make, scoped like a rule (`agent`, `connector`, a tool or `--all-tools`)
  and checked only once the rules have allowed the call, so it narrows and
  never grants. The count is of `allow`/`approved` decisions, never outcomes:
  a call the relay gave up on still spent its share and a refusal never does;
  a session is one relay run, so a restarted relay starts fresh. `holdcall policy
  budget <n> --tool <tool>|--all-tools [--agent] [--connector]`, `holdcall policy
  budget remove`, a BUDGETS table in `holdcall policy list`, and the matching
  budgets in `holdcall policy explain`. `nim_budgets` sits outside the chain like
  `nim_rules`, every change one transaction with a `budget.add` or
  `budget.remove` entry, which needed schema version 4 (`canonical_encode_v4`,
  field 20 `budget_calls`); existing databases are rebuilt on open.
  `docs/decisions/0004-budgets.md` argues what counts and why.
- **M6 -- human approval.** A third rule effect, `ask` (`holdcall policy ask`,
  `holdcall policy default ask`; deny beats ask beats allow at equal specificity),
  holds a call for a human instead of deciding it from a rule alone. The
  daemon answers `pending` and keeps the call -- tool, agent, connector,
  digest and its real arguments -- in memory only; the relay sends the real
  bytes once, only when told the call is held, and waits up to
  `[daemon] approval_timeout` (default `2m`). `holdcall approve` lists what is held
  with those arguments, pretty-printed with every byte shown as itself, never
  a summary; `holdcall approve <id>` and `holdcall reject <id> [--reason]` decide one,
  writing `approved` or `rejected` to the journal before the relay is told,
  the ordering allow and deny already keep; on approval the relay forwards
  the original frame byte for byte. Nobody deciding in time, a session
  ending, or its connection dropping all reject and journal the call, so none
  is left neither approved nor rejected. The human's reason is never
  journaled. `docs/decisions/0005-human-approval.md` says plainly that this
  defends against the model driving the client, not against the operator:
  anyone who can run `holdcall` as this user can approve a call.
- **`holdcall init` and `holdcall doctor`.** `holdcall init` rewrites Claude Code's,
  Cursor's and Claude Desktop's stdio `mcpServers` entries to route
  through Holdcall, preserving every other key -- `env` included -- via an
  order-preserving JSON rewrite (`internal/clientconfig`). Dry run by
  default; `--write` backs up the original file before writing it
  atomically, `--undo <backup>` restores one, and `--repoint` re-targets
  an entry already wrapped by a `holdcall` binary at a different path. `holdcall
  doctor` is a read-only health check across nine areas -- PATH,
  `config.toml`, the daemon, `machine-id`, the journal, the credential
  store, enrolments, rules and connectors, and every client config `holdcall
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
- **The console and `holdcall status` show policy, not just the journal.** A
  Policy tab lists rules, enrolled agents (with `STALE` or `unknown`
  marked per platform) and connectors, served from the same read-only
  journal handle as everything else, over `GET /api/policy`. `holdcall status`
  gained a policy block -- rule, agent and connector counts, and how many
  enrolments are stale -- reading the same projection, so the CLI and the
  console can never disagree about what they show.
- **A way to get `holdcall` other than `go build`.** `install.sh` is a POSIX
  `sh` script -- `curl -fsSL .../install.sh | sh` -- that resolves a
  release (or `HOLDCALL_VERSION` to pin one), verifies the archive's SHA-256
  against that release's `SHA256SUMS`, and refuses, nothing written, on
  any mismatch; `tools/install-rig/test.sh` proves the refusal actually
  bites, by running the script against both a correct and a deliberately
  wrong checksum. `.github/workflows/release.yml` builds five
  cross-compiled targets, runs the engine's test suite against them first
  (the same checks `ci.yml` runs), and publishes a GitHub Release gated on
  a human pushing a tag matching `v*`; `workflow_dispatch` runs an
  identical dry run that stops short of publishing. `holdcall version` now
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
  including the "Break Holdcall" section -- to anonymous requests for about two
  minutes before it was caught by fetching each hostname directly, rather
  than by trusting the setting, and removed (F-011).
- **Deployed again, deliberately public, and audited from outside.** The
  dashboard is meant to be readable by anyone, so protection was turned off
  on purpose and the result was fetched anonymously rather than assumed: all
  three hostnames return byte-identical content matching the local build (by
  hash), every intended security header is present including CSP, and the
  page satisfies its own CSP -- nothing it loads is off-host. `.env`,
  `.git`, `.vercel/project.json`, `vercel.json`, `package.json`,
  `data/state.json`, `generate.mjs`, `holdcall.db` and node_modules all 404; every
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
