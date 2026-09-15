"""Drive a real MCP client twice: straight to the server, then through Nim.

The relay passes only if two things are both true: every ordinary exchange is
byte-for-byte indistinguishable (the MATCH cases), and the handful of things
Nim is supposed to change come back changed in exactly the documented way
(the DIFFERS cases, asserted precisely rather than merely noticed).

Client(..., mode="legacy") pins the classic `initialize` handshake. The
installed fastmcp/mcp SDK defaults to a newer `server/discover` negotiation
(mode="auto") under which every result -- including a tools/call result -- is
wrapped with a `resultType` discriminator the client validates strictly. Nim's
own denial responses (internal/mcp/deny.go) are hand-written against the
classic result shape and predate that field, so under "auto" mode a client
cannot even parse Nim's refusal: it raises a local pydantic ValidationError
instead of the ToolError the refusal is supposed to read as. That gap is real
and is not this rig's to close -- deny.go is production wire format, and
widening it is a protocol decision, not a CI fix -- so the rig pins the
handshake version Nim was actually built and tested against (the same
"2025-06-18" the Go end-to-end tests use) and leaves the newer negotiation as
an open issue. See the README.
"""

import asyncio
import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

from fastmcp import Client
from fastmcp.client.transports import StdioTransport

RIG = Path(__file__).parent
SERVER = str(RIG / "server.py")
NIM = os.environ.get(
    "NIM_BINARY",
    str(RIG.parent.parent / "engine" / "bin" / ("nim.exe" if os.name == "nt" else "nim")),
)

# The tool a rule denies, for the policy-refusal case below. Real, callable,
# and answers directly -- so a refusal through Nim proves Nim stopped it,
# rather than the server having nothing to say.
DENIED_TOOL = "dangerous_tool"


async def drive(transport, label):
    """Exercise the server and return a comparable snapshot of everything seen."""
    out = {}
    async with Client(transport, mode="legacy") as c:
        tools = await c.list_tools()
        out["tools"] = sorted(t.name for t in tools)
        out["schemas"] = {t.name: json.dumps(t.input_schema, sort_keys=True) for t in tools}

        r = await c.call_tool("echo", {"text": "café ñandú 日本語 \" \\ {} [] emoji: 🚀"})
        out["echo"] = r.data

        r = await c.call_tool("add", {"a": 21, "b": 21})
        out["add"] = r.data

        r = await c.call_tool("big", {"kilobytes": 512})
        out["big_len"] = len(r.data)

        try:
            await c.call_tool("explode", {})
            out["explode"] = "NO ERROR RAISED"
        except Exception as exc:
            out["explode"] = type(exc).__name__

        out["ping"] = await c.ping()
    return out


def run_nim(*args, env, timeout=15):
    """Run the built nim binary for one CLI command and return (ok, stdout)."""
    proc = subprocess.run(
        [NIM, *args], env=env, capture_output=True, text=True, timeout=timeout
    )
    return proc.returncode == 0, proc.stdout + proc.stderr


