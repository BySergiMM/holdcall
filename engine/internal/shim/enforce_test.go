package shim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/daemon"
	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/mcp"
)

// The tests below drive the relay's two pumps directly rather than through Run,
// so that what the connector receives is a buffer this file owns. Byte-identical
// is the assertion throughout: comparing parsed JSON would pass on a relay that
// re-encoded every message, which is the failure these are for.

// ---------- a daemon that answers however a test needs ----------

type fakeDaemon struct {
	ln   net.Listener
	path string

	mu     sync.Mutex
	events []daemon.Event

	// answer decides the reply to a call.request. A nil answer allows.
	answer func(ev daemon.Event) any
	// silent accepts the question and never replies.
	silent bool
	// hangUp closes the connection instead of replying.
	hangUp bool
}

func newDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	path := filepath.Join(os.TempDir(),
		fmt.Sprintf("nim-enforce-%d.sock", time.Now().UnixNano()%1e9))
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	d := &fakeDaemon{ln: ln, path: path}
	t.Cleanup(func() { ln.Close(); os.Remove(path) })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serve(conn)
		}
	}()
	return d
}

func (d *fakeDaemon) serve(conn net.Conn) {
	defer conn.Close()
	r := mcp.NewReader(conn)
	enc := json.NewEncoder(conn)
	for {
		raw, err := r.ReadRaw()
		if err != nil {
			return
		}
		var ev daemon.Event
		if json.Unmarshal(raw, &ev) != nil {
			continue
		}

		d.mu.Lock()
		d.events = append(d.events, ev)
		answer, silent, hangUp := d.answer, d.silent, d.hangUp
		d.mu.Unlock()

		if ev.Kind != daemon.KindCallRequest {
			continue
		}
		if hangUp {
			return
		}
		if silent {
			continue // the shim waits, and the clock runs
		}
		var reply any = daemon.Decision{
			Kind:      daemon.KindDecision,
			SessionID: ev.SessionID,
			Seq:       ev.Seq,
			Decision:  journal.DecisionAllow,
		}
		if answer != nil {
			reply = answer(ev)
		}
		if reply == nil {
			continue
		}
		if raw, ok := reply.(json.RawMessage); ok {
			conn.Write(append([]byte(raw), '\n'))
			continue
		}
		enc.Encode(reply)
	}
}

func (d *fakeDaemon) seen() []daemon.Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]daemon.Event(nil), d.events...)
}

func (d *fakeDaemon) calls() []daemon.Event {
	var out []daemon.Event
	for _, ev := range d.seen() {
		if ev.Kind == daemon.KindCallRequest {
			out = append(out, ev)
		}
	}
	return out
}

// await collects the events matching want, waiting for as many as n to turn up.
//
// Only a call.request is synchronous. Everything else is still fire and forget,
// so a test that counts anomalies the instant the relay returns is measuring its
// own timing rather than the relay's behaviour. It returns whatever it has when
// the deadline passes, so a test expecting none does not sit here waiting.
func (d *fakeDaemon) await(t *testing.T, n int, want func(daemon.Event) bool) []daemon.Event {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var got []daemon.Event
		for _, ev := range d.seen() {
			if want(ev) {
				got = append(got, ev)
			}
		}
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func anomalyOf(name mcp.Anomaly) func(daemon.Event) bool {
	return func(ev daemon.Event) bool {
		return ev.Kind == daemon.KindAnomaly && ev.Anomaly == string(name)
	}
}

// ---------- the relay under test ----------

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) Close() error { return nil }
func (s *syncBuf) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.b.Bytes()...)
}
func (s *syncBuf) String() string { return string(s.Bytes()) }

type rig struct {
	shim      *Shim
	daemon    *fakeDaemon
	connector *syncBuf // what the downstream server received
	client    *syncBuf // what the client received from Nim itself
}

