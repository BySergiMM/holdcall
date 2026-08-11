# Nim

A local process that sits between an MCP client (Claude Code, Cursor) and the MCP
servers it uses. It holds the credentials, decides whether each `tools/call` is
allowed, blocked or needs a human, and records every decision.

Nothing else in the protocol is touched: everything that is not a `tools/call`
is forwarded byte for byte.

## Status

Pre-alpha. Nim can now refuse a `tools/call`, but it holds no credentials, has
no policy model beyond a list of tool names, and asks no human anything. The
paragraph above is where this is going, not where it is. See
`docs/milestones.md` for the order.

What does work: a `tools/call` is decided before it is forwarded, and the
decision is written to a local append-only, hash-chained journal before the
relay acts on it. A call that reached a server is a call the journal recorded.
The reverse does not follow — a recorded `allow` is not evidence the call was
made. Everything that is not a `tools/call` is forwarded byte for byte, as
before. `nim log` shows what happened, `nim verify` walks the chain, `nim
status` prints its head, `nim console` serves a read-only view.

Refusing is fail-closed: no daemon, a slow one, or an answer that does not match
the question all mean the call does not go through. A refusal Nim could not
record is not in the journal, because the only writer is what could not be
reached.

Reporting for everything other than a `tools/call` is still asynchronous, so
such an event is not guaranteed to have been written, and Nim reports only the
losses that leave evidence behind. `docs/journal-format.md` is explicit about
that, about what a decision means, and about what the chain does and does not
detect. It is short, and worth reading before relying on any of it.

## Shape

```
engine/    Go. The local daemon and the shim. All authorization happens here.
web/       Next.js dashboard. Reads history; never participates in a decision.
```

The engine keeps working with no network. The dashboard is a viewer.

## Licence

MIT.
