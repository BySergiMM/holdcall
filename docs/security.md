# Security posture

What Holdcall protects, what it does not, and what has actually been tried against
it. Every attack below was run against a real build; none is theoretical.

Read `docs/journal-format.md` for the record's own limits and
`docs/decisions/0001-failure-behaviour.md` for what happens when Holdcall cannot
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
running as the same user, spawned by Holdcall itself. Everything below is written
against that.

**Human approval (M6) is written against a second adversary: the model
driving the client**, not only the server on the other side of a call. An
`ask` rule exists because the operator does not trust a call's own
arguments to be decided by policy alone, and the thing that produces those
arguments -- picks the tool, writes the parameters, and would write a
summary of them if `holdcall approve` showed one -- is the model, not the
server. `docs/decisions/0005-human-approval.md` is the argument in full;
the short form is that `holdcall approve` prints the real `params.arguments`
because a model motivated to get a dangerous call approved has every reason
to describe it as something else. **The human operator is trusted, in this
model as in the base one.** Approval defends against the model choosing to
ask for something it should not, not against the person running `holdcall`
choosing to approve it anyway -- that is the operator doing what they are
entitled to do with a machine they run as themselves, the same boundary
"a user who reconfigures their client" already draws below.

Not in the model, and worth being explicit about:

- **A user who reconfigures their client.** Nothing stops a client being
  pointed straight at an MCP server instead of at Holdcall. No in-process shim can
  stop that. Holdcall's guarantee is about calls that go through it; making
  connectors unreachable except through Holdcall needs a network or sandbox
  boundary.
- **Root, or another user with debugger access to the daemon.**
- **A local attacker who can write to `holdcall.db`.** The chain is unkeyed —
  nothing in it is secret, so anyone able to edit a row can recompute every
  hash from the genesis and the result verifies cleanly. `holdcall verify
  --expect-head <hash>` is the only thing that catches it, and only for what
  was committed before the head you recorded.

## What peer identity establishes, per platform

Peer identity asks the kernel who is on the other end of a socket: which user,
and whether it is running the same file we are. The question that decides the
strength of the second half is whether the answer describes a **running image**
or a **filename**.

**The user is asked first, and a different one ends the check.** The kernel's
record of the connection carries the peer's effective user id (`SO_PEERCRED`
on Linux, `LOCAL_PEERCRED` on macOS), and it is compared with this process's.
A peer running as someone else is refused even if it is running the very same
binary, which an image check alone cannot see: any user who can execute a
file is running that file. Until this was added the check compared images and
nothing else; what kept other users off the socket was its directory and mode,
and that is still the first line (see *Private directories* below). Peer
identity is the second, for the day one of those is wrong. The refusal says
which check refused: "runs as another user" is not "is not Holdcall", and the
log line and the client's message name the right one.
`TestAPeerRunningAsAnotherUserIsRefusedEvenIfItRunsThisBinary`
(`internal/peer/peer_uid_test.go`) is the attack: a real second process of the
test binary, accepted, and then refused when only the user the daemon side
believes it is changes. `TestAPeerThatReallyRunsAsAnotherUserIsRefused` does it
with a real second uid (65534) and needs root, so it skips everywhere else,
CI included: run as root on Linux it passes, and the first of the two is the
one a runner executes. On macOS only the first runs, in CI; the comparison there
is the same code over `LOCAL_PEERCRED`'s `cr_uid`, which `getpeereid(3)`
documents as the peer's effective user.

| | How the peer's image is resolved | Resists path swap |
|---|---|---|
| **Linux** | `stat("/proc/<pid>/exe")` — the kernel resolves the magic link to the inode the process is executing | yes |
| **Darwin** | the vnode behind the peer's own mapping of its main image, via `proc_info`'s `PROC_PIDREGIONPATHINFO` | yes |
| **Windows** | nothing — Holdcall has no peer-credentials call there (AF_UNIX on Windows carries none), so neither the user nor the image is asked | n/a, unsupported. See *Windows is experimental* below |

Both unix implementations previously stat'ed a *path* (`kern.procargs2` on
darwin, `readlink /proc/<pid>/exe` on linux) and were defeated with no race at
all: launch from a path you own, replace the file there with a link to the holdcall
binary, then connect. `internal/peer/pathswap_test.go` is that attack, and it
runs on both platforms.