// newRig wires a relay to a fake daemon. Pass nil to leave it with no daemon at
// all, which is the fail-closed case.
func newRig(t *testing.T, d *fakeDaemon) *rig {
	t.Helper()

	r := &reporter{ch: make(chan item, 256), done: make(chan struct{})}
	if d != nil {
		conn, err := net.Dial("unix", d.path)
		if err != nil {
			t.Fatalf("dialling the fake daemon: %v", err)
		}
		r.attach(conn)
	}
	go r.loop()

	client := &syncBuf{}
	s := &Shim{
		sessionID: "s1",
		reporter:  r,
		out:       &clientOut{w: client},
		inFlight:  make(map[string]pending),
	}
	t.Cleanup(func() { r.close() })
	return &rig{shim: s, daemon: d, connector: &syncBuf{}, client: client}
}

// relay pushes frames from the client and returns once they have all been
// handled. The frames are written exactly as given, newline included.
func (g *rig) relay(frames ...string) {
	g.shim.pumpRequests(strings.NewReader(strings.Join(frames, "")), g.connector)
}

// respond pushes frames from the connector back towards the client.
func (g *rig) respond(frames ...string) {
	g.shim.pumpResponses(strings.NewReader(strings.Join(frames, "")), g.client)
}

func deny(reason string) func(daemon.Event) any {
	return func(ev daemon.Event) any {
		return daemon.Decision{
			Kind: daemon.KindDecision, SessionID: ev.SessionID, Seq: ev.Seq,
			Decision: journal.DecisionDeny, Reason: reason,
		}
	}
}

func decodeResponse(t *testing.T, line []byte) struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
} {
	t.Helper()
	var got struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("what Nim sent the client is not valid JSON: %v\n%s", err, line)
	}
	return got
}

// ---------- 1. an allowed call ----------

// Byte-identical, not equivalent. The frame below has its keys in an order no
// encoder would choose, two spaces inside it, unicode in three normalisations
// and a 512 KiB argument -- all of which survive only if nothing re-serialises
// it on the way through.
func TestAnAllowedCallReachesTheConnectorByteForByte(t *testing.T) {
	big := strings.Repeat("payload ", 64*1024) // 512 KiB
	frame := `{"params":{"arguments":{"z":1,"a":"café café 日本語 ` +
		`🚀 \" \\ {} []","big":"` + big + `"},"name":"echo"},` +
		`"method":"tools/call","id":9007199254740993,  "jsonrpc":"2.0"}` + "\n"

	g := newRig(t, newDaemon(t))
	g.relay(frame)

	if got := g.connector.String(); got != frame {
		t.Errorf("the connector did not receive the original bytes\nlen got=%d want=%d",
			len(got), len(frame))
		if len(got) < 400 && len(frame) < 400 {
			t.Errorf("got  %q\nwant %q", got, frame)
		}
	}
	if g.client.String() != "" {
		t.Errorf("Nim answered a call it allowed: %q", g.client.String())
	}

	calls := g.daemon.calls()
	if len(calls) != 1 {
		t.Fatalf("the daemon was asked %d times, want once", len(calls))
	}
	// The relay asks; it does not tell. A decision on the way out would be the
	// shim recording its own verdict.
	if calls[0].Decision != "" {
		t.Errorf("the relay sent a decision of its own: %q", calls[0].Decision)
	}
	if calls[0].Tool != "echo" || calls[0].Seq != 1 {
		t.Errorf("call reported as %+v", calls[0])
	}
}

// ---------- 2. a refused call ----------

