# Security posture

What Nim protects, what it does not, and what has actually been tried against
it. Every attack below was run against a real build; none is theoretical.

Read `docs/journal-format.md` for the record's own limits and
`docs/decisions/0001-failure-behaviour.md` for what happens when Nim cannot
decide.

## The guarantee

> A `tools/call` that reached a connector is a call the journal recorded, with
> the decision that allowed it.

One direction only. **An `allow` in the journal is not evidence the call was
made**: the relay gives up after 2 s and SQLite's busy timeout is 5 s, so a
pathologically contended write can land after the relay has already denied.
Such a call reads as `pending`, never as executed.

## Threat model

The adversary is **a downstream MCP server**: code the operator did not write,
running as the same user, spawned by Nim itself. Everything below is written
against that.

Not in the model, and worth being explicit about:

- **A user who reconfigures their client.** Nothing stops a client being
  pointed straight at an MCP server instead of at Nim. No in-process shim can
  stop that. Nim's guarantee is about calls that go through it; making
  connectors unreachable except through Nim needs a network or sandbox
  boundary.
- **Root, or another user with debugger access to the daemon.**
- **A local attacker who can write to `nim.db`.** The chain is unkeyed —
  nothing in it is secret, so anyone able to edit a row can recompute every
  hash from the genesis and the result verifies cleanly. `nim verify
  --expect-head <hash>` is the only thing that catches it, and only for what
  was committed before the head you recorded.

## What peer identity establishes, per platform

Peer identity asks the kernel who is on the other end of a socket and whether
it is running the same file we are. The question that decides its strength is
whether the answer describes a **running image** or a **filename**.

| | How the peer's image is resolved | Resists path swap |
|---|---|---|
| **Linux** | `stat("/proc/<pid>/exe")` — the kernel resolves the magic link to the inode the process is executing | yes |
| **Darwin** | the vnode behind the peer's own mapping of its main image, via `proc_info`'s `PROC_PIDREGIONPATHINFO` | yes |
| **Windows** | nothing — AF_UNIX exposes no peer-credential API | n/a, unsupported |

Both unix implementations previously stat'ed a *path* (`kern.procargs2` on
darwin, `readlink /proc/<pid>/exe` on linux) and were defeated with no race at
all: launch from a path you own, replace the file there with a link to the nim
binary, then connect. `internal/peer/pathswap_test.go` is that attack, and it
runs on both platforms.

**What this is not.** It is executable identity, kernel-provided — not
code-signing identity. It says nothing about who signed a binary, and a
rebuild of Nim is a different inode and therefore a different program as far as
this is concerned. It also does not distinguish two processes running the same
file, which is why session ownership exists.

**Darwin specifics.** `PROC_PIDREGIONPATHINFO` at address 0 returns the
process's lowest mapped region, which is its main image: `__PAGEZERO` sits
below it and an attempt to place a file-backed executable mapping underneath
was rejected by the kernel at every address tried, so a peer cannot relocate
what this reads. The flavor and every struct field come from the installed SDK
header; the one value taken from XNU rather than the SDK is the `proc_info`
call number, which Apple does not ship. That is why the implementation first
resolves *its own* pid and requires the answer to be its own binary before
trusting the mechanism at all — if a future macOS moves a field, the check
fails against something already known and falls back to the old path
comparison rather than comparing garbage.

`SecCodeCopyGuestWithAttributes` would give a stronger, signature-based
identity, but requires Security.framework and therefore cgo, which would end
the `CGO_ENABLED=0` cross-compilation this project relies on. It was not
needed: the vnode check answers the question actually being asked.

## Attacks run against the current build

