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
| Date | 2026-08-11 |
| State | `integration/trunk` |

Unloaded laptop, on battery, no other significant work running. These are not
server numbers and are not claimed to be.

## Decision path — what a `tools/call` waits for

The whole round trip a relay blocks on: write `call.request` to the socket,
evaluate the deny list, append the chained entry to SQLite **and commit it**,
write the decision back, read it. The journal write is included deliberately —
the entry is durable before the answer is sent, and excluding it would measure
something Nim never does.

| Benchmark | p50 | p95 | p99 | max |
|---|---|---|---|---|
| `DecisionRoundTrip` (one relay) | 0.075 ms | 0.166 ms | 0.266 ms | 0.91 ms |
| `DecisionRoundTripContended` (16 relays) | 1.04 ms | 1.41 ms | 1.59 ms | 3.58 ms |
| `DecisionDenied` (one relay, refused) | 0.073 ms | 0.102 ms | 0.125 ms | 0.36 ms |

**What this justifies.** `decisionTimeout` is 2 s — three orders of magnitude
past the worst p99 above. It cannot fire because the daemon is busy, only
because it is wedged or gone, which is what makes fail-closed a safety net
rather than a latency policy.

**Deny is not slower than allow.** It does the same journal write, and the deny
list is a linear scan of exact names. If those diverged it would mean the
policy check had become the expensive part; for a list of exact strings it
must not, and it does not.

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
| `Append` (one writer, committed) | 79 µs/op |
| `AppendContended` (4 writers) | 65 µs/op |
| `CanonicalEncode` (no I/O) | 176 ns/op |
| `Verify` (2000-entry chain) | 2.95 ms |

**The commit dominates.** Encoding an entry costs 176 ns; committing it costs
about 79 µs — 450× more. Hashing is not where the time goes, and any future
argument about the chain being expensive should start here.

**Verification stays cheap but grows.** 2000 entries in 2.95 ms is roughly
1.5 µs per entry, and it is linear: a million-entry journal is about 1.5 s.
That is fine for `nim verify` on demand and would not be fine on every write,
which is why nothing verifies on the write path.

## Relay — the invisibility budget

Every message pays the framing cost; only a `tools/call` pays inspection.

| Benchmark | Result | Throughput |
|---|---|---|
| `RelayFramingSmall` | 8.7 µs / 64 msgs | 13 MB/s |
| `RelayFramingLarge` (512 KiB) | 2.6 ms / 64 msgs | 200 MB/s |
| `RelayInspectSmall` | 2.1 µs | 54 MB/s |
| `RelayInspectLarge` (512 KiB args) | 5.9 ms | 89 MB/s |
| `ArgumentsDigest` 1 KiB | 3.5 µs | 291 MB/s |
| `ArgumentsDigest` 64 KiB | 204 µs | 322 MB/s |
| `ArgumentsDigest` 512 KiB | 1.6 ms | 321 MB/s |

**The digest is the size-proportional cost.** Nothing else Nim does per call
scales with payload size — messages are never re-serialised, only inspected —
so `ArgumentsDigest` is the floor for a large `tools/call`, and it runs at
roughly SHA-256's own speed.

**A 512 KiB tool call costs about 6 ms of inspection.** Against a network round
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
