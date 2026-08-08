# Nim

A local process that sits between an MCP client (Claude Code, Cursor) and the MCP
servers it uses. It holds the credentials, decides whether each `tools/call` is
allowed, blocked or needs a human, and records every decision.

Nothing else in the protocol is touched: everything that is not a `tools/call`
is forwarded byte for byte.

## Status

Pre-alpha, and so far only an observer. Nim relays and records; it blocks
nothing, holds no credentials and evaluates no policy. The paragraph above is
where this is going, not where it is. See `docs/milestones.md` for the order.

What does work: `tools/call` is recorded in a local append-only, hash-chained
journal. `nim log` shows what happened, `nim verify` walks the chain, `nim
status` prints its head.

Reporting is asynchronous, so a call that was seen is not guaranteed to have
been written, and Nim reports only the losses that leave evidence behind.
`docs/journal-format.md` is explicit about that, and about what the chain does
and does not detect. It is short, and worth reading before relying on either.

## Shape

```
engine/    Go. The local daemon and the shim. All authorization happens here.
web/       Next.js dashboard. Reads history; never participates in a decision.
```

The engine keeps working with no network. The dashboard is a viewer.

## Licence

MIT.
