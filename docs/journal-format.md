# Journal format

The journal is append-only and hash-chained. This document is the normative
definition of how an entry is encoded and how the chain is built, so that a
second implementation reproduces the same bytes and the same hashes.

Read `## What this does not protect against` before describing the journal to
anyone.

## Entry

One row of `nim_journal`. Entries are never modified after they are written: a
call produces two entries, `call.request` and `call.outcome`, not one row that
is updated.

| # | Field | Type | Present on |
|---|---|---|---|
| 1 | `chain_seq` | int | every entry |
| 2 | `schema_version` | int | every entry |
| 3 | `kind` | string | every entry |
| 4 | `session_id` | string | every entry |
| 5 | `seq` | int, nullable | `call.request`, `call.outcome` |
| 6 | `connector` | string, nullable | `session.start` |
| 7 | `tool` | string, nullable | `call.request` |
| 8 | `params_digest` | string, nullable | `call.request` |
| 9 | `decision` | string, nullable | `call.request` |
| 10 | `ok` | bool, nullable | `call.outcome` |
| 11 | `duration_ms` | int, nullable | `call.outcome` |
| 12 | `anomaly` | string, nullable | `anomaly` |
| 13 | `occurred_at` | string | every entry |
| 14 | `machine_id` | string, nullable | `session.start` |
| 15 | `client` | string, nullable | `session.start` |
| 16 | `protocol_version` | string, nullable | `session.end` |

`prev_hash` and `hash` are stored alongside but are **not** encoded: they are the
chain, not the content.

`kind` is one of `session.start`, `call.request`, `call.outcome`, `session.end`,
`anomaly`.

`decision` is one of `observed`, `allow`, `deny`, `approved`, `rejected`. M2
writes `allow` and `deny`. `observed` is what earlier milestones wrote, when
nothing was authorized at all, and it is still what those entries hold — which is
why enforcement needed no migration. `approved` and `rejected` belong to human
approval and are not written yet. See *What a decision means* below.

`anomaly` is one of `batch`, `malformed_json`, `framing`, `duplicate_id`.

Fields absent for a kind are NULL, and NULL is encoded distinctly from an empty
string.

Two placements are worth explaining. `protocol_version` sits on `session.end`
because the negotiated version is only known once `initialize` has been
answered, which is after `session.start` has already been written; a session
that is interrupted therefore does not record one. And an `anomaly` entry
carries no `seq`, because the per-session sequence counts calls, and lending an
anomaly one of those numbers would read as a missing call later.

### Why the outcome entry does not repeat `tool` and `params_digest`

It is redundant with the `call.request` that shares its `(session_id, seq)`, and
two immutable entries that both describe one call can contradict each other. A
join cannot. `connector` is on `session.start` for the same reason.

## canonical_encode_v1

```
canonical_encode_v1(entry) -> bytes
```

    output := "nim.journal.v1" 0x0A || field(1) || field(2) || ... || field(16)

Fields appear in the numeric order of the table above. Never sorted, never
omitted: an absent field is encoded as null, so the output always has exactly
sixteen fields.

Each field is:

    field := tag(1 byte) || length(8 bytes, big-endian unsigned) || payload

| Tag | Meaning | Length | Payload |
|---|---|---|---|
| `s` (0x73) | string | byte length of the payload | the string's UTF-8 bytes |
| `i` (0x69) | integer | always 8 | int64, big-endian, two's complement |
| `b` (0x62) | boolean | always 1 | `0x00` false, `0x01` true |
| `n` (0x6E) | null | always 0 | empty |

The tag and the length prefix are what make the encoding unambiguous: no two
distinct entries can produce the same bytes, and no field value can be mistaken
for a separator, because there are no separators.

### Strings

Encoded as the exact UTF-8 bytes held in the column. No escaping, no trimming,
no case folding, and **no Unicode normalisation**.

Normalisation is deliberately absent. It belongs to the decision path — where
two spellings of one repository name must not resolve differently — and NIM does
not decide anything yet. Normalising here would mean the journal recorded
something other than what was observed, which is the opposite of what a journal
is for.

### Integers

int64, big-endian, two's complement. Fixed 8-byte width, so `0`, `-1` and
`2^40` all encode to eight bytes. There is no decimal form and therefore no
question about leading zeros, signs or separators.

