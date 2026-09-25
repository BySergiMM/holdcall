"""A downstream MCP server for the relay test. Deliberately awkward."""

from fastmcp import FastMCP

mcp = FastMCP("rig-server")


@mcp.tool
def echo(text: str) -> str:
    """Return the text unchanged, including any unicode."""
    return text


@mcp.tool
def add(a: int, b: int) -> int:
    """Add two numbers."""
    return a + b


@mcp.tool
def big(kilobytes: int = 512) -> str:
    """Return a large payload, to prove the framing has no size limit."""
    return "x" * (kilobytes * 1024)


@mcp.tool
def explode() -> str:
    """Always fail, to prove errors reach the client untouched."""
    raise ValueError("deliberate failure")


@mcp.tool
def dangerous_tool() -> str:
    """Succeed when called directly, so the rig can prove Holdcall -- not the
    server -- is what refuses it once a policy names it. The rig denies this
    tool by name before comparing, over its own HOLDCALL_HOME; it names nothing a
    real connector would recognise."""
    return "the server ran it"


if __name__ == "__main__":
    mcp.run()
