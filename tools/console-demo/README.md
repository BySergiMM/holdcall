# Console demo

`stage.py` fills a throwaway home with what the console is for: rules and
budgets, two finished sessions (allows, a deny, a budget deny, one rejected
and one approved hold) and a third session that keeps a call held for a
human until the script is stopped. The screenshots in `docs/images/` come
from it. Nothing it does touches the real install: `HOLDCALL_HOME` and `HOME` both
point into the directory you give it.

```bash
cd engine && go build -o bin/holdcall ./cmd/holdcall && cd ..
python3 -m venv tools/relay-rig/.venv && tools/relay-rig/.venv/bin/pip install fastmcp==4.0.3
d="$(mktemp -d)"
python3 tools/console-demo/stage.py "$d" &            # prints READY when the call is held
HOLDCALL_HOME="$d/holdcall-home" HOME="$d/home" engine/bin/holdcall console --addr 127.0.0.1:7719
```

Then open http://127.0.0.1:7719/#held. The console and the daemon must be the
same build: rebuild `holdcall`, and the running daemon will be refused as an
older build until the script is restarted (that refusal is F-001 working).
`?theme=dark` or `?theme=light` forces a theme for a screenshot.

## The GIF

`gif.py` stages the same story in four stops, each released by a trigger
file, so a frame can be captured between them: the overview, the strip that
says a call is held, the held call with its arguments, and the journal after
the rejection. Capture each stop with headless Chrome at 1200x760
(`--force-device-scale-factor=1`), then assemble with Pillow: resize to 960
wide, quantize to about 160 colours, durations around 2.6, 2.2, 4.2 and 3.8
seconds, loop forever. The result is `docs/images/console.gif`, about 260 KB.
