# Getting started

This walks through pointing a real MCP client at Holdcall, checking that it
worked, giving one server a credential, enrolling the client program, and
writing a policy rule. Everything below is a real command and real output —
nothing here is aspirational.

Build or install `holdcall` first (see the README's *Install*), then start from a
shell with that binary on your PATH.

## The three clients, and what `holdcall init` looks at

`holdcall init` (no `--write`) discovers every client's config file and prints
what it would change, without touching anything:

```bash
holdcall init
```

| Client | Config file | Notes |
|---|---|---|
| Claude Code | `~/.claude.json` | Also rewrites `projects.<path>.mcpServers` for every project section inside it, and `.mcp.json` in your current directory if one exists there. |
| Cursor | `~/.cursor/mcp.json` | |
| Claude Desktop | `~/Library/Application Support/Claude/claude_desktop_config.json` on macOS (`%AppData%\Claude\claude_desktop_config.json` on Windows, `$XDG_CONFIG_HOME/Claude/...` or `~/.config/Claude/...` on Linux) | |

A file that doesn't exist is reported as `not found`, not an error — Holdcall
doesn't assume you have all three clients installed. `--client claude-code`,
`--client cursor` or `--client claude-desktop` restricts a run to one of
them.

## A real before and after

For every stdio `mcpServers` entry, `holdcall init` replaces `command` with the
absolute path to the `holdcall` binary and moves the original command and its
arguments after `--`, so Holdcall can spawn exactly what the client used to spawn
directly. This is the fixture `internal/clientconfig`'s own tests rewrite
(`TestInitWrapsAStdioServer`) — before:

```json
{
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"]
    }
  }
}
```

and after `holdcall init --write`:

```json
{
  "mcpServers": {
    "github": {
      "command": "/path/to/holdcall",
      "args": [
        "serve",
        "--connector", "github",
        "--client", "cursor",
        "--",
        "npx",
        "-y",
        "@modelcontextprotocol/server-github"
      ]
    }
  }
}
```

The connector name (`github`) comes from the `mcpServers` key, and the
`--client` value from which client's file this is. `env` is left exactly as
it was. Anything not stdio — an `http`/`sse` server — is reported and left
alone: there is nothing for Holdcall to spawn or relay for those.

Running `holdcall init` again against an already-wrapped entry reports
`already through Holdcall` and changes nothing; the command is idempotent.

```bash
holdcall init --write
```

Backs up every file it is about to change first, then writes it, and prints
the backup path for each — something like
`~/.cursor/mcp.json.holdcall-backup-20260915T072321Z`.

```bash
holdcall init --undo ~/.cursor/mcp.json.holdcall-backup-20260915T072321Z
```

Restores exactly that one file from exactly that backup. Nothing else is
touched, and nothing is inferred about which file a backup belongs to beyond
what its own path says.

## Verify: `holdcall doctor`

```bash
holdcall doctor
```

One line per check, in order: the `holdcall` binary on PATH, `config.toml`, the
daemon, `machine-id`, the journal's chain, the OS credential store,
enrolments, rules and connectors, and every client config `holdcall init` knows
about — each `OK`, `WARN` or `FAIL`. A `WARN` is not a clean install, but it
is not what "exit 1" means here; that is reserved for a `FAIL`, something
that must be fixed before Holdcall can be trusted at all. Run it again after
anything below to see the effect.

## Set a credential: `holdcall connector set`

```bash
echo "$GITHUB_TOKEN" | holdcall connector set github --env GITHUB_TOKEN \
    -- npx -y @modelcontextprotocol/server-github
```

The secret is read from **stdin**, never typed as a command-line argument —
argv is visible to anything that can list processes (`ps`) on the same
machine, and survives in shell history for as long as that file does.
Piping it in with `echo "$VAR" | holdcall connector set ...` keeps it out of
both. The command after `--` is what authorizes the secret: it is the one
downstream server this credential may be injected into, and it is required
for exactly that reason — a stored secret with no statement about what may
receive it is a secret anything registering a connector could redirect.

## Enrol the client program: `holdcall agent add`

```bash
holdcall agent add cursor /Applications/Cursor.app/Contents/MacOS/Cursor
holdcall agent add claude-desktop "/Applications/Claude.app/Contents/MacOS/Claude"
holdcall agent list
```

An agent is identified by the file the kernel reports for the process that
spawned the relay — not by a name the client sends, which nothing on the
wire carries anyway. For a macOS `.app`, that file is the one inside
`Contents/MacOS/`, as above.

**Claude Code is not a single fixed binary the way an `.app` bundle is.**
Depending on how it was installed, the `claude` command you type is a
version-management symlink to a versioned binary, or an npm-installed shim
that execs `node` against a JS entry point — either way, the file Holdcall
actually needs is whatever the OS resolves at the end of that chain, not
the name `claude`. Rather than guess it, enrol with your best guess, then
check what Holdcall actually resolved:

```bash
holdcall agent list
```

`holdcall agent list` and `holdcall doctor` are the source of truth for what got
enrolled, including whether it is still current. Because both of the
schemes above change the underlying file on every update — a new symlink
target, a new shim — a self-updating client is exactly the case *A STALE
enrolment* below describes, and it is worth expecting rather than being
surprised by.

Two windows of the same program are the same agent, because they run the
same file. Enrolling one is as privileged as running Holdcall itself: anything
able to execute the `holdcall` binary as this user can enrol an agent or replace
an existing one.

## A first deny rule

```bash
holdcall policy deny <tool-name>
```

Refuses that tool for every agent, on every connector, from now on. Make the
same call from your client: it comes back as a tool error, because the call
never reached the server that would have run it. Then:

```bash
holdcall log
```

shows the call with `deny` and no result — a refused call never gets one.

## A default deny, and an agent-scoped allow

The rule above is a denylist: everything except that one tool is still
allowed. To flip that — nothing is allowed except what you name — combine a
default with a narrower allow:

```bash
holdcall policy default deny
holdcall policy allow <tool-name> --agent cursor
```

`default deny` denies every tool for every agent, enrolled or not. The
second rule is more specific (it names both a tool and an agent), so it
wins for `cursor`'s sessions alone; anything unenrolled meets only the
default and is denied. `holdcall policy explain <tool-name> --agent cursor` says
which rule would decide a call shaped like that, and why, without your
having to work out the precedence by hand:

```bash
holdcall policy explain <tool-name> --agent cursor
```

To have a human decide a call instead of a rule, hold it:

```bash
holdcall policy ask <tool-name>           # the call waits, up to [daemon] approval_timeout (2m by default)
holdcall approve                          # every held call, with its real arguments
holdcall approve <id>                     # or: holdcall reject <id> --reason "why"
```

## Reading the record

```bash
holdcall log                              # calls, newest first
holdcall console                          # journal, sessions and policy, at http://127.0.0.1:7717 (loopback, read-only)
                                     # /#journal, /#sessions and /#policy open a tab directly
holdcall verify                           # walk the chain and report whether it's self-consistent
holdcall verify --expect-head <hash>      # also check nothing before that head was rewritten
```

`holdcall verify` alone shows corruption and edits that didn't recompute the
chain, but not a rewrite that recomputed it correctly — nothing in the chain
is secret, so anyone who can write `holdcall.db` can do that. `holdcall status` and
`holdcall verify` both print the current head; record it somewhere else (a
password manager entry, a note, a second machine) and pass it back with
`--expect-head` later to catch a chain that has been quietly shortened and
rebuilt since.

## What to do when

**The daemon isn't running.** `holdcall doctor` reports it as `WARN`, not `FAIL`
— it starts on demand the next time a client spawns `holdcall serve`, or run
`holdcall daemon` yourself to start it now.

**`holdcall status` says `daemon   OLDER BUILD`, or `holdcall doctor` fails `daemon
build`.** You (or an installer) replaced the `holdcall` binary while its daemon
was still running from the old one — a `go build -o` or a fresh install over
it. The old daemon and the new binary are different images at the same path,
so they refuse each other exactly as they would refuse anything else that
isn't a byte-for-byte match; this is not an impostor, and no rule changed.
Fix it with:

```bash
holdcall daemon restart
```

This asks the old daemon to exit, starts a new one from the binary now on
disk, and confirms it answers before returning.

**An enrolment is `STALE`.** `holdcall agent list` marks it and names the path;
this means the file there is no longer the one that was enrolled, which
happens whenever a self-updating client replaces its own binary or a
version-manager symlink moves. It is not evidence of tampering. Fix it the
same way you enrolled it:

```bash
holdcall agent add <name> <path>
```

**`config.toml` has a stale `[policy]` section.** `holdcall doctor` (and every
other command) refuses to load the file and says why: policy hasn't lived
in `config.toml` since M4, only in SQLite, changed with `holdcall policy`. Add
each tool from the old list with `holdcall policy deny <tool>`, then delete the
`[policy]` section from `config.toml`.

**A client still isn't wrapped.** `holdcall doctor` reports how many of its
servers go through Holdcall and how many don't, and says to run `holdcall init` to
fix it. If a run of `holdcall init` reports `not found` for a client you do have
installed, its config may be somewhere nonstandard; pass `--config <path>
--client <name>` to point at it directly, or edit the file by hand as
`holdcall init`'s own closing instructions describe.
