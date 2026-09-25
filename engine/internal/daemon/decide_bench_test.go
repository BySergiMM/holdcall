package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"slices"
	"sync"
	"testing"

	"time"
)

// The synchronous decision hop is the one thing this milestone added to the
// critical path of every tools/call, and the argument for making it
// fail-closed rests on it being cheap: shim.go's decisionTimeout says the hop
// costs a fraction of a millisecond, so two seconds cannot fire because the
// daemon is busy, only because it is wedged or gone.
//
// That number was measured once and written into a comment, which meant it
// could not be re-derived, regression-tested, or checked on anyone else's
// machine. These benchmarks make it reproducible.
//
// Go reports a mean, and a mean is the wrong statistic for a timeout: what
// matters is the tail. Both benchmarks collect every sample and report p50
// and p99 as custom metrics in milliseconds.
//
//	go test -bench BenchmarkDecision -benchtime 2000x ./internal/daemon/
//
// What is being measured is the whole round trip a relay actually waits on:
// write call.request to the socket, the daemon evaluates the deny list,
// appends the chained entry to SQLite and commits it, then writes the
// decision back and the client reads it. The journal write is included on
// purpose -- the entry is durable before the answer is sent, which is what
// makes a forwarded call a recorded call, and excluding it would measure
// something Holdcall never does.

// quiet silences the daemon's own logging for the duration of a benchmark.
//
// Not cosmetic: log writes to the same stream the benchmark reports on, and a
// "holdcall daemon listening on ..." line lands in the middle of the result row,
// which makes the numbers unparseable by benchstat and unreadable by anyone.
func quiet(b *testing.B) {
	b.Helper()
	prev := log.Writer()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(prev) })
}

// percentile returns the p'th percentile of sorted, using nearest-rank.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted))*p/100.0+0.5) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

func reportLatencies(b *testing.B, samples []time.Duration) {
	b.Helper()
	if len(samples) == 0 {
		b.Fatal("no samples collected")
	}
	slices.Sort(samples)
	ms := func(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }
	b.ReportMetric(ms(percentile(samples, 50)), "p50_ms")
	b.ReportMetric(ms(percentile(samples, 95)), "p95_ms")
	b.ReportMetric(ms(percentile(samples, 99)), "p99_ms")
	b.ReportMetric(ms(samples[len(samples)-1]), "max_ms")
}

