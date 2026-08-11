# Merging the two lineages

Two branches grew from M1 (`e215bf1`) in parallel and neither contains the
other. This is the plan for making one trunk out of them. It is written down
rather than done because the merge needs a decision nobody has taken yet — see
*The decision that has to be made first*.

## What each side has

| | `m1-bootstrap` | `claude/nim-audit-context-8e6c6c` |
|---|---|---|
| Journal | mutable `nim_calls` rows, upsert | append-only `nim_journal`, hash-chained, views |
| Verification | none | `canonical_encode_v1`, `nim verify`, `--expect-head` |
| Enforcement | none — `decision` is the literal `"allow"` | real: synchronous decide, fail-closed, deny list |
| Credentials | OS store, bound to a registered command | none |
| Peer authorization | yes | yes (ported across, `148bf11`) |
| Daemon lifetime | `Setsid`, job breakaway, flock startup lock | none |
| Read surface | `nim status` | `nim log`, `nim console`, `readmodel` projections |
| Anomalies | none — a batch passes unrecorded | `batch`, `malformed_json`, `framing`, `duplicate_id` |

## Direction

**The audit branch is the base.** Its journal is the one worth keeping: a
chain cannot be laid over rows that get updated, so adopting the other
direction would mean rewriting the chain onto a model that cannot carry it.
Everything `m1-bootstrap` has that it lacks is additive by comparison.

## The decision that has to be made first

**The two branches take opposite positions on an unreachable daemon.**

- `m1-bootstrap` fails **open**: no daemon means the downstream spawns without
  its credential, and nothing is recorded. The argument is M1's — the relay
  must never be what breaks a working setup.
- the audit branch fails **closed**: no daemon means every `tools/call` is
  denied. The argument is that a call Nim cannot record should not happen.

These cannot both survive. The recommendation is **fail closed everywhere**,
because under the audit branch's model a downstream that starts without its
credential is useless anyway — every call it would make is already being
denied — so failing open buys nothing and costs the guarantee. But this is a
product decision about what Nim does on a bad day, and it should be taken
deliberately rather than settled by whichever side wins a diff hunk.

## Conflict surface, measured

`git merge-tree --write-tree m1-bootstrap claude/nim-audit-context-8e6c6c`
reports ten conflicting files. Four are the core, four are their tests
(add/add — rewritten wholesale on both sides), two are documentation.

| File | Why it conflicts | Difficulty |
|---|---|---|
| `journal/journal.go` | two data models, not two edits | **hard** — do not textually merge |
| `daemon/daemon.go` | both rewrote `handle()` | **hard** — three message families to route |
| `shim/shim.go` | credential injection vs the decide path | **medium** |
| `cmd/nim/main.go` | different subcommands, no overlap | easy |
| `config/config.go` | `Policy` vs nothing | easy |
| `*_test.go` (×4) | rewritten on both sides | medium — merge by hand, keep both |
| `README.md`, `docs/milestones.md` | both rewritten | easy, but renumber (below) |

## Order of work

Each step should build and pass `go test -race ./...` before the next.

1. **Branch from the audit branch.** Do not `git merge` — the journal conflict
   is semantic and a textual merge will produce something that compiles and is
   wrong. Port deliberately, file by file.
2. **Additive files first — no conflicts, immediate value.** `credential/*`,
   `daemon/protocol.go`, `daemon/validate.go`, `daemon/lock_target.go`,
   `daemon/lock_unix.go`, `daemon/lock_windows.go`, `shim/detach_unix.go`,
   `shim/detach_windows.go`, `uuid/*`. (`daemon/peer*.go` and `daemon/reader.go`
   are already there, ported in `148bf11`.)
3. **`nim_connectors` into the chained journal.** The table is orthogonal to
   `nim_journal` — it is connector metadata, not a record of events — so it
   moves across as-is, with its `command` column and the additive migration.
4. **Route three message families in `handle()`.** The base reads only Events;
   `m1-bootstrap` added Request/Response for credentials. The merged handler
   peeks at `kind`, routes requests to `handleRequest`, `call.request` to
   `answer`, and everything else to `apply`. Peer authorization and session
   ownership already sit above all three.
5. **`fetchConnector` into the base's `Run`.** The credential round-trip
   happens once at spawn, before the decide path exists for that session, so
   the two do not interact. Keep the command binding intact: the daemon
   decides what a credential is injected into.
6. **Daemon lifetime.** `Setsid`, the Windows breakaway, and the flock startup
   lock port onto the base's `listen()`, which currently has neither the lock
   nor the retry loop.
7. **CLI.** `serve`, `daemon`, `status`, `connector *`, `log`, `verify`,
   `console`. The subcommands do not overlap; `status` needs merging, since
   both sides extended it.
8. **Tests.** Keep every test from both sides. The add/add conflicts are not
   competing versions of one file, they are two disjoint sets of tests that
   happen to share a filename.
9. **Renumber the milestones once.** `M2` currently names *daemon lifetime* on
   one branch and *minimal enforcement* on the other, where lifetime is
   `M2.5`. Pick one numbering, state the equivalence, and delete the other
   history.

## What to check when it is done

- The relay rig still reports `RESULT: IDENTICAL` (`pip install fastmcp`,
  `python tools/relay-rig/compare.py`). This is the only test that proves a
  real client cannot tell, and every merge step above touches the relay.
- `nim verify` on a journal written by the merged binary.
- A credential is injected into the registered command, and only that one:
  `nim serve --target X -- /bin/sh -c 'env'` must not receive X's secret.
- A denied call never reaches the connector, and no daemon means denied.
- Both benchmarks still report a p99 far below `decisionTimeout`.

## Estimate

One to two days of careful work. Most of it is steps 4 and 8; steps 1–3 and
6–7 are mechanical.