| # | Attack | Result | Covered by |
|---|---|---|---|
| 1 | Name a connector, supply your own command, read the credential | **blocked** | `credential_binding_test.go`, `spawn_test.go` |
| 2 | Ask for a credential from a process that is not Nim | **blocked** | `peer_authz_test.go` |
| 3 | Write forged entries to the journal from a non-Nim process | **blocked** | `peer_authz_test.go` |
| 4 | Obtain a decision from a non-Nim process | **blocked** | `peer_authz_test.go` |
| 5 | Report calls under a session another connection opened | **blocked** | `peer_authz_test.go` |
| 6 | Bind the socket first and impersonate the daemon | **blocked** | `impostor_test.go` |
| 7 | As the impostor, choose the command a credential is injected into | **blocked** | `impostor_test.go` |
| 8 | As the impostor, harvest tool names and argument digests | **blocked** | `impostor_test.go` |
| 9 | Exhaust daemon memory with an unterminated line | **blocked** (1 MiB cap) | `peer_authz_test.go` |
| 10 | Bypass the rules (until M4, the deny list) | **blocked** | `decide_test.go`, `enforce_test.go` |
| 11 | Smuggle a `tools/call` inside a JSON-RPC batch | **blocked** (batch refused whole) | `enforce_test.go` |
| 12 | Smuggle a call in a frame Go cannot parse but the server can | **blocked** (frame refused) | `enforce_test.go` |
| 13 | Kill the daemon to disable enforcement | **blocked** (denies) | `enforce_test.go` |
| 14 | Truncate the journal's tail | **detected** only with `--expect-head` | `journal_test.go` |
| 15 | Alter an entry in place | **detected** | `journal_test.go` |
| 16 | Leak a credential into logs, the journal, or stderr | **none found** | manual, re-run each audit |
| 17 | Leak call arguments into the journal | **none found** (digest only) | `journal_test.go` |
| 18 | Defeat peer identity by swapping the file at the peer's launch path | **blocked** (linux and darwin) | `pathswap_test.go` |
| 19 | Pass `--expect-head` a head the journal has grown past | **no longer misreported** | `journal_test.go` |
| 20 | Smuggle a `tools/call` behind a repeated or case-variant JSON key | **blocked** (refused, anomaly recorded) | `mcp_test.go`, `enforce_test.go` |
| 21 | Name one tool to the daemon and another to the server | **blocked** (exact keys; unreadable call refused) | `enforce_test.go` |
| 22 | Start a session under an id another connection opened | **blocked** (daemon-wide, for all time) | `decide_test.go` |
| 23 | Bind the socket first and receive the secret from `nim connector set` | **blocked** | `impostor_test.go` |
| 24 | Change the rules by editing `config.toml` | **blocked** (file refused) | `config_test.go`, `e2e_test.go` |
| 25 | Change the rules without a journal entry | **blocked** (one transaction) | `rules_test.go` |
| 26 | Read the console from another site through DNS rebinding | **blocked** (loopback Host only) | `console_test.go` |
| 27 | Leave a connector running after the relay is asked to stop | **blocked** for SIGTERM/SIGINT; SIGKILL still open | `e2e_test.go` |

Live vulnerabilities found by audit rather than hypotheticals: **1** (any local
process could read every credential), **3** (the journal was writable by
anything), **6–8** (the daemon was impersonable, which the merge itself
introduced), **9** (one unauthenticated connection could exhaust the daemon's
memory), **18** (peer identity compared a filename rather than the running
image), and on 2026-09-14 **20–21** (a repeated `method` key put a `tools/call`
in front of the connector with no decision, no entry and no anomaly; a
case-variant `Name` had the daemon decide on one tool while the server ran
another) and **22** (a second connection could start a session under a live
id). Each has a regression test that fails against the code as it was. **23**
was found by reading rather than running: the relay verified the daemon, the
management commands did not, and one of them carries the plaintext secret.

The table above is the attacks someone thought of, and `dashboard/data/state.json`
is the copy the build checks -- every test named there must exist. When the two
disagree, the dashboard is the one that was checked.

**18 qualified 2–8 on darwin until it was fixed**, because all of those rest on
peer identity, and until the vnode check replaced the path comparison they were
defeatable there. They now hold on both unix platforms. Rows 1 and 10–13 never
depended on peer identity and hold everywhere.

**19 was not a vulnerability but a false accusation**, which is its own kind of
failure: `--expect-head` reported "entries have been removed and the chain
recomputed" for a journal that had merely grown since the head was recorded.
An operator shown that every time learns to ignore it, and the one check that
detects truncation stops being read. It now looks for the recorded head *in*
the chain rather than only at its tip.

## What protects what

**Peer identity** keeps anything that is not this binary off the socket, in
both directions. It is a floor, not the authorization model: anything able to
execute the binary passes it. It is verified at accept, while the peer is
certainly alive, which also closes the pid-reuse window a later check would
leave.

**Session ownership** stops one run of Nim reporting under another's session.
Peer identity cannot tell two runs apart; this can.

**The connector's registered command** is what authorizes a credential. Not
peer identity, and not the connection's target binding — both of those passed
the attack that read every secret. The daemon returns the command; the caller
never supplies one.

**Purpose binding** commits a connection to one job on its first request, so a
credential lookup cannot pivot to a second connector mid-connection, and a
policy connection cannot ask for a credential.

**The rules** are the authorization model, as far as one exists: allow or
deny per `(agent, connector, tool)`, in SQLite, changed only through the
daemon over the same verified socket as everything else, and every change
one transaction with its entry in the chain. `tool` is an exact name or `*`
for a default; there is no matching shorter than that. Among the rules that
match a call, the most specific wins -- an exact tool over a default, naming
the agent or the connector over not naming it -- and a tie in specificity
goes to deny; no matching rule at all is allow, the M4 baseline. One
function, `journal.Decide`, is the whole of that precedence, table-driven
tested and used by both the decision path and `nim policy explain`, so what
the CLI says a call would get can never drift from what it actually gets.
docs/decisions/0003-allow-rules-and-precedence.md is the argument. What a
rule is worth is bounded by what an enrolment is worth -- anything that can
run this binary as this user can make either -- and the entry is what makes
that visible afterwards, not what prevents it.

