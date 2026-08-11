# Nim

A local process that sits between an MCP client (Claude Code, Cursor) and the MCP
servers it uses. It holds the credentials and records every `tools/call`.

Nothing else in the protocol is touched: everything that is not a `tools/call`
is forwarded byte for byte.

## Status

Pre-alpha, and on this branch still an observer. Nim relays, records, and holds
credentials; it blocks nothing, evaluates no policy and asks no human anything.
`docs/milestones.md` is the order things are being built in, and says plainly
which of them exist.

What works today:

- **A relay a real client cannot distinguish from a direct connection.** Tool
  list, JSON schemas, unicode, a 512 KiB payload, error propagation and ping
  are all byte-identical through Nim. The harness is in `tools/relay-rig`.
- **A local record of every `tools/call`** in SQLite: tool name, an argument
  digest, outcome and duration. Arguments themselves never leave the machine.
- **Credentials out of the client's configuration file** and into the OS
  credential store, injected into one registered server at spawn time.

What does not work yet: nothing is blocked, there is no policy model, no agent
identity, no budget, no approval, and no dashboard.

## Credentials

A connector names the one downstream server its secret may be injected into.
That binding is what authorizes the credential, so it is required:

```bash
echo "$GITHUB_TOKEN" | nim connector set github --env GITHUB_TOKEN \
    -- npx -y @modelcontextprotocol/server-github

nim serve --target github        # runs the registered server, with the token
```

The secret is never a command-line argument and never reaches SQLite. Peer
verification keeps processes that are not Nim off the daemon socket entirely.

Read `docs/milestones.md`'s M3 section before relying on any of that: it sets
out what these checks do and do not establish, including one they were
previously described as making and did not.

## What the record is, and is not

The journal is a local SQLite record of what the relay observed. It has no
hash chain on this branch, no signatures and no external anchor, so it detects
nothing about a local process that edits it directly. Reporting is
asynchronous, so a call that was seen is not guaranteed to have been written.

Do not describe it as tamper-proof, tamper-evident or an immutable audit log.

## Shape

```
engine/    Go. The local daemon and the shim. All authorization happens here.
```

The engine keeps working with no network. A dashboard is planned (M8) and does
not exist yet.

## Building

```bash
cd engine && go build -o bin/nim ./cmd/nim
```

Go 1.25 or newer. No C toolchain: the SQLite driver is pure Go, so the engine
cross-compiles for darwin, linux and windows with `CGO_ENABLED=0`.

## Licence

MIT.
