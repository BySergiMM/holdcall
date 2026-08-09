package shim

import (
	"bytes"
	"strings"
	"testing"

	"github.com/BySergiMM/nim/engine/internal/daemon"
	"github.com/BySergiMM/nim/engine/internal/mcp"
)

// newTestShim builds a Shim whose reporter never touches the network: events
// land straight in a channel this test reads from, so noteRequest/noteResponse
// can be exercised without a live daemon.
func newTestShim() (*Shim, *reporter) {
	r := &reporter{ch: make(chan daemon.Event, 16), done: make(chan struct{})}
	s := &Shim{
		opts:      Options{Target: "test"},
		sessionID: "s1",
		reporter:  r,
		inFlight:  make(map[string]pending),
	}
	return s, r
}

func parse(t *testing.T, raw string) mcp.Envelope {
	t.Helper()
	env, ok := mcp.Parse([]byte(raw))
	if !ok {
		t.Fatalf("failed to parse: %s", raw)
	}
	return env
}

func TestNoteRequestReportsAnAllowedCallWithNoOutcomeYet(t *testing.T) {
	s, r := newTestShim()
	s.noteRequest(parse(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"x":1}}}`))

	select {
	case ev := <-r.ch:
		if ev.Kind != daemon.KindCall || ev.SessionID != "s1" || ev.Tool != "echo" || ev.Seq != 1 {
			t.Fatalf("unexpected event: %+v", ev)
		}
		if ev.Decision != "allow" {
			t.Errorf("decision = %q, want allow (M1 never blocks)", ev.Decision)
		}
		if ev.OK != nil {
			t.Errorf("a request-time report must not claim an outcome yet, got OK=%v", *ev.OK)
		}
	default:
		t.Fatal("noteRequest did not report anything")
	}

	s.mu.Lock()
	_, tracked := s.inFlight["1"]
	s.mu.Unlock()
	if !tracked {
		t.Fatal("the call must be tracked in-flight so its response can be correlated")
	}
}

func TestNoteResponseCorrelatesByIDAndReportsTheOutcome(t *testing.T) {
	s, r := newTestShim()
	s.noteRequest(parse(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`))
	<-r.ch // drain the request-time report

	s.noteResponse(parse(t, `{"jsonrpc":"2.0","id":1,"result":{"isError":false,"content":[]}}`))

	select {
	case ev := <-r.ch:
		if ev.OK == nil || !*ev.OK {
			t.Fatalf("expected OK=true, got %v", ev.OK)
		}
		if ev.DurationMS == nil {
			t.Fatal("expected a duration")
		}
		if ev.Tool != "echo" || ev.Seq != 1 {
			t.Errorf("response report lost the call's identity: %+v", ev)
		}
	default:
		t.Fatal("noteResponse did not report anything")
	}

	s.mu.Lock()
	_, stillTracked := s.inFlight["1"]
	s.mu.Unlock()
	if stillTracked {
		t.Error("a matched response must be removed from in-flight tracking")
	}
}

// A failed tool call arrives as a *successful* JSON-RPC response carrying
// result.isError -- exactly the M1 finding recorded in docs/milestones.md.
func TestNoteResponseRecordsAFailedToolCallAsNotOK(t *testing.T) {
	s, r := newTestShim()
	s.noteRequest(parse(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom"}}`))
	<-r.ch

	s.noteResponse(parse(t, `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[]}}`))

	ev := <-r.ch
	if ev.OK == nil || *ev.OK {
		t.Fatalf("a tool that raised must be recorded as not OK, got %v", ev.OK)
	}
}

func TestNoteResponseIgnoresRepliesToUncorrelatedRequests(t *testing.T) {
	s, r := newTestShim()
	// A reply to something that was never a tracked tools/call (e.g.
	// "initialize") must not be reported at all.
	s.noteResponse(parse(t, `{"jsonrpc":"2.0","id":99,"result":{}}`))

	select {
	case ev := <-r.ch:
		t.Fatalf("unexpected report for an uncorrelated response: %+v", ev)
	default:
	}
}

func TestSeqIncrementsPerToolCall(t *testing.T) {
	s, r := newTestShim()
	s.noteRequest(parse(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a"}}`))
	s.noteRequest(parse(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"b"}}`))
	<-r.ch
	<-r.ch
	if s.seq != 2 {
		t.Fatalf("seq = %d, want 2", s.seq)
	}
}

type nopWriteCloser struct{ *bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }

// The relay's whole promise is that bytes pass through untouched while a
// tools/call is still recognised on the way -- this is that promise exercised
// through pumpRequests specifically, complementing mcp.TestReadRawIsByteExact
// which only covers the framing layer underneath it.
func TestPumpRequestsForwardsBytesUnchangedAndReportsToolCalls(t *testing.T) {
	s, r := newTestShim()
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"

	var out bytes.Buffer
	s.pumpRequests(strings.NewReader(input), nopWriteCloser{&out})

	if out.String() != input {
		t.Fatalf("pumpRequests altered the stream\n got: %q\nwant: %q", out.String(), input)
	}

	select {
	case ev := <-r.ch:
		if ev.Tool != "echo" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	default:
		t.Fatal("the tools/call in the stream was not reported")
	}
	select {
	case ev := <-r.ch:
		t.Fatalf("the notification must not be reported as a call: %+v", ev)
	default:
	}
}

func TestPumpResponsesForwardsBytesUnchangedAndClosesTheLoopOnPendingCalls(t *testing.T) {
	s, r := newTestShim()
	s.noteRequest(parse(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`))
	<-r.ch // drain the request-time report

	input := `{"jsonrpc":"2.0","id":1,"result":{"isError":false,"content":[]}}` + "\n"
	var out bytes.Buffer
	s.pumpResponses(strings.NewReader(input), &out)

	if out.String() != input {
		t.Fatalf("pumpResponses altered the stream\n got: %q\nwant: %q", out.String(), input)
	}

	ev := <-r.ch
	if ev.OK == nil || !*ev.OK {
		t.Fatalf("expected the response to be reported as OK, got %+v", ev)
	}
}