func TestARefusedCallNeverReachesTheConnector(t *testing.T) {
	d := newDaemon(t)
	d.answer = deny("the tool is on the deny list in config.toml")

	g := newRig(t, d)
	g.relay(`{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call",` +
		`"params":{"name":"dangerous_tool","arguments":{}}}` + "\n")

	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatalf("the connector received %d bytes of a call that was refused: %q",
			n, g.connector.String())
	}

	got := decodeResponse(t, []byte(g.client.String()))
	if got.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q", got.JSONRPC)
	}
	if !got.Result.IsError {
		t.Error("the client was not told the call failed")
	}
	if string(got.ID) != "9007199254740993" {
		t.Errorf("id = %s: a client cannot match a response it cannot recognise", got.ID)
	}
	if len(got.Result.Content) != 1 || got.Result.Content[0].Text != mcp.DeniedByPolicy {
		t.Errorf("the client was told %+v, want the policy refusal", got.Result.Content)
	}

	// Refused once means asked once, and no outcome will ever follow.
	if n := len(g.daemon.calls()); n != 1 {
		t.Errorf("the daemon was asked %d times", n)
	}
	if len(g.shim.inFlight) != 0 {
		t.Error("a refused call is waiting for a response that will never come")
	}
}

// A journal that could not be written is not a policy verdict: the daemon
// never reached a decision, so the client must be told the retryable
// sentence, not the permanent one.
func TestAnUnrecordedCallIsNotReadAsPolicy(t *testing.T) {
	d := newDaemon(t)
	d.answer = func(ev daemon.Event) any {
		return daemon.Decision{
			Kind: daemon.KindDecision, SessionID: ev.SessionID, Seq: ev.Seq,
			Decision: daemon.DecisionUndecided, Reason: "the call could not be recorded",
		}
	}

	g := newRig(t, d)
	g.relay(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}` + "\n")

	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatalf("a call reached the connector after the daemon could not record it: %q",
			g.connector.String())
	}
	got := decodeResponse(t, []byte(g.client.String()))
	if got.Result.Content[0].Text != mcp.DeniedNoDecision {
		t.Errorf("client told %q, want the could-not-decide refusal", got.Result.Content[0].Text)
	}
}

// ---------- 3. no daemon ----------

func TestWithNoDaemonEverythingIsRefused(t *testing.T) {
	g := newRig(t, nil)
	g.relay(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}` + "\n")

	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatalf("a call reached the connector with nothing to record it: %q", g.connector.String())
	}
	got := decodeResponse(t, []byte(g.client.String()))
	if !got.Result.IsError {
		t.Error("the client was not told the call failed")
	}
	// Not the policy sentence: no policy was consulted, and saying otherwise
	// would tell an agent a transient failure is permanent.
	if got.Result.Content[0].Text != mcp.DeniedNoDecision {
		t.Errorf("client told %q, want the could-not-decide refusal", got.Result.Content[0].Text)
	}
	if g.shim.reporter.lost == 0 {
		t.Error("a call that could not be recorded was not counted as a loss")
	}
}

// ---------- 4. timeout ----------