// decideOnce sends one call.request and waits for its decision, returning how
// long the relay would have blocked.
func decideOnce(b *testing.B, conn net.Conn, enc *json.Encoder, dec *json.Decoder, session string, seq int) time.Duration {
	b.Helper()
	ev := Event{
		Kind: KindCallRequest, SessionID: session, Seq: seq, Tool: "list_repos",
		Digest:     "0000000000000000000000000000000000000000000000000000000000000000",
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	start := time.Now()
	if err := enc.Encode(ev); err != nil {
		b.Fatalf("encode: %v", err)
	}
	var d Decision
	if err := dec.Decode(&d); err != nil {
		b.Fatalf("decode: %v", err)
	}
	elapsed := time.Since(start)
	if d.Decision != "allow" {
		b.Fatalf("unexpected decision %q", d.Decision)
	}
	return elapsed
}

// openSession dials the daemon and starts a session on the connection, which
// is required before it may report calls.
func openSession(b *testing.B, cfg configLike, id string) (net.Conn, *json.Encoder, *json.Decoder) {
	b.Helper()
	conn, err := net.Dial("unix", cfg.socket())
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	if err := enc.Encode(Event{
		Kind: KindSessionStart, SessionID: id, MachineID: "bench",
		Connector: "github", OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		b.Fatalf("session.start: %v", err)
	}
	return conn, enc, dec
}

// configLike keeps the two benchmarks from depending on config's shape.
type configLike interface{ socket() string }

type sockPath string

func (s sockPath) socket() string { return string(s) }

// BenchmarkDecisionRoundTrip is one relay asking, which is the number
// decisionTimeout is set against.
func BenchmarkDecisionRoundTrip(b *testing.B) {
	quiet(b)
	cfg, _ := start(b)
	conn, enc, dec := openSession(b, sockPath(cfg.Daemon.Socket), "bench-session")
	defer conn.Close()

	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		samples = append(samples, decideOnce(b, conn, enc, dec, "bench-session", i+1))
	}
	b.StopTimer()
	reportLatencies(b, samples)
}

// BenchmarkDecisionRoundTripContended is sixteen relays against the single
// writer, which is the case the timeout has to survive: a client that spawns
// one shim per configured MCP server, all deciding at once.
func BenchmarkDecisionRoundTripContended(b *testing.B) {
	quiet(b)
	const relays = 16
	cfg, _ := start(b)

	perRelay := b.N / relays
	if perRelay < 1 {
		perRelay = 1
	}

	var mu sync.Mutex
	all := make([]time.Duration, 0, perRelay*relays)

	b.ResetTimer()
	var wg sync.WaitGroup
	for r := 0; r < relays; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			session := fmt.Sprintf("bench-session-%d", r)
			conn, enc, dec := openSession(b, sockPath(cfg.Daemon.Socket), session)
			defer conn.Close()

			local := make([]time.Duration, 0, perRelay)
			for i := 0; i < perRelay; i++ {
				local = append(local, decideOnce(b, conn, enc, dec, session, i+1))
			}
			mu.Lock()
			all = append(all, local...)
			mu.Unlock()
		}(r)
	}
	wg.Wait()
	b.StopTimer()
	reportLatencies(b, all)
}

// BenchmarkDecisionDenied is the refusal path. It should not be slower than
// allow -- a denial does the same journal write and the same rule lookup, so
// if the two diverged it would mean the rules themselves had become the
// expensive part, which for an indexed exact-name lookup they must never be.
func BenchmarkDecisionDenied(b *testing.B) {
	quiet(b)
	cfg, _ := start(b)
	deny(b, cfg, "denied_tool", "", "")
	conn, enc, dec := openSession(b, sockPath(cfg.Daemon.Socket), "bench-deny")
	defer conn.Close()

	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev := Event{
			Kind: KindCallRequest, SessionID: "bench-deny", Seq: i + 1, Tool: "denied_tool",
			Digest:     "0000000000000000000000000000000000000000000000000000000000000000",
			OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		start := time.Now()
		if err := enc.Encode(ev); err != nil {
			b.Fatalf("encode: %v", err)
		}
		var d Decision
		if err := dec.Decode(&d); err != nil {
			b.Fatalf("decode: %v", err)
		}
		samples = append(samples, time.Since(start))
		if d.Decision != "deny" {
			b.Fatalf("expected deny, got %q", d.Decision)
		}
	}
	b.StopTimer()
	reportLatencies(b, samples)
}

// BenchmarkCredentialLookup is the spawn-time round trip, measured at the
// handler so it excludes the OS credential store: what is being checked is
// Holdcall's own cost, and a real keychain read is the operating system's.
func BenchmarkCredentialLookup(b *testing.B) {
	j := freshJournal(b)
	store := newFakeStore()
	store.Set("github", "ghp_bench")
	if err := j.SetConnector("github", "GITHUB_TOKEN", []string{"server", "--flag"}, time.Now()); err != nil {
		b.Fatalf("SetConnector: %v", err)
	}
	locks := newTargetLocks()

	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		state := &requestState{}
		start := time.Now()
		resp := handleCredentialGet(
			Request{ID: "1", Kind: KindCredentialGet, Target: "github"}, state, j, store, locks)
		samples = append(samples, time.Since(start))
		if resp.Error != "" {
			b.Fatalf("unexpected error: %s", resp.Error)
		}
	}
	b.StopTimer()
	reportLatencies(b, samples)
}