**What this is not.** It is executable identity, kernel-provided — not
code-signing identity. It says nothing about who signed a binary, and a
rebuild of Holdcall is a different inode and therefore a different program as far as
this is concerned. It also does not distinguish two processes running the same
file, which is why session ownership exists.

**What an upgrade looks like (F-001).** Replacing the binary at its own path
while a daemon runs — a `go build -o` or an install over a running process —
leaves the old daemon executing an inode the directory entry no longer names,
so the daemon and every new client compare as different images and refuse
each other by the same check as any other mismatch. The trust boundary does
not move: this is still "not the same file", decided the same way. What
changes is the diagnosis. `peer.Diagnose` tells that specific shape — the
peer's launch path is exactly our own, only the file differs — apart from an
unrelated impostor at a different path, so the daemon's log and every
client's refusal can say "older build, restart it" instead of the generic
"not Holdcall". `holdcall daemon restart` is that restart, sent by signal because the
socket itself is exactly what an older build cannot answer for a new client.

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

## Private directories

What keeps another user off the daemon starts with their not being able to reach
it. Holdcall's home, its data directory (the journal and the install's
identifier) and the directory the socket is in are created `0700`, and on Linux
and macOS one that already exists is **checked**, not assumed: it must belong to
the user running Holdcall (a link is followed, and the directory it leads to is
the one checked), and any access group or other has to it is removed. A
directory somebody else owns is refused, with a message naming it and the setting
that moves it, and the daemon does not start. The socket is bound only inside a
directory that passed this, so it is never reachable in the gap between
`net.Listen` creating it and the `chmod` that narrowed it (F-008 was exactly that
gap).