**Strict reading** of the one message Nim acts on. Objects are read by exact
key and a repeated key is refused, because that is the one shape on which
parsers legitimately disagree, and a `tools/call` Nim cannot read as one tool
name is refused before the daemon is asked.

**Fail-closed** covers every way of not getting a decision, including a rule
lookup that fails.

## Agent identity

An agent is a client program, enrolled by the operator and identified by the
executable it runs -- the same kernel-backed identity peer verification uses,
applied to the socket peer's *parent*, since a relay is spawned by the client.
Nothing about it is sent by the relay: there is no agent field on the wire, so
an agent cannot state one. A name a caller could choose would let a restricted
agent claim an unrestricted one's, which inverts a policy rather than bypassing
it.

Recorded on `session.start` at schema_version 2, and covered by the chain:
altering it after the fact breaks the entry's hash.

**Rules decide on it.** A rule can be scoped to an enrolled agent and applies
to the sessions derived as that agent, and to no other. A session no enrolment
matched -- the ordinary state -- meets only the rules that name no agent.
Under M4's deny-only rules that was necessarily a ceiling, because nothing
could grant. Since M4.5 a rule can also allow, so the same sentence now cuts
both ways: an agent-scoped allow can admit an enrolled agent to something an
unenrolled one is denied, which is the point of enrolling it.
`docs/decisions/0002-what-an-unknown-agent-may-do.md` is the argument for the
deny-only case; `docs/decisions/0003-allow-rules-and-precedence.md` is the
argument for this one, including why an unknown agent still cannot gain
anything an operator did not explicitly grant to a name.

**An enrolment change is a journal entry** (F-018, closed). `agent.add` and
`agent.remove` are written by `AddAgent` and `RemoveAgent` in the same
transaction as the change to `nim_agents`, exactly as `rule.add` and
`rule.remove` are. Re-enrolling a name against another executable now writes a
fresh `agent.add` carrying the new identity, so the chain says which
executable every rule scoped to that name applied to, and when that changed.
The rules are in the chain; what they are scoped to is too.

What it establishes: two different client programs are different agents. What
it does not: two windows of the same program are the same agent, because they
run the same file. Enrolment is also as privileged as running Nim -- anything
that can execute this binary as this user can enrol or replace one, exactly as
it can register a connector.

## Known gaps

| Gap | Severity | Why it is open |
|---|---|---|
| **Windows has no peer verification** | high, on Windows | AF_UNIX there exposes no `SO_PEERCRED` equivalent. Needs a named-pipe transport, which reverses a standing decision. Nothing in this project has ever been run on Windows. |
| **Windows socket directory has no real ACL** | high, on Windows | `os.Chmod` only toggles the read-only attribute. Confidentiality rests on default temp-directory ACLs. |
| **PATH resolution on the registered command** | medium | The registered argv is spawned through normal PATH lookup, so a caller that already controls PATH can front-run the binary name. Closing it needs process inversion. |
| **The credential is handed to the connector** | medium | Injected into the downstream's environment, so a compromised connector has its own secret and, on Linux, any same-user process can read `/proc/<pid>/environ`. Nim cannot revoke what it has given away. |
| **The journal is unkeyed** | medium | See the threat model. Only `--expect-head` covers rewriting. |
| **A relay killed with SIGKILL cannot stop its connector** | low | Only a connector that reads its stdin notices. SIGTERM and SIGINT are handled; nothing can handle SIGKILL. |

**"Rules only deny" is fixed and no longer listed here.** M4.5 added allow
rules and a stated precedence between them and deny
(docs/decisions/0003-allow-rules-and-precedence.md); D-002 is answered again
there and in docs/decisions/0002's last section. An allow-list per agent is
now `nim policy default deny` plus `nim policy allow <tool> --agent <name>`,
and an unenrolled program is denied by the default rule rather than merely
unprivileged by an absent one.

## Re-running the attacks

The regression tests are the attacks, and they are spread across every
package, so the honest command is the whole suite -- it takes under a minute:

```bash
cd engine
go test -race -shuffle=on -count=1 -v ./...
```

The `-run` pattern this section used to give selected the tests behind rows
2–9 and none of the others, while saying it re-ran the attacks. The exact test
names per row are in `dashboard/data/state.json`, which the build checks
against the tree; `.github/scripts/assert-evidence-ran.sh` then refuses a CI
run in which any of them was skipped rather than run.

The manual ones (leakage into logs and the database) are worth repeating by
hand after any change to logging or the journal schema, because they are
absence checks and absence is not something a test suite notices losing.
