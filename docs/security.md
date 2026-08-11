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
| 10 | Bypass the deny list | **blocked** | `decide_test.go`, `enforce_test.go` |
| 11 | Smuggle a `tools/call` inside a JSON-RPC batch | **blocked** (batch refused whole) | `enforce_test.go` |
| 12 | Smuggle a call in a frame Go cannot parse but the server can | **blocked** (frame refused) | `enforce_test.go` |
| 13 | Kill the daemon to disable enforcement | **blocked** (denies) | `enforce_test.go` |
| 14 | Truncate the journal's tail | **detected** only with `--expect-head` | `journal_test.go` |
| 15 | Alter an entry in place | **detected** | `journal_test.go` |
| 16 | Leak a credential into logs, the journal, or stderr | **none found** | manual, re-run each audit |
| 17 | Leak call arguments into the journal | **none found** (digest only) | `journal_test.go` |

Three of these were live vulnerabilities found by audit rather than
hypotheticals: **1** (any local process could read every credential), **3**
(the journal was writable by anything), and **6–8** (the daemon was
impersonable, which the merge itself introduced). Each has a regression test
that fails against the code as it was.

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
credential lookup cannot pivot to a second connector mid-connection.

**Fail-closed** covers every way of not getting a decision.

## Known gaps

| Gap | Severity | Why it is open |
|---|---|---|
| **Windows has no peer verification** | high, on Windows | AF_UNIX there exposes no `SO_PEERCRED` equivalent. Needs a named-pipe transport, which reverses a standing decision. Nothing in this project has ever been run on Windows. |
| **Windows socket directory has no real ACL** | high, on Windows | `os.Chmod` only toggles the read-only attribute. Confidentiality rests on default temp-directory ACLs. |
| **PATH resolution on the registered command** | medium | The registered argv is spawned through normal PATH lookup, so a caller that already controls PATH can front-run the binary name. Closing it needs process inversion. |
| **The credential is handed to the connector** | medium | Injected into the downstream's environment, so a compromised connector has its own secret and, on Linux, any same-user process can read `/proc/<pid>/environ`. Nim cannot revoke what it has given away. |
| **Policy lives in `config.toml`** | medium | Anything able to write that file can empty the deny list, and the change leaves no journal entry. Scaffolding; belongs in SQLite. |
| **The journal is unkeyed** | medium | See the threat model. Only `--expect-head` covers rewriting. |
| **No agent identity** | — | Every decision is per-tool, not per-agent. Not a vulnerability; the reason grants, budgets and approval cannot be built yet. |

## Re-running the attacks

The regression tests are the attacks:

```bash
cd engine
go test -race -run 'Impostor|NonNim|Credential|Connection|Unauthenticated' ./... -v
```

The manual ones (leakage into logs and the database) are worth repeating by
hand after any change to logging or the journal schema, because they are
absence checks and absence is not something a test suite notices losing.