// The first call pays the timeout. The second must not: a dead connection is
// terminal, so everything after it is refused at once. Without that, a wedged
// daemon costs two seconds per call for the life of the session.
func TestATimeoutRefusesAndTheNextCallDoesNotWait(t *testing.T) {
	d := newDaemon(t)
	d.silent = true

	g := newRig(t, d)
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}` + "\n"

	start := time.Now()
	g.relay(call)
	first := time.Since(start)

	if first < decisionTimeout {
		t.Errorf("gave up after %v, before the timeout: %v", first, decisionTimeout)
	}
	if first > decisionTimeout+time.Second {
		t.Errorf("took %v to give up on a %v timeout", first, decisionTimeout)
	}
	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatal("a call reached the connector after the decision timed out")
	}

	start = time.Now()
	g.relay(call)
	if second := time.Since(start); second > 200*time.Millisecond {
		t.Errorf("the second call waited %v: the connection was not dropped", second)
	}
	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatal("a call reached the connector after the connection was dropped")
	}
}

// ---------- 5. answers that are not decisions ----------

func TestAnythingButAValidDecisionIsARefusal(t *testing.T) {
	cases := []struct {
		name  string
		reply func(daemon.Event) any
	}{
		{"not JSON", func(daemon.Event) any { return json.RawMessage(`{"kind":`) }},
		{"the wrong kind", func(ev daemon.Event) any {
			return daemon.Decision{Kind: "verdict", SessionID: ev.SessionID, Seq: ev.Seq,
				Decision: journal.DecisionAllow}
		}},
		{"another session", func(ev daemon.Event) any {
			return daemon.Decision{Kind: daemon.KindDecision, SessionID: "somebody-else",
				Seq: ev.Seq, Decision: journal.DecisionAllow}
		}},
		{"another sequence", func(ev daemon.Event) any {
			return daemon.Decision{Kind: daemon.KindDecision, SessionID: ev.SessionID,
				Seq: ev.Seq + 1, Decision: journal.DecisionAllow}
		}},
		{"a decision nobody defined", func(ev daemon.Event) any {
			return daemon.Decision{Kind: daemon.KindDecision, SessionID: ev.SessionID,
				Seq: ev.Seq, Decision: "maybe"}
		}},
		{"an empty decision", func(ev daemon.Event) any {
			return daemon.Decision{Kind: daemon.KindDecision, SessionID: ev.SessionID, Seq: ev.Seq}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newDaemon(t)
			d.answer = c.reply

			g := newRig(t, d)
			g.relay(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}` + "\n")

			if n := len(g.connector.Bytes()); n != 0 {
				t.Fatalf("the connector was reached on a %s: %q", c.name, g.connector.String())
			}
			got := decodeResponse(t, []byte(g.client.String()))
			if !got.Result.IsError {
				t.Error("the client was not told the call failed")
			}
			if got.Result.Content[0].Text != mcp.DeniedNoDecision {
				t.Errorf("client told %q, want the could-not-decide refusal",
					got.Result.Content[0].Text)
			}
		})
	}
}

// ---------- 6. the socket closing mid-question ----------