### Booleans

One byte, `0x00` or `0x01`. No other value is valid.

### Null

Tag only, with a zero length. `null`, `""` and `0` are three different
encodings, so a NULL column and an empty string never collide.

### Timestamps

`occurred_at` is a string, encoded as a string. It is written as RFC3339 with
nanoseconds in UTC. The encoder does not parse or re-format it: whatever bytes
are in the column are what get hashed.

### What canonical_encode_v1 is not

It is **not** a JSON canonicaliser. It takes a fixed list of sixteen typed
scalars, so questions about object key ordering, duplicate keys, batches and
malformed JSON do not arise here — an entry has no keys and no nesting.

Those questions are real, but they belong to two other places:

- **Anomaly detection** — batch, malformed JSON and framing are counted and
  recorded as `anomaly` entries, and from M2 the frames that could hide a
  `tools/call` are refused rather than relayed. They are not canonicalised.
  This is not the full set of JSON ambiguities, and it is not meant to be. In
  particular **duplicate object keys are not detected**: `{"name":"a","name":"b"}`
  is accepted by every parser involved, each picks one, and Go picks the last —
  so the journal records `b` while a server whose parser prefers the first would
  act on `a`. Noticing that requires parsing strictly and deciding what the
  message *is*, which is the same work as canonicalising the request, and
  belongs with it. The standard library gives no signal that a duplicate was
  seen, so there is no cheap detection to bolt on in the meantime.
- **Request canonicalisation (not in this milestone)** — when NIM starts
  deciding, the message it authorises must be the message it emits, which needs
  JCS-style canonical JSON with NFC and rejection of duplicate keys. That work
  belongs with enforcement, because canonicalising a request NIM merely forwards
  would change bytes for no benefit.

`params_digest` is therefore still `sha256` over the raw `params.arguments`
bytes as they arrived. Digests written under `schema_version` 1 are **not**
comparable with digests written under any later version that canonicalises
first. The version field is what makes that safe.

## Chain

    genesis        := sha256("nim.journal.v1.genesis" 0x0A || machine_id)
    hash(n)        := sha256(raw_bytes(prev_hash(n)) || canonical_encode_v1(entry n))
    prev_hash(1)   := genesis
    prev_hash(n)   := hash(n-1)

`raw_bytes` is the 32-byte decoding of the hex-encoded previous hash, not its
hex text. Hashes are stored as lowercase hex.

`chain_seq` starts at 1 and increases by exactly one per entry, with no gaps.
Because `chain_seq` is itself encoded, an entry cannot be moved without
invalidating its hash.

The genesis is per-install, so two installs do not produce the same chain from
the same entries and their journals stay distinguishable when they eventually
share one table.

`nim verify` checks, in order: contiguity of `chain_seq` from 1; that every
`schema_version` is known; that each `prev_hash` equals the previous entry's
`hash`; and that each `hash` equals the recomputation above. It reports the
first `chain_seq` that fails.

The one check it may skip is the first: without `machine_id` there is no genesis
to compare entry 1 against, so it is left unchecked and the result says so.
Everything from entry 2 on is verified as usual.

## What the chain does not say

The chain covers each entry's contents and its position. It says nothing about
whether the entries make sense together.

A journal can be entirely self-consistent and still contain a `call.outcome`
with no `call.request`, a `session.end` for a session that never started, or
timestamps that run backwards. Every hash checks out, because every hash only
ever claimed that *that entry* is as it was written.

Semantic coherence is a separate property, and this milestone does not check it.
Two things rein it in a little: in normal operation the daemon is the only thing
appending, and `(session_id, seq, kind)` is unique, so one call cannot be
recorded twice under one sequence number. The first is a convention rather than
a constraint — nothing stops another process opening the file and appending —
and neither is a proof about the set as a whole.

## What the record can and cannot tell you about losses

An event passes through three states, and only the last leaves a record:

1. **Observed** — the shim saw a message go past.
2. **Accepted** — the daemon received the report, over a socket that carries no
   answer back.
3. **Written** — the entry is in the journal.

Losses between 1 and 2 usually leave a shape, and the shim always says so on
stderr as it happens. Afterwards the journal shows a session that never ended,
or a per-session `seq` that skips, and `nim status` reports both.

