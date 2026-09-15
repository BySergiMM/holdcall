# Relay rig

The acceptance test for the one thing Nim must never get wrong: being invisible.

It drives a real MCP client against the same server twice — once directly,
once through `nim serve` — and compares everything the client can observe:
the tool list, the JSON schemas, unicode round-trips, a 512 KiB payload,
error propagation and ping. Everything must match, with two named exceptions
that must differ in exactly one way each: a tool a policy rule denies, and a
frame whose JSON object names `method` twice.

```bash
cd engine && go build -o bin/nim ./cmd/nim && cd ..

# fastmcp requires Python >=3.10; if python3 on PATH is older, point this at
# a newer interpreter instead (see "Python version" below).
python3 -m venv tools/relay-rig/.venv
tools/relay-rig/.venv/bin/pip install fastmcp==4.0.3

tools/relay-rig/.venv/bin/python tools/relay-rig/compare.py
```

Expected last line: `RESULT: IDENTICAL`. That covers both halves: every MATCH
row above it matched, and every must-differ case below it differed exactly as
required. `NIM_BINARY` overrides the binary under test.

The rig starts its own `nim daemon` (over its own `NIM_HOME`, at
`tools/relay-rig/nim-home/`, gitignored) before comparing, denies one tool
through it with `nim policy deny`, and stops that daemon again once done —
whether or not the comparison passed.

Every fastmcp transport is closed before the daemon is stopped. fastmcp keeps
the relay subprocess alive after the client context exits (`keep_alive`
defaults to true) and only ends it when the transport is closed or the event
loop is torn down; a relay that outlives the daemon reports, correctly, that
its `session.end` went unrecorded. That report is Nim working as designed,
and the rig's job is not to provoke it.

## Python version

fastmcp (every release, checked back to 0.1.0) requires Python >=3.10. This
repository has no other Python dependency to pin a version for, so the rig
keeps its own virtualenv at `tools/relay-rig/.venv/` (gitignored) rather than
assume `python3` on PATH is new enough. CI creates this venv with
`actions/setup-python` pinned to 3.12; locally, point the `venv` step above at
whatever `python3.10+` you have.

## The must-match cases

Tool list, JSON schemas, a unicode round-trip, a 512 KiB payload, a tool that
raises, and `ping`, all under `Client(..., mode="legacy")`, the classic
`initialize` handshake. The client's default mode is exercised by the
must-differ case below rather than here: the MATCH cases compare what the
client observed on each side, and what they observe does not depend on the
handshake, while the refusal does.

## The must-differ cases

**A tool a rule denies, under both handshakes.** `dangerous_tool` succeeds
when called directly, so a refusal through Nim proves Nim stopped it, not
that the server had nothing to say. The rig runs `nim policy deny
dangerous_tool` against its own daemon before comparing, and runs this case
twice: under `mode="legacy"` (`initialize`) and under the client's default
`mode="auto"`, which since fastmcp 4 negotiates `server/discover` and the
2026-07-28 revision, where every result carries a `resultType` the client
validates strictly. A refusal is the one message Nim writes itself, so it is
the one place a dialect mismatch can hide: before Nim answered in the
negotiated dialect, a client in the default mode raised a local
`ValidationError` on the refusal instead of the `ToolError` it is meant to
read as (F-021, fixed 2026-09-15). Through Nim the call must come back as a tool error whose
text is exactly `mcp.DeniedByPolicy`: `"Nim denied this call by policy. Do not
retry automatically."` — it names Nim, and it says not to retry.

**A frame with a repeated `method` key.** `{"...,"method":"ping","method":
"tools/call",...}` is valid JSON; Python's own parser (like JavaScript's)
keeps the last value and answers it as an ordinary `tools/call`, which is what
the direct run gets back. Through Nim, `internal/mcp.Classify`'s exact-key
reader refuses the frame outright — not a JSON-RPC error, not a tool result,
nothing at all on that id, because which of the two methods to answer under is
the very question it cannot answer safely. The rig sends this frame over a
raw hand-driven session (fastmcp's `Client` cannot construct a literal
duplicate key) and asserts silence, then sends one more ordinary call on the
same connection to prove the relay is still alive rather than dead.

## Why a real client

Unit tests prove the framing is byte-exact. Only a real client proves the
*client* cannot tell. Two of the three bugs found in M1 — a socket path over the
AF_UNIX limit, and failed tool calls recorded as successful — were invisible to
unit tests and obvious here.
