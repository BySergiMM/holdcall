# Holdcall

**[holdcall.vercel.app](https://holdcall.vercel.app)** — the product page,
and [/status](https://holdcall.vercel.app/status/) for what is verified.

![The console: the overview of a journal, a call arriving held for a human, its real arguments including a bidirectional override shown as an escape, and the journal after the rejection with the entry's place in the hash chain](docs/images/console.gif)

Holdcall is a local process that sits between an MCP client — Claude Code, Cursor,
Claude Desktop — and the MCP servers it spawns. It holds each server's
credentials, decides whether every `tools/call` is allowed before it reaches
a server, and records the decision in an append-only, hash-chained journal.
Nothing else in the protocol is touched: everything that is not a
`tools/call` is forwarded byte for byte.

It is for a developer who already runs one of those clients with MCP servers
configured and wants three things none of them give you on their own: a
record of what a tool was asked to do, a way to refuse one by name before it
runs, and each server's credential kept out of a client's plaintext config
and away from the servers it isn't registered for. It is pre-alpha — see
Status below for exactly what that means before you point it at something
you cannot afford to have go wrong.

## Status

Pre-alpha, and honest about it. What works:

- **A relay a real client cannot distinguish from a direct connection.** Tool
  list, schemas, unicode, a 512 KiB payload, error propagation and ping all
  pass through unchanged, and the two things that must differ — a refused
  call, a frame Holdcall will not read — differ exactly as documented
  (`tools/relay-rig`, re-run against the current binary on 2026-09-15 under
  both the `initialize` and the `server/discover` handshake, and now a CI
  job).
- **Refusal.** A `tools/call` is decided before it is forwarded, and the
  decision is written to the journal before the relay acts on it. A refused
  call never leaves Holdcall.
- **An append-only, hash-chained record** (`holdcall log`, `holdcall verify`, `holdcall
  console`), and **credentials out of the client's config file**, into the
  OS credential store, injected only into the one server each is registered
  for.
- **Agent identity, derived rather than declared**, from the executable the
  kernel reports for the process that spawned the relay; nothing on the wire
  names an agent.
- **Policy in SQLite**, per agent and per connector, with a stated precedence
  between `allow` and `deny`. Every change is an entry in the journal;
  `config.toml` carries no policy at all — a file that still does is refused.
- **Budgets.** A cap on how many *allowed* calls one session may make,
  scoped like a rule and checked only once the rules have allowed the call —
  a budget narrows, never grants. Decremented at authorization time: a call
  the relay gave up on still spent its share, a refusal never does, and a
  restarted relay is a new session with a fresh count.
- **Human approval.** A third rule effect, `ask`, holds a call in the
  daemon's memory until `holdcall approve` or `holdcall reject` decides it, or nobody
  does and it is rejected. `holdcall approve` shows the call's real parameters,
  every byte as itself, never a model-written summary; the decision is in
  the journal before the relay acts on it.
- **`holdcall init` and `holdcall doctor`** — pointing a client at Holdcall, and checking
  the result, without hand-editing JSON.

`docs/milestones.md` is the order the rest comes in; see *What does not
exist yet* below.

## Failing closed

Every way of not getting a decision is a denial: no daemon, a slow daemon, a
closed socket, an answer to a different question, a journal write that failed.
There is no path on which a `tools/call` reaches a server without a decision
behind it.

The one thing that is deliberately *not* a denial is the absence of
configuration. A connector nobody set up is not a failure, and those servers
keep working untouched. `docs/decisions/0001-failure-behaviour.md` sets out
why those are different questions.

## Install

**From source.** Go 1.26.6 or newer; see *Building* for the rest:

```bash
git clone https://github.com/BySergiMM/holdcall.git && cd holdcall/engine
go build -o bin/holdcall ./cmd/holdcall && ./bin/holdcall version
```

**Had it installed as `nim`?** The product was renamed on 2026-09-25 and
nothing is migrated on its own: stop the daemon, move
`~/Library/Application Support/nim` to `.../holdcall` and rename `data/nim.db`
to `data/holdcall.db` inside it to keep the journal, rules and enrolments, run
`holdcall init --write` so client configs spawn the new binary, and set each
connector's credential again, since the keychain items were stored under the
old name. `CHANGELOG.md` has the full list.

**From a release.** `v0.1.0` exists, built by hand on macOS on 2026-09-26;
`CHANGELOG.md` says which archive was actually executed. While the repository
is private the installer cannot fetch it anonymously, so take the archive from
the release page with your GitHub session; once the repository is public this
is the line:

```bash
curl -fsSL https://raw.githubusercontent.com/BySergiMM/holdcall/m1-bootstrap/install.sh | sh
```

POSIX `sh`, never `sudo`, writes only inside `HOLDCALL_INSTALL_DIR` (default
`$HOME/.local/bin`). Resolves the latest GitHub release, or
`HOLDCALL_VERSION=vX.Y.Z` to pin one, checks the archive's SHA-256 against that
release's `SHA256SUMS`, and refuses — nothing written — on any mismatch.
Windows has no `sh`: take the `.zip` from the release page.

A release binary adds the ldflags that make `holdcall version` report something
other than `0.0.0-dev`, the same ones `.github/workflows/release.yml` uses
per target:

```bash
go build -trimpath -ldflags "-s -w -X main.version=$TAG \
    -X main.commit=$(git rev-parse HEAD) -X main.builtAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o bin/holdcall ./cmd/holdcall
```

**After upgrading**, if a daemon from the old binary is still running: a
replaced binary and a running daemon become different images, so they refuse
each other (`holdcall status` says `daemon   OLDER BUILD`). Run `holdcall daemon
restart` — it asks the old daemon to exit and starts a new one from the
binary now on disk, then confirms the new one answers before returning.

## Five minutes to a first decision

The full walkthrough, with before/after config examples per client, is
`docs/getting-started.md`. Short version:

```bash
holdcall init                # dry run: shows what it would rewrite, touches nothing
holdcall init --write        # applies it, after backing up every file it changes
holdcall init --undo <backup path holdcall init --write printed>
```
Finds Claude Code's, Cursor's and Claude Desktop's config files and rewrites
each stdio server to route through Holdcall; `--undo` restores one file from its
backup.

```bash
holdcall doctor
```
One line per check — PATH, `config.toml`, the daemon, the journal, the
credential store, enrolments, rules, connectors, client configs — `OK`,
`WARN` or `FAIL`, with a remedy. Exits 1 only on a `FAIL`.

```bash
holdcall policy deny <tool-name>    # recorded as a rule.add entry
```
Ask your client to call that tool: it comes back as a tool error, because
the call never reached the server that would have run it.

```bash
holdcall log        # shows the call: deny, no result
holdcall console    # the same journal, plus held calls, sessions and policy, at 127.0.0.1:7717
```

![The console's Journal view over a demo journal: every entry in chain order with its kind, tool, decision, agent and connector, and entry 22, a rejected call, open beside it with its prev_hash and hash](docs/images/console.png)

The console is read-only and serves loopback only. It has five views:
an overview of the record (chain check, decisions, detectable gaps, the
policy in effect), the calls **held** for a human with their real
arguments and the `holdcall approve` / `holdcall reject` lines to copy, the
**journal** with filters and a detail drawer showing each entry's place in
the chain, **sessions** with a timeline each, and the **policy** with an
"explain a call" form that runs the daemon's own decision function. Deciding
stays on the command line on purpose: a page any other page on this machine
can reach must not be able to approve. `/#held`, `/#journal/22` and
`/#sessions/<id>` open a view, an entry or a session directly.

![The console's Held view: one call to dangerous_tool held for a human, with the agent, connector, its real arguments including a bidirectional override shown as an escape, and the approve and reject commands to copy](docs/images/console-held.png)

## Policy

A rule is `deny`, `allow` or `ask`, scoped to a tool and, optionally, one
enrolled agent and one connector:

```bash
holdcall policy deny delete_repository                        # for everyone
holdcall policy deny delete_repository --agent cursor         # for one client program
holdcall policy allow force_push --agent claude-code --connector github
holdcall policy default deny                                  # every tool, unless something more specific says otherwise
holdcall policy remove delete_repository                       # or --default
holdcall policy list
holdcall policy explain force_push --agent claude-code --connector github
holdcall policy ask delete_branch --connector github               # hold it for a human
holdcall approve                                                    # what is held, with its real arguments
holdcall approve <id>                                               # or: holdcall reject <id> --reason "not that branch"
holdcall policy budget 20 --tool list_repos --agent claude-code    # at most 20 allowed calls per session
holdcall policy budget 100 --all-tools --connector github          # every tool on one connector, per session
holdcall policy budget remove --tool list_repos --agent claude-code
```

**Precedence, in one sentence:** the most specific matching rule wins — an
exact tool beats a default, naming the agent or the connector beats not
naming it — a tie at equal specificity goes to deny, then ask, then allow,
and no matching rule at all is allow.

**What an unenrolled program may do, in one sentence:** a session no
enrolment matched is bound only by the rules that name no agent, so a plain
`deny` is a ceiling nothing escapes by staying unenrolled, and `holdcall policy
default deny` plus an agent-scoped `allow` is the allow-list that ceiling
alone could not express — `docs/decisions/0002-what-an-unknown-agent-may-do.md`
and `docs/decisions/0003-allow-rules-and-precedence.md` argue both halves.

Every rule added or removed is a `rule.add` or `rule.remove` entry in the
chain, in the same transaction as the rule; a budget is a `budget.add` or
`budget.remove` entry the same way. A budget never grants — it only lowers
what the rules already allow, per session (`docs/decisions/0004-budgets.md`).

## Agents

```bash
holdcall agent add cursor /Applications/Cursor.app/Contents/MacOS/Cursor
holdcall agent list
holdcall agent remove cursor
```

An agent is a client program, enrolled by the executable it runs — the file
the kernel reports for the process that spawned the relay, matched against
enrolments made this way; nothing on the wire names an agent. Two windows of
the same program are the same agent, because they run the same file:
enrolment identifies an executable, not a running instance. Re-enrolling a
name against a different path moves what that name means, including every
rule scoped to it. Enrolling one is as privileged as running Holdcall itself —
the journal, not the enrolment, is what makes that visible afterwards.

## Credentials

A connector names the one downstream server its secret may be injected into.
That binding is what authorizes the credential, so it is required:

```bash
echo "$GITHUB_TOKEN" | holdcall connector set github --env GITHUB_TOKEN \
    -- npx -y @modelcontextprotocol/server-github

holdcall serve --connector github        # runs the registered server, with the token
```

The secret is never a command-line argument and never reaches SQLite. A command
given on the command line is ignored when a connector supplies one: the daemon
decides what receives a credential, not the caller.

Both ends of the daemon socket verify each other by peer identity, so neither
a process pretending to be a shim nor one pretending to be the daemon gets in.

## What the record is, and is not

The journal is append-only and hash-chained, and `docs/journal-format.md` is
the normative definition of the encoding, the chain, and — importantly — what
the chain does not protect against.

The short version: it detects corruption, partial writes and edits made by
anything that does not know the chain exists. It does **not** detect a local
attacker who can write to `holdcall.db`, because nothing in the construction is
secret and the whole chain can be recomputed. Only a head recorded elsewhere
(`holdcall verify --expect-head`) covers that.

Do not describe it as tamper-proof, tamper-evident, or an immutable audit log.

Policy and enrolment changes are entries in the chain too, not a separate
record with weaker guarantees: `rule.add`, `rule.remove`, `agent.add`,
`agent.remove`, `budget.add` and `budget.remove` are each written in the same
SQLite transaction as the change they describe, so the rules, the budgets and
the enrolments they are scoped to have a history that verifies exactly like
the calls do.

## What does not exist yet

- **A hosted journal viewer.** M8, now planned rather than blocked: D-004
  decided on 2026-09-16 that the mirror presents what this machine
  reported, labelled as such on every row, and holds pinned heads apart from
  synced rows (`docs/decisions/0006-what-the-mirror-may-claim.md`). It needs
  a hosting account, which is the operator's to provide.
- **Conditions on a call's arguments, or on time.** A rule matches `(agent,
  connector, tool)` and nothing else.
- **Anything run on Windows.** DPAPI credential storage and all five
  cross-compiled targets exist, but no code here has ever executed on a
  real Windows machine — see *Building* and `docs/security.md`'s *Known
  gaps* table.

`docs/milestones.md` has the test names behind every claim above, and
`docs/architecture.md` says where each layer lives and what one call goes
through.

## Shape

```
engine/      Go. The daemon, the shim, the journal and the console.
dashboard/   A static page of what is true about this repository, built from it.
docs/        Architecture, security posture, decisions, and the journal's format.
supabase/    The schema a journal mirror would land in. Nothing writes to it.
tools/       The relay rig: a real MCP client, run direct and through Holdcall.
```

The engine keeps working with no network. `dashboard/` is deployed publicly
(D-003): the product page at the root and the status page at `/status/`, both
built from this repository and showing no runtime state; the hosted journal
viewer is M8 and does not exist.

## Building

```bash
cd engine && go build -o bin/holdcall ./cmd/holdcall
```

Go 1.26.6 or newer, which the `go` directive fetches on demand. No C
toolchain: the SQLite driver is pure Go, so the engine cross-compiles for
darwin, linux and windows with `CGO_ENABLED=0`.

The whole suite runs on real Linux and macOS runners in CI and passes on
both, since 2026-09-26. Windows executes the same suite and fails 52 tests
(F-029 on the status page says which and why); its job does not decide the
run until that is fixed, and two of the security properties are known not
to hold there regardless — see the *Known gaps* table in `docs/security.md`.

### Running CI without GitHub's runners

`.github/workflows/ci.yml` is the source of truth, and `tools/ci-local.sh`
runs the same jobs with the same commands and assertions on the machine it
is started on: format, vet, tidy, `go test -race -shuffle -v` with the
evidence script, the five cross-compiles, govulncheck, the dashboard's
check and build, and the relay rig. `tools/ci-linux.sh` runs the Linux leg
inside a local [Lima](https://lima-vm.io) virtual machine, which is the only
way `peer_linux.go` and the Linux credential store get executed when the
hosted runners are not available. Neither can be Windows.

```bash
tools/ci-local.sh              # every job, here
tools/ci-local.sh test vuln    # a subset
tools/ci-linux.sh              # the same, in an Ubuntu VM (needs limactl)
```

## Performance

`docs/benchmarks.md`, with the commands that reproduce every figure. The
decision a call waits for costs p99 0.27 ms including the durable write.

## Where the project actually stands

`dashboard/` builds a page listing every guarantee alongside what it does *not*
guarantee, every attack anyone has tried against Holdcall including the ones that
worked, and every known weakness. It is generated from this repository and
refuses to build if a claim cites a test that does not exist — which is how it
avoids becoming another document that drifts from the code. `docs/dashboard.md`
explains the design and, more usefully, what it deliberately cannot show.

    cd dashboard && npm install && npm run check && npm run dev

It shows no runtime state and never will: the journal, sessions, enrolled
agents and rules live on the machine running Holdcall. `holdcall console` serves the
journal and its sessions over loopback; `holdcall agent list` and `holdcall policy list`
show the rest.

## Licence

MIT.
