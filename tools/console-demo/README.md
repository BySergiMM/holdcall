# Console demo

`stage.py` fills a throwaway home with what the console is for: rules and
budgets, two finished sessions (allows, a deny, a budget deny, one rejected
and one approved hold) and a third session that keeps a call held for a
human until the script is stopped. The screenshots in `docs/images/` come
from it. Nothing it does touches the real install: `NIM_HOME` and `HOME` both
point into the directory you give it.

```bash
cd engine && go build -o bin/nim ./cmd/nim && cd ..
python3 -m venv tools/relay-rig/.venv && tools/relay-rig/.venv/bin/pip install fastmcp==4.0.3
d="$(mktemp -d)"
python3 tools/console-demo/stage.py "$d" &            # prints READY when the call is held
NIM_HOME="$d/nim-home" HOME="$d/home" engine/bin/nim console --addr 127.0.0.1:7719
```

Then open http://127.0.0.1:7719/#held. The console and the daemon must be the
same build: rebuild `nim`, and the running daemon will be refused as an
older build until the script is restarted (that refusal is F-001 working).
`?theme=dark` or `?theme=light` forces a theme for a screenshot.