func TestAClosedSocketDuringADecisionIsARefusal(t *testing.T) {
	d := newDaemon(t)
	d.hangUp = true

	g := newRig(t, d)
	g.relay(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}` + "\n")

	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatal("a call reached the connector after the daemon hung up")
	}
	if !strings.Contains(g.client.String(), "isError") {
		t.Errorf("the client was not answered: %q", g.client.String())
	}
}

// ---------- 8. two writers, one client ----------

// splittingWriter models what a pipe does to a write bigger than the kernel's
// atomic size: it splits it, and anything writing concurrently lands in the gap.
//
// A real os.Pipe would not fail this test even without clientOut's mutex,
// because Go's *os.File takes a lock of its own -- which is exactly why the
// relay must not rely on it. This writer asks the question directly.
type splittingWriter struct {
	mu     sync.Mutex
	inside int
	seen   [][]byte
	frag   []byte
}

func (w *splittingWriter) Write(p []byte) (int, error) {
	w.enter()
	defer w.leave()
	half := len(p) / 2
	w.part(p[:half])
	time.Sleep(time.Millisecond) // room for an unserialised writer to cut in
	w.part(p[half:])
	return len(p), nil
}

func (w *splittingWriter) enter() { w.mu.Lock(); w.inside++; w.mu.Unlock() }
func (w *splittingWriter) leave() { w.mu.Lock(); w.inside--; w.mu.Unlock() }

// part appends one fragment, splitting the stream into lines as it goes.
func (w *splittingWriter) part(b []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frag = append(w.frag, b...)
	for {
		i := bytes.IndexByte(w.frag, '\n')
		if i < 0 {
			return
		}
		w.seen = append(w.seen, append([]byte(nil), w.frag[:i]...))
		w.frag = w.frag[i+1:]
	}
}

func (w *splittingWriter) lines() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]byte(nil), w.seen...)
}

func TestTwoWritersNeverSpliceAMessage(t *testing.T) {
	d := newDaemon(t)
	d.answer = deny("no")

	g := newRig(t, d)
	w := &splittingWriter{}
	g.shim.out = &clientOut{w: w}

	// One large relayed response and one refusal, over and over, from the two
	// goroutines that write to a client in production.
	big := `{"jsonrpc":"2.0","id":42,"result":{"content":[{"type":"text","text":"` +
		strings.Repeat("A", 512*1024) + `"}]}}` + "\n"
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}` + "\n"

	const rounds = 12
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		g.shim.pumpResponses(strings.NewReader(strings.Repeat(big, rounds)), g.shim.out)
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			g.shim.pumpRequests(strings.NewReader(call), &syncBuf{})
		}
	}()
	wg.Wait()

	lines := w.lines()
	if len(lines) != 2*rounds {
		t.Fatalf("got %d lines, want %d: messages were merged or lost", len(lines), 2*rounds)
	}
	for i, line := range lines {
		if !json.Valid(line) {
			t.Fatalf("line %d is not a complete message (%d bytes): %.120q…",
				i, len(line), line)
		}
	}
}

// ---------- 10. responses come back untouched ----------

func TestResponsesAreRelayedByteForByte(t *testing.T) {
	frames := []string{
		`{"id":1,"result":{"content":[{"text":"café 🚀","type":"text"}]},  "jsonrpc":"2.0"}` + "\n",
		`{"jsonrpc":"2.0","id":2,"result":{"isError":true,"content":[]}}` + "\n",
		`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"no such method"}}` + "\n",
		`[{"jsonrpc":"2.0","id":4,"result":{}}]` + "\n",
		`not json at all` + "\n",
	}
	want := strings.Join(frames, "")

	g := newRig(t, newDaemon(t))
	g.respond(frames...)

	if got := g.client.String(); got != want {
		t.Errorf("responses were altered\n got %q\nwant %q", got, want)
	}
}

// ---------- 11. frames Nim cannot read ----------

// The reason this rule is categorical. Go rejects NaN and Infinity; Python
// accepts both, so a frame Nim calls malformed is a working tools/call to a
// FastMCP server. The third case defeats any heuristic that looks for the
// literal string "tools/call" in the bytes.
func TestUnreadableFramesAreNotRelayed(t *testing.T) {
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"rm","arguments":{"x":NaN}}}` + "\n",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"rm","arguments":{"x":Infinity}}}` + "\n",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"rm","arguments":{"x":NaN}}}` + "\n",
		`{"jsonrpc":"2.0","id":4,"method":"ping"}}` + "\n",
	}

	g := newRig(t, newDaemon(t))
	g.relay(frames...)

	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatalf("an unreadable frame reached the connector: %q", g.connector.String())
	}
	if s := g.client.String(); s != "" {
		t.Errorf("Nim invented an answer to a frame it could not read: %q", s)
	}
	if n := len(g.daemon.calls()); n != 0 {
		t.Errorf("the daemon was asked about %d frames Nim could not parse", n)
	}

	got := g.daemon.await(t, len(frames), anomalyOf(mcp.AnomalyMalformedJSON))
	if len(got) != len(frames) {
		t.Errorf("recorded %d malformed_json anomalies, want %d", len(got), len(frames))
	}
}

// ---------- 12 & 13. batches ----------

