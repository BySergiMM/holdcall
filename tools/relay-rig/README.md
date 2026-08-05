# Relay rig

The acceptance test for the one thing Nim must never get wrong: being invisible.

It drives a real MCP client twice against the same server — once directly, once
through `nim serve` — and compares everything the client can observe: the tool
list, the JSON schemas, unicode round-trips, a 512 KiB payload, error
propagation and ping. Any difference is a failure.

```bash
cd engine && go build -o bin/nim ./cmd/nim && cd ..
pip install fastmcp
python tools/relay-rig/compare.py
```

Expected last line: `RESULT: IDENTICAL`.

`NIM_BINARY` overrides the binary under test.

## Why a real client

Unit tests prove the framing is byte-exact. Only a real client proves the
*client* cannot tell. Two of the three bugs found in M1 — a socket path over the
AF_UNIX limit, and failed tool calls recorded as successful — were invisible to
unit tests and obvious here.
