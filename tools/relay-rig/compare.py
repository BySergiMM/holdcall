"""Drive a real MCP client twice: straight to the server, then through Nim.

The relay passes only if both runs are indistinguishable.
"""

import asyncio
import json
import os
import sys
from pathlib import Path

from fastmcp import Client
from fastmcp.client.transports import StdioTransport

RIG = Path(__file__).parent
SERVER = str(RIG / "server.py")
NIM = os.environ.get(
    "NIM_BINARY",
    str(RIG.parent.parent / "engine" / "bin" / ("nim.exe" if os.name == "nt" else "nim")),
)


async def drive(transport, label):
    """Exercise the server and return a comparable snapshot of everything seen."""
    out = {}
    async with Client(transport) as c:
        tools = await c.list_tools()
        out["tools"] = sorted(
            {"name": t.name, "description": (t.description or "").strip()} for t in tools
        ) if False else sorted([t.name for t in tools])
        out["schemas"] = {t.name: json.dumps(t.inputSchema, sort_keys=True) for t in tools}

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


async def main() -> int:
    home = RIG / "nim-home"
    env = dict(os.environ)
    env["NIM_HOME"] = str(home)

    direct = StdioTransport(command=sys.executable, args=[SERVER], env=env)
    through = StdioTransport(
        command=NIM,
        args=["serve", "--target", "rig", "--client", "test-rig", "--", sys.executable, SERVER],
        env=env,
    )

    a = await drive(direct, "direct")
    b = await drive(through, "through nim")

    print(f"{'check':<12} {'direct':<28} {'through nim':<28} verdict")
    print("-" * 84)
    failures = 0
    for key in a:
        same = a[key] == b[key]
        if not same:
            failures += 1
        va, vb = str(a[key]), str(b[key])
        print(f"{key:<12} {va[:26]:<28} {vb[:26]:<28} {'MATCH' if same else 'DIFFERS'}")

    print()
    print("tools identical:  ", a["tools"] == b["tools"], a["tools"])
    print("schemas identical:", a["schemas"] == b["schemas"])
    print()
    print("RESULT:", "IDENTICAL" if failures == 0 else f"{failures} DIFFERENCES")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
