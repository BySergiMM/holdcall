package shim

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/daemon"
)

// closeWithTimeout bounds reporter.close() so a regression that makes it hang
// fails the test instead of the whole suite.
func closeWithTimeout(t *testing.T, r *reporter) {
	t.Helper()
	done := make(chan struct{})
	go func() { r.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reporter.close() did not return in time")
	}
}

func TestReporterEncodesEventsInOrder(t *testing.T) {
	client, server := net.Pipe()
	r := &reporter{conn: server, ch: make(chan daemon.Event, 4), done: make(chan struct{})}
	go r.loop()

	r.send(daemon.Event{Kind: daemon.KindCall, SessionID: "s1", Seq: 1})
	r.send(daemon.Event{Kind: daemon.KindCall, SessionID: "s1", Seq: 2})

	dec := json.NewDecoder(client)
	var ev1, ev2 daemon.Event
	if err := dec.Decode(&ev1); err != nil {
		t.Fatalf("decode first event: %v", err)
	}
	if err := dec.Decode(&ev2); err != nil {
		t.Fatalf("decode second event: %v", err)
	}
	if ev1.Seq != 1 || ev2.Seq != 2 {
		t.Fatalf("events arrived out of order: got seq %d then %d", ev1.Seq, ev2.Seq)
	}

	closeWithTimeout(t, r)
}

// The relay's first duty is to be invisible: a shim started before the
// daemon comes up (or one that never manages to reach it) must not hang or
// panic, just silently drop what it cannot deliver.
func TestReporterDropsEventsWhenThereIsNoConnection(t *testing.T) {
	r := &reporter{conn: nil, ch: make(chan daemon.Event, 4), done: make(chan struct{})}
	go r.loop()

	r.send(daemon.Event{Kind: daemon.KindCall, Seq: 1})
	closeWithTimeout(t, r)
}

func TestReporterSendNeverBlocksOnceTheQueueIsFull(t *testing.T) {
	// loop() is deliberately not started: nothing drains the channel, so
	// send must still never block once it fills up.
	r := &reporter{conn: nil, ch: make(chan daemon.Event, 1), done: make(chan struct{})}
	r.send(daemon.Event{Seq: 1})

	done := make(chan struct{})
	go func() { r.send(daemon.Event{Seq: 2}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("send blocked once the queue was full; a tool call must never stall on bookkeeping")
	}
	if len(r.ch) != 1 {
		t.Fatalf("queue length = %d, want 1 (the second send must have been dropped, not queued)", len(r.ch))
	}
}

// A write failure mid-session (the daemon restarts, the socket goes away)
// must not take the relay down or spin the loop retrying forever.
func TestReporterStopsSendingAfterAWriteErrorAndStillClosesCleanly(t *testing.T) {
	client, server := net.Pipe()
	client.Close() // the peer is already gone before loop ever writes to it

	r := &reporter{conn: server, ch: make(chan daemon.Event, 4), done: make(chan struct{})}
	go r.loop()

	r.send(daemon.Event{Seq: 1}) // this write fails; loop must nil out r.conn, not panic
	closeWithTimeout(t, r)
}
