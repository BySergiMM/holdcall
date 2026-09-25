"""Stage a demo journal for the console: a daemon in a throwaway home, a few
rules and budgets, two finished sessions with allows, denies, one rejected
and one approved hold, and a third session that keeps a call held until
this process ends. For screenshots, demos and trying the console by hand;
nothing here touches the real install.

    cd engine && go build -o bin/holdcall ./cmd/holdcall && cd ..
    python3 tools/console-demo/stage.py "$(mktemp -d)"      # prints READY, then waits
    HOLDCALL_HOME=<that dir>/holdcall-home HOME=<that dir>/home engine/bin/holdcall console

Needs the relay rig's venv (tools/relay-rig/README.md), whose server is the
downstream MCP server the sessions talk to. HOLDCALL_BINARY overrides the binary.
Stop it with Ctrl-C or SIGTERM; it ends the sessions and the daemon.
"""
import json, os, re, select, signal, subprocess, sys, time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HOLDCALL = os.environ.get("HOLDCALL_BINARY") or str(ROOT / "engine/bin/holdcall")
SERVER = str(ROOT / "tools/relay-rig/server.py")
PY = str(ROOT / "tools/relay-rig/.venv/bin/python")
if len(sys.argv) != 2:
    sys.exit("usage: stage.py <empty directory for HOLDCALL_HOME and HOME>")
DEMO = Path(sys.argv[1]).resolve()
HOME = DEMO / "holdcall-home"; FAKEHOME = DEMO / "home"
HOME.mkdir(parents=True, exist_ok=True); FAKEHOME.mkdir(parents=True, exist_ok=True)
(HOME / "config.toml").write_text('[daemon]\napproval_timeout = "45m"\n')
env = dict(os.environ, HOLDCALL_HOME=str(HOME), HOME=str(FAKEHOME))


def holdcall(*args):
    p = subprocess.run([HOLDCALL, *args], env=env, capture_output=True, text=True)
    print("$ holdcall", " ".join(args), "->", (p.stdout + p.stderr).strip()[:160].replace("\n", " | "))
    return p.stdout


class Session:
    """A raw JSON-RPC client over stdio, so the sessions need no MCP client
    library and the arguments are exactly what is written here."""

    def __init__(self, connector, client):
        self.p = subprocess.Popen(
            [HOLDCALL, "serve", "--connector", connector, "--client", client, "--", PY, SERVER],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            text=True, bufsize=1, env=env)
        self.n = 0
        self.send({"jsonrpc": "2.0", "id": self.next(), "method": "initialize",
                   "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                              "clientInfo": {"name": client, "version": "1.4.2"}}})
        self.read(15)
        self.send({"jsonrpc": "2.0", "method": "notifications/initialized"})

    def next(self):
        self.n += 1
        return self.n

    def send(self, obj):
        self.p.stdin.write(json.dumps(obj) + "\n"); self.p.stdin.flush()

    def read(self, timeout):
        r, _, _ = select.select([self.p.stdout], [], [], timeout)
        return self.p.stdout.readline() if r else None

    def call(self, tool, args, wait=10):
        self.call_nowait(tool, args)
        line = self.read(wait)
        print("  call", tool, json.dumps(args)[:60], "->", (line or "(no answer yet)").strip()[:100])
        return line

    def call_nowait(self, tool, args):
        self.send({"jsonrpc": "2.0", "id": self.next(), "method": "tools/call",
                   "params": {"name": tool, "arguments": args}})

    def close(self):
        try:
            self.p.stdin.close()
        except Exception:
            pass
        self.p.terminate()
        try:
            self.p.wait(5)
        except subprocess.TimeoutExpired:
            self.p.kill()


def held_ids():
    return re.findall(r"^id\s+(\S+)$", holdcall("approve"), re.M)


daemon = subprocess.Popen([HOLDCALL, "daemon"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
for _ in range(50):
    if "daemon   running" in holdcall("status"):
        break
    time.sleep(0.2)

# The agent is whoever spawns the relay: this interpreter. Enrol the file the
# kernel will report, which may not be sys.executable (a launcher stub).
me = subprocess.check_output(["ps", "-o", "comm=", "-p", str(os.getpid())], text=True).strip()
holdcall("agent", "add", "claude-code", me)
holdcall("policy", "deny", "explode")
holdcall("policy", "deny", "delete_repository", "--connector", "github")
holdcall("policy", "ask", "dangerous_tool")
holdcall("policy", "ask", "big", "--agent", "claude-code")
holdcall("policy", "allow", "echo", "--connector", "github")
holdcall("policy", "budget", "4", "--tool", "add")
holdcall("policy", "budget", "50", "--all-tools", "--connector", "github")

# Session 1: allows, a deny, a budget deny, one rejected and one approved hold.
s1 = Session("github", "claude-code")
s1.call("add", {"a": 2, "b": 3})
s1.call("echo", {"text": "hello from the demo, with ünïcödé"})
s1.call("explode", {})
for a, b in ((10, 20), (1, 1), (7, 8), (5, 5)):  # the fourth add is over the budget
    s1.call("add", {"a": a, "b": b})
s1.call_nowait("dangerous_tool", {"branch": "release/2026-09", "force": True}); time.sleep(1.5)
for i in held_ids():
    holdcall("reject", i, "--reason", "not that branch")
print("  ->", (s1.read(10) or "").strip()[:100])
s1.call_nowait("dangerous_tool", {"branch": "feature/console", "force": False}); time.sleep(1.5)
for i in held_ids():
    holdcall("approve", i)
print("  ->", (s1.read(10) or "").strip()[:100])
s1.close()

# Session 2: another client on another connector, short and finished.
s2 = Session("filesystem", "cursor")
s2.call("echo", {"text": "README.md"}); s2.call("add", {"a": 40, "b": 2}); s2.call("explode", {})
s2.close()

# Session 3: stays open, holding a call with a bidirectional override in it.
s3 = Session("github", "claude-code")
s3.call("echo", {"text": "git status"}); s3.call("add", {"a": 3, "b": 4})
s3.call_nowait("dangerous_tool", {"repository": "BySergiMM/holdcall", "branch": "main", "force": True,
                                  "message": "chore: rewrite history ‮force‬"})
time.sleep(1.5); print("held now:", held_ids())
print(f"READY  HOLDCALL_HOME={HOME} HOME={FAKEHOME}"); sys.stdout.flush()


def bye(*_):
    s3.close()
    daemon.terminate()
    try:
        daemon.wait(5)
    except subprocess.TimeoutExpired:
        daemon.kill()
    sys.exit(0)


signal.signal(signal.SIGTERM, bye); signal.signal(signal.SIGINT, bye)
while True:
    time.sleep(1)
