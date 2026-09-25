# Benchmarks

Every number here is produced by a benchmark in the repository. None of them
is a remembered figure, and none should be quoted without re-running it: the
point of this file is the commands, not the values.

## How to reproduce

```bash
cd engine
go test -run XXX -bench . -benchtime 2000x ./internal/daemon/ 2>/dev/null
go test -run XXX -bench . -benchtime 2000x ./internal/journal/
go test -run XXX -bench . -benchtime 2000x ./internal/mcp/
```

`2>/dev/null` on the daemon package drops its own log output, which otherwise
lands in the middle of the result rows.

Latencies are reported as custom metrics (`p50_ms`, `p95_ms`, `p99_ms`,
`max_ms`) rather than as the mean Go prints. A mean is the wrong statistic for
justifying a timeout; the tail is the thing that has to stay clear of it.

## Conditions

| | |
|---|---|
| Machine | Apple M5, 10 cores, darwin/arm64 |
| Go | 1.26.5 |
| Build | `CGO_ENABLED=0`, `modernc.org/sqlite` (pure Go) |
| Journal | `synchronous=FULL`, WAL, `_txlock=immediate` |
| Date | 2026-09-14 |
| State | `m1-bootstrap`, after M4 (a decision reads the rules table) |

Unloaded laptop, on battery, no other significant work running. These are not
server numbers and are not claimed to be.

## Decision path — what a `tools/call` waits for

The whole round trip a relay blocks on: write `call.request` to the socket,
look the call up in the rules table, append the chained entry to SQLite **and
commit it**, write the decision back, read it. The journal write is included
deliberately — the entry is durable before the answer is sent, and excluding it
would measure something Nim never does.

| Benchmark | p50 | p95 | p99 | max |
|---|---|---|---|---|
| `DecisionRoundTrip` (one relay) | 0.091 ms | 0.182 ms | 0.263 ms | 1.00 ms |
| `DecisionRoundTripContended` (16 relays) | 1.22 ms | 1.76 ms | 1.96 ms | 8.45 ms |
| `DecisionDenied` (one relay, refused) | 0.092 ms | 0.133 ms | 0.169 ms | 0.42 ms |

**What this justifies.** `decisionTimeout` is 2 s — three orders of magnitude
past the worst p99 above. It cannot fire because the daemon is busy, only
because it is wedged or gone, which is what makes fail-closed a safety net
rather than a latency policy.

**What M4 cost.** The decision now reads `nim_rules` — one indexed query per
call, against the one table a decision may read — where it used to scan a
list in memory. p50 moved from 0.075 ms to 0.091 ms and p99 stayed where it
was; the commit still dominates. The contended `max` roughly doubled on this
run, which is one sample at the tail of sixteen relays fighting for one write
lock and is not a figure to build on.

**What M4.5 cost.** Allow rules and a precedence between allow and deny
(`docs/decisions/0003`) changed the lookup from "is there a deny for this
tool" to "collect every matching rule and pick one": at most eight candidate
rows through the same unique index. Re-run on 2026-09-15 with the same
command: `DecisionRoundTrip` p50 0.092 ms, p99 0.272 ms; contended p50
1.17 ms, p99 1.78 ms; `DecisionDenied` p50 0.091 ms, p99 0.151 ms. Within the
noise of the M4 figures above, so the table is not restated.

**What M5 cost.** A budget is consulted only once the rules have allowed a
call, and costs two more reads on the same connection: the budgets whose
scope matches, and a count of this session's allowed `call.request` entries
through the unique index on `(session_id, seq, kind)`. Measured on
2026-09-15 with one budget configured and never exhausted
(`BenchmarkDecisionWithBudget`, same command, `-benchtime 2000x`): p50
0.32 ms, p95 0.51 ms, p99 0.79 ms, max 3.2 ms, against `DecisionRoundTrip`
p50 0.11 ms, p99 0.20 ms on the same run. Three times the plain allow, and
still three orders of magnitude under `decisionTimeout`; a session with no
matching budget pays one read for the empty match and nothing for the count.

**Deny is not slower than allow.** It does the same journal write and the same
rule lookup. If those diverged it would mean the rules had become the expensive
part; for an indexed exact-name lookup they must not, and they do not.

## Credential lookup — spawn time, once per session

| Benchmark | p50 | p95 | p99 |
|---|---|---|---|
| `CredentialLookup` | 0.005 ms | 0.006 ms | 0.007 ms |

Measured at the handler, so it excludes the OS credential store. That is
deliberate: a Keychain or Secret Service read is the operating system's cost,
not Nim's, and it varies with whether the keychain is unlocked. What this says
is that Nim's own part of a credential lookup is free next to everything else.

## Journal

| Benchmark | Result |
|---|---|
| `Append` (one writer, committed) | 75 µs/op |
| `AppendContended` (4 writers) | 70 µs/op |
| `CanonicalEncode` (no I/O) | 185 ns/op |
| `Verify` (2000-entry chain) | 3.20 ms |

**The commit dominates.** Encoding an entry costs 185 ns; committing it costs
about 75 µs — 400× more. Hashing is not where the time goes, and any future
argument about the chain being expensive should start here.

**Verification stays cheap but grows.** 2000 entries in 3.2 ms is roughly
1.6 µs per entry, and it is linear: a million-entry journal is about 1.6 s.
That is fine for `nim verify` on demand and would not be fine on every write,
which is why nothing verifies on the write path.

## Relay — the invisibility budget

Every message pays the framing cost; only a `tools/call` pays inspection.

| Benchmark | Result | Throughput |
|---|---|---|
| `RelayFramingSmall` | 5.4 µs / 64 msgs | 21 MB/s |
| `RelayFramingLarge` (512 KiB) | 3.0 ms / 64 msgs | 177 MB/s |
| `RelayInspectSmall` | 3.6 µs | 31 MB/s |
| `RelayInspectLarge` (512 KiB args) | 4.8 ms | 109 MB/s |
| `ArgumentsDigest` 1 KiB | 4.0 µs | 257 MB/s |
| `ArgumentsDigest` 64 KiB | 201 µs | 327 MB/s |
| `ArgumentsDigest` 512 KiB | 1.6 ms | 320 MB/s |

**Inspection got stricter, and a small message pays for it.** Since M4 an
object is read key by key, refusing a key that appears twice, instead of one
`json.Unmarshal` into a struct — that is what closed the duplicate-key bypass
(F-013). `RelayInspectSmall` went from 2.1 µs to 3.6 µs per message. A
tools/call is the only message that pays it, it is paid once, and it is a
fraction of the journal commit that follows.

**The digest is the size-proportional cost.** Nothing else Nim does per call
scales with payload size — messages are never re-serialised, only inspected —
so `ArgumentsDigest` is the floor for a large `tools/call`, and it runs at
roughly SHA-256's own speed.

**A 512 KiB tool call costs about 5 ms of inspection.** Against a network round
trip to a real service, that is not the thing anyone will notice. Against a
local server returning instantly, it is measurable, and it is the number to
watch if inspection ever grows beyond a digest.

## What is not measured here

- **End-to-end client-observable latency.** The relay rig
  (`tools/relay-rig`) proves a real client cannot tell the difference in
  *behaviour*; it records no timings. Adding them there would need a real
  client in the loop and is the honest way to answer "does a user notice".
- **Anything on Linux or Windows.** Every figure above is darwin/arm64.
- **A cold or locked keychain**, for the reason given above.
- **Sustained load or a large existing journal.** `Append` is measured against
  a fresh database; SQLite's behaviour as a file grows and WAL checkpoints is
  a different question and is not answered here.
