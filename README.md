# Nim

A local process that sits between an MCP client (Claude Code, Cursor) and the
MCP servers it uses. It holds the credentials, decides whether each
`tools/call` is allowed, and records every decision in a hash-chained journal.

Nothing else in the protocol is touched: everything that is not a `tools/call`
is forwarded byte for byte.

## Status

Pre-alpha, and honest about it. What works:

- **A relay a real client cannot distinguish from a direct connection.** Tool
  list, JSON schemas, unicode, a 512 KiB payload, error propagation and ping
  all pass through unchanged. The harness is `tools/relay-rig`.
- **Refusal.** A `tools/call` is decided before it is forwarded, and the
  decision is written to the journal before the relay acts on it. A refused
  call never leaves Nim.
- **An append-only, hash-chained record.** `nim log` shows what happened,
  `nim verify` walks the chain, `nim console` serves a read-only local view.
- **Credentials out of the client's config file**, into the OS credential
  store, injected only into the one server each is registered for.

What does not exist yet: agent identity, budgets, human approval, any policy
model beyond a list of tool names, and the hosted dashboard. `docs/milestones.md`
is the order those come in.

## Failing closed

Every way of not getting a decision is a denial: no daemon, a slow daemon, a
closed socket, an answer to a different question, a journal write that failed.
There is no path on which a `tools/call` reaches a server without a decision
behind it.

The one thing that is deliberately *not* a denial is the absence of
configuration. A connector nobody set up is not a failure, and those servers
keep working untouched. `docs/decisions/0001-failure-behaviour.md` sets out
why those are different questions.

## Credentials

A connector names the one downstream server its secret may be injected into.
That binding is what authorizes the credential, so it is required:

```bash
echo "$GITHUB_TOKEN" | nim connector set github --env GITHUB_TOKEN \
    -- npx -y @modelcontextprotocol/server-github

nim serve --connector github        # runs the registered server, with the token
```

The secret is never a command-line argument and never reaches SQLite. A command
given on the command line is ignored when a connector supplies one: the daemon
decides what receives a credential, not the caller.

Both ends of the daemon socket verify each other by peer identity, so neither
a process pretending to be a shim nor one pretending to be the daemon gets in.

## What the record is, and is not

The journal is append-only and hash-chained, and `docs/journal-format.md` is
the normative definition of the encoding, the chain, and — importantly — what
the chain does not protect against.

The short version: it detects corruption, partial writes and edits made by
anything that does not know the chain exists. It does **not** detect a local
attacker who can write to `nim.db`, because nothing in the construction is
secret and the whole chain can be recomputed. Only a head recorded elsewhere
(`nim verify --expect-head`) covers that.

Do not describe it as tamper-proof, tamper-evident, or an immutable audit log.

## Shape

```
engine/    Go. The daemon, the shim, the journal and the console.
```

The engine keeps working with no network. A hosted dashboard is planned (M8)
and does not exist.

## Building

```bash
cd engine && go build -o bin/nim ./cmd/nim
```

Go 1.25 or newer. No C toolchain: the SQLite driver is pure Go, so the engine
cross-compiles for darwin, linux and windows with `CGO_ENABLED=0`.

Everything has been run on darwin/arm64. Linux is exercised in CI; **no code
in this project has ever been run on Windows**, only cross-compiled, and two
of its security properties are known not to hold there — see
`docs/milestones.md`.

## Performance

`docs/benchmarks.md`, with the commands that reproduce every figure. The
decision a call waits for costs p99 0.27 ms including the durable write.

## Where the project actually stands

`dashboard/` builds a page listing every guarantee alongside what it does *not*
guarantee, every attack anyone has tried against Nim including the ones that
worked, and every known weakness. It is generated from this repository and
refuses to build if a claim cites a test that does not exist — which is how it
avoids becoming another document that drifts from the code. `docs/dashboard.md`
explains the design and, more usefully, what it deliberately cannot show.

    cd dashboard && npm install && npm run check && npm run dev

It shows no runtime state and never will: the journal, sessions and enrolled
agents live on the machine running Nim. `nim console` serves those over
loopback.

## Licence

MIT.
