# Getting started

This walks through pointing a real MCP client at Nim, checking that it
worked, giving one server a credential, enrolling the client program, and
writing a policy rule. Everything below is a real command and real output —
nothing here is aspirational.

Build or install `nim` first (see the README's *Install*), then start from a
shell with that binary on your PATH.

## The three clients, and what `nim init` looks at

`nim init` (no `--write`) discovers every client's config file and prints
what it would change, without touching anything:

```bash
nim init
```

| Client | Config file | Notes |
|---|---|---|
| Claude Code | `~/.claude.json` | Also rewrites `projects.<path>.mcpServers` for every project section inside it, and `.mcp.json` in your current directory if one exists there. |
| Cursor | `~/.cursor/mcp.json` | |
| Claude Desktop | `~/Library/Application Support/Claude/claude_desktop_config.json` on macOS (`%AppData%\Claude\claude_desktop_config.json` on Windows, `$XDG_CONFIG_HOME/Claude/...` or `~/.config/Claude/...` on Linux) | |

A file that doesn't exist is reported as `not found`, not an error — Nim
doesn't assume you have all three clients installed. `--client claude-code`,
`--client cursor` or `--client claude-desktop` restricts a run to one of
them.

## A real before and after

For every stdio `mcpServers` entry, `nim init` replaces `command` with the
absolute path to the `nim` binary and moves the original command and its
arguments after `--`, so Nim can spawn exactly what the client used to spawn
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

and after `nim init --write`:

```json
{
  "mcpServers": {
    "github": {
      "command": "/path/to/nim",
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
alone: there is nothing for Nim to spawn or relay for those.

Running `nim init` again against an already-wrapped entry reports
`already through Nim` and changes nothing; the command is idempotent.

```bash
nim init --write
```

Backs up every file it is about to change first, then writes it, and prints
the backup path for each — something like
`~/.cursor/mcp.json.nim-backup-20260915T072321Z`.

```bash
nim init --undo ~/.cursor/mcp.json.nim-backup-20260915T072321Z
```

Restores exactly that one file from exactly that backup. Nothing else is
touched, and nothing is inferred about which file a backup belongs to beyond
what its own path says.

## Verify: `nim doctor`

```bash
nim doctor
```

One line per check, in order: the `nim` binary on PATH, `config.toml`, the
daemon, `machine-id`, the journal's chain, the OS credential store,
enrolments, rules and connectors, and every client config `nim init` knows
about — each `OK`, `WARN` or `FAIL`. A `WARN` is not a clean install, but it
is not what "exit 1" means here; that is reserved for a `FAIL`, something
that must be fixed before Nim can be trusted at all. Run it again after
anything below to see the effect.

## Set a credential: `nim connector set`

```bash
echo "$GITHUB_TOKEN" | nim connector set github --env GITHUB_TOKEN \
    -- npx -y @modelcontextprotocol/server-github
```

The secret is read from **stdin**, never typed as a command-line argument —
argv is visible to anything that can list processes (`ps`) on the same
machine, and survives in shell history for as long as that file does.
Piping it in with `echo "$VAR" | nim connector set ...` keeps it out of
both. The command after `--` is what authorizes the secret: it is the one
downstream server this credential may be injected into, and it is required
for exactly that reason — a stored secret with no statement about what may
receive it is a secret anything registering a connector could redirect.

## Enrol the client program: `nim agent add`

```bash
nim agent add cursor /Applications/Cursor.app/Contents/MacOS/Cursor
nim agent add claude-desktop "/Applications/Claude.app/Contents/MacOS/Claude"
nim agent list
```

An agent is identified by the file the kernel reports for the process that
spawned the relay — not by a name the client sends, which nothing on the
wire carries anyway. For a macOS `.app`, that file is the one inside
`Contents/MacOS/`, as above.

**Claude Code is not a single fixed binary the way an `.app` bundle is.**
Depending on how it was installed, the `claude` command you type is a
version-management symlink to a versioned binary, or an npm-installed shim
that execs `node` against a JS entry point — either way, the file Nim
actually needs is whatever the OS resolves at the end of that chain, not
the name `claude`. Rather than guess it, enrol with your best guess, then
check what Nim actually resolved:

```bash
nim agent list
```

`nim agent list` and `nim doctor` are the source of truth for what got
enrolled, including whether it is still current. Because both of the
schemes above change the underlying file on every update — a new symlink
target, a new shim — a self-updating client is exactly the case *A STALE
enrolment* below describes, and it is worth expecting rather than being
surprised by.

Two windows of the same program are the same agent, because they run the
same file. Enrolling one is as privileged as running Nim itself: anything
able to execute the `nim` binary as this user can enrol an agent or replace
an existing one.

## A first deny rule

```bash
nim policy deny <tool-name>
```

Refuses that tool for every agent, on every connector, from now on. Make the
same call from your client: it comes back as a tool error, because the call
never reached the server that would have run it. Then:

```bash
nim log
```

shows the call with `deny` and no result — a refused call never gets one.

## A default deny, and an agent-scoped allow

The rule above is a denylist: everything except that one tool is still
allowed. To flip that — nothing is allowed except what you name — combine a
default with a narrower allow:

```bash
nim policy default deny
nim policy allow <tool-name> --agent cursor
```

`default deny` denies every tool for every agent, enrolled or not. The
second rule is more specific (it names both a tool and an agent), so it
wins for `cursor`'s sessions alone; anything unenrolled meets only the
default and is denied. `nim policy explain <tool-name> --agent cursor` says
which rule would decide a call shaped like that, and why, without your
having to work out the precedence by hand:

```bash
nim policy explain <tool-name> --agent cursor
```

To have a human decide a call instead of a rule, hold it:

```bash
nim policy ask <tool-name>           # the call waits, up to [daemon] approval_timeout (2m by default)
nim approve                          # every held call, with its real arguments
nim approve <id>                     # or: nim reject <id> --reason "why"
```

## Reading the record

```bash
nim log                              # calls, newest first
nim console                          # journal, sessions and policy, at http://127.0.0.1:7717 (loopback, read-only)
                                     # /#journal, /#sessions and /#policy open a tab directly
nim verify                           # walk the chain and report whether it's self-consistent
nim verify --expect-head <hash>      # also check nothing before that head was rewritten
```

`nim verify` alone shows corruption and edits that didn't recompute the
chain, but not a rewrite that recomputed it correctly — nothing in the chain
is secret, so anyone who can write `nim.db` can do that. `nim status` and
`nim verify` both print the current head; record it somewhere else (a
password manager entry, a note, a second machine) and pass it back with
`--expect-head` later to catch a chain that has been quietly shortened and
rebuilt since.

## What to do when

**The daemon isn't running.** `nim doctor` reports it as `WARN`, not `FAIL`
— it starts on demand the next time a client spawns `nim serve`, or run
`nim daemon` yourself to start it now.

**An enrolment is `STALE`.** `nim agent list` marks it and names the path;
this means the file there is no longer the one that was enrolled, which
happens whenever a self-updating client replaces its own binary or a
version-manager symlink moves. It is not evidence of tampering. Fix it the
same way you enrolled it:

```bash
nim agent add <name> <path>
```

**`config.toml` has a stale `[policy]` section.** `nim doctor` (and every
other command) refuses to load the file and says why: policy hasn't lived
in `config.toml` since M4, only in SQLite, changed with `nim policy`. Add
each tool from the old list with `nim policy deny <tool>`, then delete the
`[policy]` section from `config.toml`.

**A client still isn't wrapped.** `nim doctor` reports how many of its
servers go through Nim and how many don't, and says to run `nim init` to
fix it. If a run of `nim init` reports `not found` for a client you do have
installed, its config may be somewhere nonstandard; pass `--config <path>
--client <name>` to point at it directly, or edit the file by hand as
`nim init`'s own closing instructions describe.
