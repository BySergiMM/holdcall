"""Stage the four moments of docs/images/console.gif, one trigger file at a
time, so a screenshot can be taken between them:

    python3 tools/console-demo/gif.py . "$d" &     # prints STAGE1: a finished session
    touch "$d/trig/go-hold"                        # prints HELD: a call is waiting
    holdcall reject <id> ...                       # from the CLI, over HOLDCALL_HOME="$d/holdcall-home"
    touch "$d/trig/go-end"                         # the client gets its refusal; everything stops

Frames are captured from `holdcall console` between the steps and assembled
with Pillow (see README.md here). Temp home only; nothing real is touched."""
import json, os, re, select, subprocess, sys, time
from pathlib import Path
ROOT = Path(sys.argv[1]); DEMO = Path(sys.argv[2]); TRIG = DEMO / "trig"
NIM = str(ROOT / "engine/bin/holdcall"); SERVER = str(ROOT / "tools/relay-rig/server.py"); PY = str(ROOT / "tools/relay-rig/.venv/bin/python")
HOME = DEMO / "holdcall-home"; FAKEHOME = DEMO / "home"
for d in (HOME, FAKEHOME, TRIG): d.mkdir(parents=True, exist_ok=True)
(HOME / "config.toml").write_text('[daemon]\napproval_timeout = "45m"\n')
env = dict(os.environ, HOLDCALL_HOME=str(HOME), HOME=str(FAKEHOME))
def nim(*a): return subprocess.run([NIM, *a], env=env, capture_output=True, text=True).stdout
class Session:
    def __init__(self, connector, client):
        self.p = subprocess.Popen([NIM, "serve", "--connector", connector, "--client", client, "--", PY, SERVER], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, bufsize=1, env=env)
        self.n = 0
        self.send({"jsonrpc": "2.0", "id": self.next(), "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": client, "version": "1.4.2"}}}); self.read(15)
        self.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
    def next(self): self.n += 1; return self.n
    def send(self, o): self.p.stdin.write(json.dumps(o) + "\n"); self.p.stdin.flush()
    def read(self, t):
        r, _, _ = select.select([self.p.stdout], [], [], t); return self.p.stdout.readline() if r else None
    def call(self, tool, args, wait=10): self.call_nowait(tool, args); return self.read(wait)
    def call_nowait(self, tool, args): self.send({"jsonrpc": "2.0", "id": self.next(), "method": "tools/call", "params": {"name": tool, "arguments": args}})
    def close(self):
        try: self.p.stdin.close()
        except Exception: pass
        self.p.terminate()
        try: self.p.wait(5)
        except subprocess.TimeoutExpired: self.p.kill()
def held(): return re.findall(r"^id\s+(\S+)$", nim("approve"), re.M)
def wait_for(name):
    while not (TRIG / name).exists(): time.sleep(0.3)
daemon = subprocess.Popen([NIM, "daemon"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
for _ in range(50):
    if "daemon   running" in nim("status"): break
    time.sleep(0.2)
me = subprocess.check_output(["ps", "-o", "comm=", "-p", str(os.getpid())], text=True).strip()
nim("agent", "add", "claude-code", me)
nim("policy", "deny", "delete_repository"); nim("policy", "ask", "force_push"); nim("policy", "ask", "dangerous_tool")
nim("policy", "deny", "explode"); nim("policy", "budget", "20", "--tool", "add"); nim("policy", "allow", "echo", "--connector", "github")
s1 = Session("github", "claude-code")
s1.call("add", {"a": 2, "b": 3}); s1.call("echo", {"text": "git status"}); s1.call("explode", {}); s1.call("add", {"a": 10, "b": 20}); s1.call("echo", {"text": "git log --oneline -5"})
s1.call_nowait("dangerous_tool", {"branch": "feature/console", "force": False}); time.sleep(1.5)
for i in held(): nim("approve", i)
s1.read(10); s1.close()
s2 = Session("filesystem", "cursor"); s2.call("echo", {"text": "README.md"}); s2.call("add", {"a": 40, "b": 2}); s2.close()
print("STAGE1", flush=True)
wait_for("go-hold")
s3 = Session("github", "claude-code"); s3.call("echo", {"text": "git status"})
s3.call_nowait("dangerous_tool", {"repository": "BySergiMM/holdcall", "branch": "main", "force": True, "message": "chore: rewrite history ‮force‬"})
time.sleep(1.5); print("HELD", held(), flush=True)
wait_for("go-end")
print("CLIENT", (s3.read(20) or "").strip()[:120], flush=True)
s3.close(); daemon.terminate()
try: daemon.wait(5)
except subprocess.TimeoutExpired: daemon.kill()
print("DONE", flush=True)