def start_daemon(env):
    """Start `nim daemon` as a foreground child this process controls, and
    wait for it to answer `nim status` before returning.

    Not StartDaemon()'s own auto-spawn (which detaches and outlives its
    caller by design): the rig needs a handle it can stop deterministically
    once the must-differ cases are done with it, the same way the Go
    end-to-end tests' stack.daemon() does.
    """
    proc = subprocess.Popen(
        [NIM, "daemon"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL
    )
    deadline = time.time() + 10
    while time.time() < deadline:
        ok, out = run_nim("status", env=env, timeout=5)
        if ok and "daemon   running" in out:
            return proc
        if proc.poll() is not None:
            raise RuntimeError("nim daemon exited before it came up")
        time.sleep(0.1)
    proc.kill()
    proc.wait()
    raise RuntimeError("the daemon did not come up within 10s")


def stop_daemon(proc):
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()


class RawSession:
    """A hand-driven MCP stdio session, for the one case fastmcp's Client
    cannot express: a frame whose JSON object names a key twice. A Python
    dict cannot hold a duplicate key, so the frame below is built as a literal
    string rather than json.dumps'd from one.
    """

    def __init__(self, command, args, env):
        self.proc = subprocess.Popen(
            [command, *args],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
            env=env,
        )

    def send(self, raw: str):
        self.proc.stdin.write(raw + "\n")
        self.proc.stdin.flush()

    def read_line(self, timeout: float):
        """One line, or None if nothing arrived within timeout -- the signal
        a refused frame gives: not an error, silence."""
        import select

        r, _, _ = select.select([self.proc.stdout], [], [], timeout)
        return self.proc.stdout.readline() if r else None

    def handshake(self):
        self.send(
            '{"jsonrpc":"2.0","id":1,"method":"initialize",'
            '"params":{"protocolVersion":"2025-06-18","capabilities":{},'
            '"clientInfo":{"name":"relay-rig-raw","version":"0"}}}'
        )
        self.read_line(10)
        self.send('{"jsonrpc":"2.0","method":"notifications/initialized"}')

    def close(self):
        try:
            self.proc.stdin.close()
        except Exception:
            pass
        self.proc.terminate()
        try:
            self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()


async def check_policy_denial(env) -> tuple[bool, str]:
    """MUST DIFFER: a tool a rule denies. Direct, the server just runs it.
    Through Nim, with the rule in place, it must come back as a tool error
    whose text names Nim and says not to retry -- mcp.DeniedByPolicy,
    verbatim, per engine/internal/mcp/deny.go.
    """
    direct = StdioTransport(command=sys.executable, args=[SERVER], env=env)
    async with Client(direct, mode="legacy") as c:
        r = await c.call_tool(DENIED_TOOL, {})
        direct_text, direct_error = r.data, r.is_error

    through = StdioTransport(
        command=NIM,
        args=["serve", "--connector", "rig", "--client", "test-rig", "--", sys.executable, SERVER],
        env=env,
    )
    denial_text = None
    raised = False
    async with Client(through, mode="legacy") as c:
        try:
            await c.call_tool(DENIED_TOOL, {})
        except Exception as exc:
            raised = True
            denial_text = str(exc)

    problems = []
    if direct_error or direct_text != "the server ran it":
        problems.append(f"direct call to {DENIED_TOOL} did not succeed as it should have with no rule: "
                         f"is_error={direct_error} data={direct_text!r}")
    if not raised:
        problems.append(f"through nim, {DENIED_TOOL} was not refused -- the rule did not bite")
    else:
        if "Nim" not in denial_text:
            problems.append(f"the refusal does not name Nim: {denial_text!r}")
        if "retry" not in denial_text.lower():
            problems.append(f"the refusal does not say not to retry: {denial_text!r}")

    if problems:
        return False, "; ".join(problems)
    return True, f"denied as expected: {denial_text!r}"


def check_duplicate_method_is_refused(env) -> tuple[bool, str]:
    """MUST DIFFER: a frame whose object names `method` twice.

    Go's strict, exact-key decoder (internal/mcp.Classify) refuses this whole
    -- not a JSON-RPC error, not a tool result, nothing at all on that id,
    because which of the two methods to answer under is exactly the question
    it cannot answer safely. Direct, the downstream server has no such
    scruples: Python's own json module keeps the last "method" it sees, same
    as JavaScript would, and answers it as an ordinary tools/call.
    """
    dup_frame = (
        '{"jsonrpc":"2.0","id":42,"method":"ping",'
        '"method":"tools/call","params":{"name":"echo","arguments":{"text":"dup-key-attack"}}}'
    )

    direct = RawSession(sys.executable, [SERVER], env)
    try:
        direct.handshake()
        direct.send(dup_frame)
        direct_line = direct.read_line(5)
    finally:
        direct.close()

    through = RawSession(
        NIM,
        ["serve", "--connector", "rig", "--client", "test-rig", "--", sys.executable, SERVER],
        env,
    )
    try:
        through.handshake()
        through.send(dup_frame)
        nim_line = through.read_line(5)
        # The relay must still be alive and serving ordinary frames -- a
        # refusal that is really a dead relay would look the same otherwise.
        through.send('{"jsonrpc":"2.0","id":43,"method":"tools/call","params":'
                     '{"name":"echo","arguments":{"text":"still-alive"}}}')
        alive_line = through.read_line(5)
    finally:
        through.close()

    problems = []
    if not direct_line or '"dup-key-attack"' not in direct_line:
        problems.append(f"direct did not answer the duplicate-key frame as an ordinary call: {direct_line!r}")
    if nim_line is not None:
        problems.append(f"through nim, the duplicate-key frame got an answer instead of silence: {nim_line!r}")
    if not alive_line or '"still-alive"' not in alive_line:
        problems.append(f"the relay did not survive to answer the next frame: {alive_line!r}")

    if problems:
        return False, "; ".join(problems)
    return True, "direct answered it; through nim, silence -- the frame was refused, not merely unlucky"


async def main() -> int:
    home = RIG / "nim-home"
    if home.exists():
        shutil.rmtree(home)
    env = dict(os.environ)
    env["NIM_HOME"] = str(home)

    print(f"NIM_BINARY: {NIM}")
    print(f"NIM_HOME:   {home}")
    print()

    daemon = start_daemon(env)
    try:
        ok, out = run_nim("policy", "deny", DENIED_TOOL, env=env)
        if not ok or "recorded in the journal" not in out:
            print(f"FATAL: could not set up the deny rule for {DENIED_TOOL}:\n{out}")
            return 2

        direct = StdioTransport(command=sys.executable, args=[SERVER], env=env)
        through = StdioTransport(
            command=NIM,
            args=["serve", "--connector", "rig", "--client", "test-rig", "--", sys.executable, SERVER],
            env=env,
        )

        a = await drive(direct, "direct")
        b = await drive(through, "through nim")

        print(f"{'check':<12} {'direct':<28} {'through nim':<28} verdict")
        print("-" * 84)
        match_failures = 0
        for key in a:
            same = a[key] == b[key]
            if not same:
                match_failures += 1
            va, vb = str(a[key]), str(b[key])
            print(f"{key:<12} {va[:26]:<28} {vb[:26]:<28} {'MATCH' if same else 'DIFFERS (unexpectedly)'}")

        print()
        print("tools identical:  ", a["tools"] == b["tools"], a["tools"])
        print("schemas identical:", a["schemas"] == b["schemas"])

        print()
        print(f"{'must-differ case':<40} verdict")
        print("-" * 84)
        differ_failures = 0
        for name, ok, detail in (
            ("policy-denied tool call", *await check_policy_denial(env)),
            ("repeated `method` key", *check_duplicate_method_is_refused(env)),
        ):
            if not ok:
                differ_failures += 1
            print(f"{name:<40} {'DIFFERS as required' if ok else 'WRONG'}")
            print(f"    {detail}")

        total_failures = match_failures + differ_failures
        print()
        print("RESULT:", "IDENTICAL" if total_failures == 0 else f"{total_failures} DIFFERENCES")
        return 1 if total_failures else 0
    finally:
        stop_daemon(daemon)


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
