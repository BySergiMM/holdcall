# Nim

A local process that sits between an MCP client (Claude Code, Cursor) and the MCP
servers it uses. It holds the credentials, decides whether each `tools/call` is
allowed, blocked or needs a human, and records every decision.

Nothing else in the protocol is touched: everything that is not a `tools/call`
is forwarded byte for byte.

## Status

Pre-alpha. The relay and the journal work: every `tools/call` is recorded, and
a real MCP client cannot tell it is going through Nim. Nothing is blocked,
budgeted or approved yet -- see `docs/milestones.md` for what is being built
and in what order.

## Shape

```
engine/    Go. The local daemon and the shim. All authorization happens here.
web/       Next.js dashboard. Reads history; never participates in a decision.
```

The engine keeps working with no network. The dashboard is a viewer.

## Licence

MIT.