func TestABatchCarryingAToolCallIsRefusedWhole(t *testing.T) {
	frame := `[{"jsonrpc":"2.0","id":1,"method":"ping"},` +
		`{"jsonrpc":"2.0","method":"notifications/progress"},` +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"rm"}}]` + "\n"

	g := newRig(t, newDaemon(t))
	g.relay(frame)

	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatalf("part of a refused batch reached the connector: %q", g.connector.String())
	}
	// No call.request, no seq: the record has no shape for a batch element, and
	// inventing one would put a call in the journal that Nim never decided.
	if n := len(g.daemon.calls()); n != 0 {
		t.Errorf("the daemon was asked about %d batch elements", n)
	}
	if g.shim.seq != 0 {
		t.Errorf("seq advanced to %d for a batch", g.shim.seq)
	}

	if n := len(g.daemon.await(t, 1, anomalyOf(mcp.AnomalyBatch))); n != 1 {
		t.Errorf("recorded %d batch anomalies, want 1", n)
	}

	var got []struct {
		ID    json.RawMessage `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(g.client.String()), &got); err != nil {
		t.Fatalf("the batch refusal is not a valid JSON-RPC batch response: %v\n%s",
			err, g.client.String())
	}
	if len(got) != 2 {
		t.Fatalf("answered %d elements, want the two carrying an id", len(got))
	}
}

func TestABatchWithoutAToolCallIsRelayedByteForByte(t *testing.T) {
	frame := `[{"jsonrpc":"2.0","id":1,"method":"ping"},` +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}]` + "\n"

	g := newRig(t, newDaemon(t))
	g.relay(frame)

	if got := g.connector.String(); got != frame {
		t.Errorf("a harmless batch was altered or dropped\n got %q\nwant %q", got, frame)
	}
	if g.client.String() != "" {
		t.Errorf("Nim answered a batch it relayed: %q", g.client.String())
	}
}

// ---------- 14. two messages in one frame ----------

func TestMoreThanOneMessageInAFrameIsNotRelayed(t *testing.T) {
	frame := `{"jsonrpc":"2.0","id":1,"method":"ping"}` +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"rm"}}` + "\n"

	g := newRig(t, newDaemon(t))
	g.relay(frame)

	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatalf("a frame with two messages in it reached the connector: %q", g.connector.String())
	}
	if n := len(g.daemon.await(t, 1, anomalyOf(mcp.AnomalyFraming))); n != 1 {
		t.Errorf("recorded %d framing anomalies, want 1", n)
	}
}

// ---------- 15. everything else is nobody's business ----------