Where the default socket goes follows from it: `$XDG_RUNTIME_DIR` when set (the
XDG Base Directory specification makes it the user's own, mode 0700), otherwise
the temp directory if it is already private to the user (macOS's per-user
`$TMPDIR`), otherwise a `holdcall-<uid>` directory the user creates inside it.
Never directly in a shared directory such as Linux's `/tmp`: root owns that, so a
daemon that insists on owning its socket's directory could not start there, and
one that did not insist could have its socket pre-created or replaced by any
local user. Evidence: `TestEnsurePrivateDirRefusesADirectoryAnotherUserOwns`,
`TestEnsurePrivateDirNarrowsADirectoryWeOwnThatOthersCouldReach`,
`TestTheDefaultSocketIsNeverPutDirectlyInADirectoryOthersCanUse`
(`internal/config/privatedir_test.go`) and `TestListenRefusesASocketDirectoryAnotherUserOwns`,
`TestListenNarrowsAPreExistingSocketDirectory` (`internal/daemon/listen_dir_test.go`),
which fail when the checks they exercise are removed. Another user's directory
is simulated by making the test believe it runs as a different uid; no test here
creates a real second account.

**What this is not.** It is the first line, not a boundary against the same user:
a process running as you owns the same directories. And on Windows none of it
applies, see below.

## Windows is experimental

Holdcall builds for Windows, `release.yml` publishes a `.zip`, and the whole
suite runs on `windows-latest` in CI (since 2026-09-28, F-029). That is all the
evidence there is for Windows: a CI runner, not a person at a Windows desktop.
The tests whose job is to show an attacker being refused are skipped there, each
named with its reason in `.github/scripts/windows-skip-allowlist.txt`, because
on Windows the attacker is not refused. **The guarantee at the top of this
document does not hold on Windows against another process running as the same
user.** Use it there to see what an agent does, not to stop one.

| | Linux and macOS | Windows |
|---|---|---|
| The platform confirms who is connecting: this user, this binary | yes, for every connection at accept, and for the daemon by every relay before it sends a byte | **no.** Holdcall has no peer-credentials call there (see below). Every connection is accepted and every daemon is believed |
| A process that is not Holdcall obtains a decision or writes journal entries (rows 3 and 4) | refused | **possible** for anything that can open the socket |
| A process that binds the socket first is taken for the daemon, and sees each call's tool name and argument digest (rows 6 and 8) | refused by the relay | **taken for it**, and its decisions are believed |
| A secret leaves the daemon | to a confirmed peer only, with the command it is registered for | **never.** `credential.get` for a configured connector is answered with an error before the store is read. The relay takes no connector, credential or command, from a daemon it cannot confirm, and does not start. `holdcall connector set` refuses to send a secret |
| A server with no connector runs through the relay, decided and journaled | yes | yes |
| Rules, budgets, `ask`, strict reading, fail-closed, the chain and `verify` | yes | the same code, and the tests that do not depend on peer identity run on `windows-latest`; nothing stops another process from using the same daemon |
| Agent identity: an enrolment is an executable | yes | **no.** `holdcall agent add` fails, no session is ever matched to an enrolment, and only rules that name no agent apply |
| Home, data and socket directories are private to the user | created `0700`, and an existing one must be the user's own | **not checked.** No ACL is read or set; privacy rests on the default ACLs under the user's profile |
| `holdcall daemon restart` | yes | refuses, and says how to stop the daemon by hand |
| Daemons racing at startup are serialised | yes (`flock`) | no: the startup lock does nothing, so the stale-socket reclaim race is not closed |
| Credential store | Keychain, Secret Service | DPAPI (`store_windows.go`). No test calls it, and with no secret able to move, nothing reaches it |

Rows 2, 7 and 23 of the attack table below hold on Windows too, by a different
mechanism: nothing secret moves. `TestOnWindowsARealDaemonReleasesNoCredentialOverARealSocket` and
`TestOnWindowsARelayDoesNotSpawnWhatAnUnverifiedDaemonNamed` run that refusal on
`windows-latest`, over a real socket. Rows 3, 4, 6 and 8 are open there.

**Why a secret is refused rather than left as it was.** A connection nobody could
verify was handled like a verified one, so anything able to open the socket could
be handed any connector's credential with the command it was registered for.
Refusing costs Windows its connectors, and it is the only choice that does not
hand a secret to something nobody confirmed. The rule is not about Windows:
`requestState.peerVerified` is `peer.Verdict.Verified()` wherever Holdcall runs,
so a platform that gains a verdict gains the release with no further change.

**What is open, and what it is not.** Microsoft's announcement of AF_UNIX on
Windows lists ancillary data, which is what `SCM_CREDENTIALS` travels in, as
unsupported
([Windows Command Line blog](https://devblogs.microsoft.com/commandline/af_unix-comes-to-windows/)),
so there is no equivalent of `SO_PEERCRED` or `LOCAL_PEERCRED`. The peer's
process id can be read, though, through the `SIO_AF_UNIX_GETPEERPID` control
code (defined in mingw-w64's `afunix.h`, and reported working on Windows 10
1903 to 2004 in [microsoft/WSL#4676](https://github.com/microsoft/WSL/issues/4676);
Microsoft's [Winsock IOCTL reference](https://learn.microsoft.com/en-us/windows/win32/winsock/winsock-ioctls)
does not list it). Holdcall does not use it. A user and an image identity would
have to be derived from that pid, which nothing in this repository does and
nobody here could run to show it works; what is stated above is what the code
does and no more. This corrects an earlier version of this document, which said
Windows exposed no way to ask and that closing the gap needed a named-pipe
transport: the pid route is the other candidate, and `docs/milestones.md` (M1)
records the decision to use one unix-socket transport on all three platforms.

If Windows ever gets a verdict, `TestOnWindowsARealDaemonReleasesNoCredentialOverARealSocket`
fails and says so: the cue to implement `peer_windows.go`, to rewrite this
section, and to delete that test.

## Attacks run against the current build

| # | Attack | Result | Covered by |
|---|---|---|---|
| 1 | Name a connector, supply your own command, read the credential | **blocked** | `credential_binding_test.go`, `spawn_test.go` |
| 2 | Ask for a credential from a process that is not Holdcall | **blocked** | `peer_authz_test.go` |
| 3 | Write forged entries to the journal from a non-Holdcall process | **blocked** | `peer_authz_test.go` |
| 4 | Obtain a decision from a non-Holdcall process | **blocked** | `peer_authz_test.go` |
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
| 23 | Bind the socket first and receive the secret from `holdcall connector set` | **blocked** | `impostor_test.go` |
| 24 | Change the rules by editing `config.toml` | **blocked** (file refused) | `config_test.go`, `e2e_test.go` |
| 25 | Change the rules without a journal entry | **blocked** (one transaction) | `rules_test.go` |
| 26 | Read the console from another site through DNS rebinding | **blocked** (loopback Host only) | `console_test.go` |
| 27 | Leave a connector running after the relay is asked to stop | **blocked** for SIGTERM/SIGINT; SIGKILL still open | `e2e_test.go` |
| 28 | Enrol a second name against the operator's client binary, then wait for a re-enrolment | **blocked** (one executable is one agent) | `agents_test.go`, `agent_test.go` |
| 29 | Read a secret from `holdcall init`'s own output | **blocked** (env values never shown) | `rewrite_test.go` |
| 30 | Read a connector's secret through the console | **blocked** (none is there to read) | `console_test.go` |
| 31 | Speak the newer `server/discover` handshake so the client cannot parse Holdcall's refusals | **blocked** (refusals follow the negotiated dialect) | `dialect_test.go`, `deny_test.go`, `tools/relay-rig` |
| 32 | Approve your own call from the model | **blocked only by circumstance, stated honestly rather than claimed further**: peer identity refuses anything that is not the `holdcall` binary, so a model that cannot execute commands has no way to reach `holdcall approve` at all -- but a model with shell access, which many agent setups grant, runs `holdcall approve` exactly as an operator typing it would, and nothing on the socket tells the two apart. See *Threat model* above and `docs/decisions/0005-human-approval.md`. | `peer_authz_test.go` (the peer-identity floor); not closed beyond it |
| 33 | Exhaust a budget, then keep calling | **blocked** (the next allowed call is refused; a budget never grants) | `budget_test.go`, `budget_e2e_test.go` |
| 34 | Connect as another user running the same binary | **blocked** (linux and darwin: the peer's user is compared before its image) | `peer_uid_test.go` |
| 35 | Pre-create the socket's directory as another user, or leave the socket in a shared directory | **blocked** (linux and darwin: the daemon will not start in a directory another user owns) | `privatedir_test.go`, `listen_dir_test.go` |
| 36 | Ask for a credential over a connection the platform could not verify | **blocked** (nothing is released, and the store is not read); this is every connection on Windows | `peer_unverified_test.go`, `peer_unverified_windows_test.go` |

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
**28** and **29** were found by the 2026-09-15 review of the merged M4.5 work
and reproduced in scratch tests before the code shipped: two names could be
enrolled against one executable with an unordered lookup between them, and
`holdcall init`'s dry run printed the env block where client configs keep tokens.
**34–36** were found by an audit that read the code, not by an attack on a
running install: peer identity compared a peer's executable and not
its user, the directories the socket and journal live in were assumed private
rather than checked, and a connection the platform could not verify was handed
credentials as if it had been. Each has a test that fails when the check it
exercises is removed.

The table above is the attacks someone thought of, and `dashboard/data/state.json`
is the copy the build checks -- every test named there must exist. When the two
disagree, the dashboard is the one that was checked.

**18 qualified 2–8 on darwin until it was fixed**, because all of those rest on
peer identity, and until the vnode check replaced the path comparison they were
defeatable there. They now hold on both unix platforms. Rows 1 and 10–13 never
depended on peer identity and hold everywhere. On Windows rows 3, 4, 6 and 8 do
not hold, and rows 2, 7 and 23 hold only because no secret moves there at all:
see *Windows is experimental*.

**19 was not a vulnerability but a false accusation**, which is its own kind of
failure: `--expect-head` reported "entries have been removed and the chain
recomputed" for a journal that had merely grown since the head was recorded.
An operator shown that every time learns to ignore it, and the one check that
detects truncation stops being read. It now looks for the recorded head *in*
the chain rather than only at its tip.

## What protects what

**Peer identity** keeps anything that is not this binary, run by this user, off
the socket, in both directions. It is a floor, not the authorization model:
anything able to execute the binary as this user passes it. It is verified at
accept, while the peer is certainly alive, which also closes the pid-reuse window
a later check would leave. On Windows it does not exist; see *Windows is
experimental*.

**Session ownership** stops one run of Holdcall reporting under another's session.
Peer identity cannot tell two runs apart; this can.

**The connector's registered command** is what authorizes a credential. Not
peer identity, and not the connection's target binding — both of those passed
the attack that read every secret. The daemon returns the command; the caller
never supplies one.

**Purpose binding** commits a connection to one job on its first request, so a
credential lookup cannot pivot to a second connector mid-connection, a policy
connection cannot ask for a credential, and an approval connection --
`approval.list`/`approval.decide`, M6 -- cannot either.

**The rules** are the authorization model, as far as one exists: deny, allow
or ask per `(agent, connector, tool)`, in SQLite, changed only through the
daemon over the same verified socket as everything else, and every change
one transaction with its entry in the chain. `tool` is an exact name or `*`
for a default; there is no matching shorter than that. Among the rules that
match a call, the most specific wins -- an exact tool over a default, naming
the agent or the connector over not naming it -- and a tie in specificity
goes to deny, then ask, then allow; no matching rule at all is allow, the M4
baseline. One function, `journal.Decide`, is the whole of that precedence,
table-driven tested and used by both the decision path and `holdcall policy
explain`, so what the CLI says a call would get can never drift from what it
actually gets. docs/decisions/0003-allow-rules-and-precedence.md, and its
2026-09-15 addendum for `ask`, is the argument. What a rule is worth is
bounded by what an enrolment is worth -- anything that can run this binary as
this user can make either -- and the entry is what makes that visible
afterwards, not what prevents it. **A budget narrows what the
rules allow, never widens it.** Checked only once a rule has already said
allow, a budget caps the number of allowed calls one session may make, scoped
like a rule; it counts decisions, not outcomes, so a call the relay gave up
on still spent its share, and it has no way to express "allow" itself, so it
can never turn a rule's deny into anything else. docs/decisions/0004-budgets.md
is the argument.

**Human approval** (M6) is what an `ask` rule leads to: the call is held in
memory, off the model's channel, until a human decides it through `holdcall
approve`/`holdcall reject` or the approval timer rejects it for them. What it
protects is narrower than what the rules above protect, and
`docs/decisions/0005-human-approval.md` says so directly: it defends
against *the model* choosing to ask for something dangerous, not against
the operator choosing to approve it anyway. The real arguments a human sees
never touch SQLite or a log line while the call is held -- `pendingRegistry`
is memory only -- and the daemon writes the decision before it answers the
relay, the same ordering every other verdict this project makes already
keeps. See row 32 in the attack table for what this does not close: nothing
here tells a human typing `holdcall approve` apart from a model that has been
given a shell.

**Strict reading** of the one message Holdcall acts on. Objects are read by exact
key and a repeated key is refused, because that is the one shape on which
parsers legitimately disagree, and a `tools/call` Holdcall cannot read as one tool
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
run the same file. Enrolment is also as privileged as running Holdcall -- anything
that can execute this binary as this user can enrol or replace one, exactly as
it can register a connector.

## Known gaps

| Gap | Severity | Why it is open |
|---|---|---|
| **Windows has no peer verification** | high, on Windows | Not implemented. AF_UNIX there has no `SO_PEERCRED` equivalent; the peer's process id can be read (`SIO_AF_UNIX_GETPEERPID`) but is not, and a user and image identity would have to be derived from it. A named-pipe transport is the other route and reverses a standing decision. The suite does run on `windows-latest` in CI since 2026-09-28, with the tests that show an attacker refused skipped by name. Since this change no secret is released to a peer that cannot be verified, so on Windows no connector works. See *Windows is experimental*. |
| **Windows directories have no real ACL** | high, on Windows | `os.Chmod` only toggles the read-only attribute, and nothing here reads or sets an ACL. Home, journal and socket privacy rests on the default ACLs under the user's profile. On Linux and macOS the directories are created `0700` and an existing one must be the user's own; see *Private directories*. |
| **PATH resolution on the registered command** | medium | The registered argv is spawned through normal PATH lookup, so a caller that already controls PATH can front-run the binary name. Closing it needs process inversion. |
| **The credential is handed to the connector** | medium | Injected into the downstream's environment, so a compromised connector has its own secret and, on Linux, any same-user process can read `/proc/<pid>/environ`. Holdcall cannot revoke what it has given away. |
| **The journal is unkeyed** | medium | See the threat model. Only `--expect-head` covers rewriting. |
| **A relay killed with SIGKILL cannot stop its connector** | low | Only a connector that reads its stdin notices. SIGTERM and SIGINT are handled; nothing can handle SIGKILL. |

**"Rules only deny" is fixed and no longer listed here.** M4.5 added allow
rules and a stated precedence between them and deny
(docs/decisions/0003-allow-rules-and-precedence.md); D-002 is answered again
there and in docs/decisions/0002's last section. An allow-list per agent is
now `holdcall policy default deny` plus `holdcall policy allow <tool> --agent <name>`,
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
