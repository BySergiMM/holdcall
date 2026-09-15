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

## Python version

fastmcp (every release, checked back to 0.1.0) requires Python >=3.10. This
repository has no other Python dependency to pin a version for, so the rig
keeps its own virtualenv at `tools/relay-rig/.venv/` (gitignored) rather than
assume `python3` on PATH is new enough. CI creates this venv with
`actions/setup-python` pinned to 3.12; locally, point the `venv` step above at
whatever `python3.10+` you have.

## The must-match cases

Tool list, JSON schemas, a unicode round-trip, a 512 KiB payload, a tool that
raises, and `ping`. `Client(..., mode="legacy")` pins the classic `initialize`
handshake for both runs: the installed SDK also speaks a newer
`server/discover` negotiation by default (`mode="auto"`), under which every
result carries a `resultType` discriminator that Nim's own hand-written
denial responses (`engine/internal/mcp/deny.go`) predate and do not carry —
under that mode a client cannot even parse Nim's refusal, and raises a local
`ValidationError` instead of the `ToolError` the refusal is supposed to read
as. That gap is real; widening the wire format is a protocol decision the CI
task that added this comparison is not the place to make unilaterally, so the
rig pins the handshake version Nim was actually built and tested against —
`2025-06-18`, the same one `engine/cmd/nim/e2e_test.go` uses — and this is
recorded as an open issue rather than silently worked around.

## The must-differ cases

**A tool a rule denies.** `dangerous_tool` succeeds when called directly, so a
refusal through Nim proves Nim stopped it, not that the server had nothing to
say. The rig runs `nim policy deny dangerous_tool` against its own daemon
before comparing. Through Nim the call must come back as a tool error whose
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