func TestOnlyToolCallsConsultTheDaemon(t *testing.T) {
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n",
		`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n",
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}` + "\n",
		`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"file:///x"}}` + "\n",
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n",
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2}}` + "\n",
		`{"jsonrpc":"2.0","id":5,"method":"completion/complete"}` + "\n",
	}
	want := strings.Join(frames, "")

	g := newRig(t, newDaemon(t))
	g.relay(frames...)

	if got := g.connector.String(); got != want {
		t.Errorf("a method that is not Nim's business was altered or held up\n got %q\nwant %q",
			got, want)
	}
	if n := len(g.daemon.calls()); n != 0 {
		t.Errorf("the daemon was asked about %d messages that were not tools/call", n)
	}
	if g.client.String() != "" {
		t.Errorf("Nim answered something it should only have relayed: %q", g.client.String())
	}
}

// ---------- 17. a failing tool is not a refusal ----------

// MCP reports a tool that raised as a successful response carrying
// result.isError -- the same shape Nim uses to refuse. The difference is who
// wrote it, and the record has to keep them apart: one is a connector saying the
// work failed, the other is Nim saying the work never happened.
func TestAConnectorErrorIsNotANimRefusal(t *testing.T) {
	g := newRig(t, newDaemon(t))

	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"explode"}}` + "\n"
	g.relay(call)
	if g.connector.String() != call {
		t.Fatal("the call did not reach the connector")
	}

	fail := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"boom"}],"isError":true}}` + "\n"
	g.respond(fail)

	if g.client.String() != fail {
		t.Errorf("the connector's own error was not passed through\n got %q\nwant %q",
			g.client.String(), fail)
	}

	// Recorded as a completed call that failed, not as a refusal.
	outcomes := g.daemon.await(t, 1, func(ev daemon.Event) bool {
		return ev.Kind == daemon.KindCallOutcome
	})
	if len(outcomes) != 1 {
		t.Fatalf("%d outcomes recorded, want 1", len(outcomes))
	}
	if outcomes[0].OK == nil || *outcomes[0].OK {
		t.Error("a tool that raised was recorded as successful")
	}
	if calls := g.daemon.calls(); len(calls) != 1 || calls[0].Decision != "" {
		t.Errorf("the call was reported as %+v", calls)
	}
}

// A present id of null is still a request, not a notification: the client
// sent it expecting a reply with id: null, and a denial must not leave it
// waiting for one that never comes.
func TestADeniedCallWithAnExplicitNullIDIsAnswered(t *testing.T) {
	d := newDaemon(t)
	d.answer = deny("the tool is on the deny list in config.toml")

	g := newRig(t, d)
	g.relay(`{"jsonrpc":"2.0","id":null,"method":"tools/call",` +
		`"params":{"name":"dangerous_tool","arguments":{}}}` + "\n")

	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatalf("the connector received %d bytes of a call that was refused: %q",
			n, g.connector.String())
	}
	got := decodeResponse(t, []byte(g.client.String()))
	if string(got.ID) != "null" {
		t.Errorf("id = %s, want null: a client sending id:null still expects a reply", got.ID)
	}
	if got.Result.Content[0].Text != mcp.DeniedByPolicy {
		t.Errorf("the client was told %+v, want the policy refusal", got.Result.Content)
	}
}

// A queue with no room is not a reason to let a call through.
func TestAFullQueueRefuses(t *testing.T) {
	r := &reporter{ch: make(chan item), done: make(chan struct{})} // no capacity, no reader
	if v := r.ask(daemon.Event{Kind: daemon.KindCallRequest, SessionID: "s1", Seq: 1}); v == verdictAllow {
		t.Fatal("a call was allowed by a reporter that could not even ask")
	}
	if r.lost != 1 {
		t.Errorf("lost = %d, want the unasked call counted", r.lost)
	}
}

var _ io.WriteCloser = (*syncBuf)(nil)

// ---------- 16. objects that do not mean one thing ----------

// answers reads every message Nim itself sent the client, in order.
func answers(t *testing.T, client *syncBuf) []struct {
	ID     json.RawMessage `json:"id"`
	Result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
} {
	t.Helper()
	var out []struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	for _, line := range strings.Split(strings.TrimSpace(client.String()), "\n") {
		if line == "" {
			continue
		}
		var a struct {
			ID     json.RawMessage `json:"id"`
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			t.Fatalf("Nim sent the client something that is not JSON-RPC: %v\n%s", err, line)
		}
		out = append(out, a)
	}
	return out
}

// The bypass, as it was run against this relay before the fix: a repeated
// `method` key made Go's typed decoder return an error, Classify shrugged and
// handed back an empty envelope, and the frame went to the connector with no
// decision, no entry and no anomaly -- where a Python or JavaScript parser read
// the last value, tools/call, and ran the tool.
func TestAToolCallHiddenBehindARepeatedKeyIsRefused(t *testing.T) {
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":5,"method":"tools/call","params":{"name":"rm"}}` + "\n",
		`{"jsonrpc":"2.0","id":2,"method":"ping","method":"tools/call","params":{"name":"rm"}}` + "\n",
		`{"jsonrpc":"2.0","id":3,"id":4,"method":"tools/call","params":{"name":"rm"}}` + "\n",
	}

	g := newRig(t, newDaemon(t))
	g.relay(frames...)

	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatalf("a frame naming a key twice reached the connector: %q", g.connector.String())
	}
	if n := len(g.daemon.calls()); n != 0 {
		t.Errorf("the daemon was asked to decide %d frames Nim could not read as one message", n)
	}
	// Not answered: with the id itself among the keys that may repeat, which
	// id to answer under is the same question as which method was meant.
	if s := g.client.String(); s != "" {
		t.Errorf("Nim invented an answer to a frame it could not read: %q", s)
	}
	if got := g.daemon.await(t, len(frames), anomalyOf(mcp.AnomalyDuplicateKey)); len(got) != len(frames) {
		t.Errorf("recorded %d duplicate_key anomalies, want %d", len(got), len(frames))
	}
}

