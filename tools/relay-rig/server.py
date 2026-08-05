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


if __name__ == "__main__":
    mcp.run()