Usually, not always: if the daemon was never reachable at all, nothing was
written, so there is no session to be unfinished and no sequence to skip. That
run leaves no trace in the journal — only the warning the shim printed at the
time, and `nim status` reporting that the daemon is not running.

**Losses between 2 and 3 leave nothing, except for `call.request`.** If the
daemon accepts an event and then cannot write it, the shim is never told and the
journal has no trace, so nothing can tell that case apart from one where no event
was ever sent. The daemon records the failure in its own log, and that is the
only place it appears.

The exception is the one kind that is answered. From M2 the shim waits for the
daemon's decision before forwarding a `tools/call`, and the daemon answers only
after the entry is written — so a request the daemon accepted and failed to write
comes back as a denial rather than vanishing. `session.start`, `call.outcome`,
`session.end` and `anomaly` are still one-way and still have the blind spot.

The same blind spot covers events lost at the very end of a session that then
closes normally: with the highest `seq` gone too, there is nothing left for the
gap check to find a hole in.

So, precisely:

> Nim records the events it observed, and reports the losses that leave
> detectable evidence in the journal. A `call.request` that was not written
> cannot have been forwarded. For every other kind, reporting is asynchronous and
> one-way, so a failure on the daemon's write path can be indistinguishable from
> no event having been sent.

Do not read `gaps: none of the detectable kinds found` as `no calls were lost`.

## What a decision means, and what it does not

From M2 a `call.request` carries the decision the daemon reached: `allow` or
`deny`. Entries written before then carry `observed`, which recorded that nothing
had been decided at all.

One direction holds:

> A call that reached a connector is a call this journal recorded, with the
> decision that allowed it.

The converse does not. **An `allow` is not evidence the call was made.** The
relay gives up after two seconds and SQLite's busy timeout is five, so a heavily
contended write can be committed after the relay has already denied the call and
answered the client. Nothing distinguishes that entry from a call still running:
both are an `allow` with no `call.outcome`, and both read as `pending`.

**A refusal produces one entry, not two.** There is no `call.outcome` for work
that never happened, and its absence is not a gap. A reader deriving state from
these entries should treat `deny` with no outcome as refused, and `deny` with an
outcome as inconsistent — nothing should produce the latter.

**Some refusals are not here at all.** When the daemon cannot be reached the
relay denies the call locally, and the only thing that can write to this journal
is exactly what could not be reached. Those refusals are counted as lost events
and printed on stderr by the relay; the journal never learns of them. So
`decision = deny` in this file always means a policy refusal, never an inability
to decide.

## What this does not protect against

The chain detects corruption, partial writes, and modification by anything that
does not know the chain exists. That is worth having and it is all that is
being claimed.

**It does not detect a local attacker who can write to `nim.db`.** Nothing in
this construction is secret, so anyone able to edit a row is equally able to
recompute every hash from the genesis onwards, and the result verifies cleanly.
Truncating the tail and recomputing is likewise undetectable.

On durability, for the record: these connections run with `synchronous=FULL`,
so SQLite flushes the write-ahead log before reporting a commit, and a power cut
should not drop entries it had already accepted — as far as the hardware honours
the flush. That value is SQLite's own default rather than something chosen here,
which is a thin thing for a durability property to rest on; pinning it belongs
with the milestone that decides what a commit on the critical path may cost.

The seed is not part of the journal. If `machine-id` goes missing, the first
entry cannot be checked against anything, and `nim verify` says so rather than
reporting a mismatch: missing verification material is not evidence that the
thing being verified is wrong. Everything from the second entry on is still
checked, and restoring the file restores the rest.

Two things would change that, and neither is in this milestone:

- **Recording the head elsewhere.** `nim status` prints the head hash and the
  chain length, and `nim verify --expect-head <hash>` checks against a head you
  recorded earlier. Anything committed before that head cannot be rewritten
  without the mismatch showing. This is manual on purpose: automatic anchoring
  needs somewhere off the machine to anchor to.
- **Owning the file as another OS user.** If the journal belongs to a principal
  the agent is not, integrity stops depending on hashes and starts depending on
  the kernel, which is a much better place for it.

Do not describe this journal as tamper-proof, tamper-evident, or an immutable
security log. It is a hash-chained append-only record with the limits above.