// A key in a different case is a different key -- to a server, and now to
// Nim. `"Method":"ping"` used to win over `"method":"tools/call"` in Go's
// decoder, `"Name":"list_repos"` over `"name":"delete_repository"`, and
// `"ID":9` over `"id":1`: the daemon decided on one tool while the connector
// ran another, and the journal said the harmless one had been called.
func TestACaseVariantKeyCannotRenameTheCall(t *testing.T) {
	frame := `{"jsonrpc":"2.0","id":1,"ID":9,"method":"tools/call","Method":"ping",` +
		`"params":{"name":"delete_repository","Name":"list_repos","arguments":{}}}` + "\n"

	d := newDaemon(t)
	d.answer = func(ev daemon.Event) any {
		if ev.Tool == "delete_repository" {
			return deny("delete_repository is on the list")(ev)
		}
		return daemon.Decision{Kind: daemon.KindDecision, SessionID: ev.SessionID, Seq: ev.Seq,
			Decision: journal.DecisionAllow}
	}
	g := newRig(t, d)
	g.relay(frame)

	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatalf("the denied call reached the connector under another name: %q", g.connector.String())
	}
	calls := g.daemon.calls()
	if len(calls) != 1 || calls[0].Tool != "delete_repository" {
		t.Fatalf("the daemon was asked about %+v; want exactly one call naming delete_repository", calls)
	}
	got := answers(t, g.client)
	if len(got) != 1 || string(got[0].ID) != "1" || !got[0].Result.IsError {
		t.Fatalf("the client was answered %+v; want one refusal under id 1, the id it sent", got)
	}
}

// A tools/call Nim cannot read as exactly one tool name is refused before a
// sequence number is spent or the daemon is asked. The daemon used to be asked
// about tool "" for every one of these, and allowed it.
func TestACallWithoutAReadableNameIsRefusedBeforeAnyDecision(t *testing.T) {
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call"}` + "\n",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":0}}` + "\n",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"a","name":"rm"}}` + "\n",
		`{"jsonrpc":"2.0","method":"tools/call","params":{"arguments":{}}}` + "\n",
	}

	g := newRig(t, newDaemon(t))
	g.relay(frames...)

	if n := len(g.connector.Bytes()); n != 0 {
		t.Fatalf("a call Nim could not name reached the connector: %q", g.connector.String())
	}
	if n := len(g.daemon.calls()); n != 0 {
		t.Errorf("the daemon was asked to decide %d calls Nim could not name", n)
	}
	if g.shim.seq != 0 {
		t.Errorf("seq advanced to %d for calls that were never decided", g.shim.seq)
	}

	got := answers(t, g.client)
	if len(got) != 3 {
		t.Fatalf("answered %d frames, want the three carrying an id", len(got))
	}
	for i, a := range got {
		if string(a.ID) != fmt.Sprint(i+1) || !a.Result.IsError {
			t.Errorf("answer %d = %+v; want a refusal under id %d", i, a, i+1)
		}
		if len(a.Result.Content) == 0 || a.Result.Content[0].Text != mcp.DeniedUnreadable {
			t.Errorf("answer %d does not carry the unreadable-call refusal", i)
		}
	}
	if n := len(g.daemon.await(t, 3, anomalyOf(mcp.AnomalyUnreadableCall))); n != 3 {
		t.Errorf("recorded %d unreadable_call anomalies, want 3", n)
	}
	if n := len(g.daemon.await(t, 1, anomalyOf(mcp.AnomalyDuplicateKey))); n != 1 {
		t.Errorf("recorded %d duplicate_key anomalies, want 1", n)
	}
}
