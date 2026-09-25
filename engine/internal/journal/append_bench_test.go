package journal

import (
	"path/filepath"
	"testing"
	"time"
)

// The journal write is inside the decision round trip -- the entry is durable
// before the answer is sent -- so it is the floor under every number in
// internal/daemon's benchmarks. Measuring it alone says how much of that cost
// is SQLite and how much is everything else.
//
//	go test -bench BenchmarkAppend -benchtime 2000x ./internal/journal/
func benchJournal(b *testing.B) *Journal {
	b.Helper()
	j, err := Open(filepath.Join(b.TempDir(), "holdcall.db"), "bench-machine")
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { j.Close() })
	return j
}

func callRequest(seq int64) Entry {
	tool := "list_repositories"
	digest := "0000000000000000000000000000000000000000000000000000000000000000"
	decision := DecisionAllow
	return Entry{
		Kind:         KindCallRequest,
		SessionID:    "bench-session",
		Seq:          &seq,
		Tool:         &tool,
		ParamsDigest: &digest,
		Decision:     &decision,
		OccurredAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// BenchmarkAppend is one chained, committed entry: read the head, encode,
// hash, insert, commit. synchronous is FULL, so this includes the flush.
func BenchmarkAppend(b *testing.B) {
	j := benchJournal(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := j.Append(callRequest(int64(i + 1))); err != nil {
			b.Fatalf("Append: %v", err)
		}
	}
}

// BenchmarkAppendContended is four writers against the one journal, which is
// what several relays deciding at once produce. The mutex and _txlock=immediate
// are what keep this from failing rather than merely queueing -- see Open.
func BenchmarkAppendContended(b *testing.B) {
	j := benchJournal(b)
	const writers = 4
	per := b.N / writers
	if per < 1 {
		per = 1
	}

	b.ResetTimer()
	done := make(chan error, writers)
	for w := 0; w < writers; w++ {
		go func(w int) {
			for i := 0; i < per; i++ {
				e := callRequest(int64(w*per + i + 1))
				e.SessionID = "bench-session"
				if err := j.Append(e); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}(w)
	}
	for w := 0; w < writers; w++ {
		if err := <-done; err != nil {
			b.Fatalf("Append under contention: %v", err)
		}
	}
}

// BenchmarkCanonicalEncode is the hashing input alone, with no I/O. It is the
// part a second implementation has to reproduce byte for byte, so it is worth
// knowing that it costs almost nothing next to the commit.
func BenchmarkCanonicalEncode(b *testing.B) {
	e := callRequest(1)
	e.ChainSeq, e.SchemaVersion = 1, SchemaVersion1
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = canonicalEncodeV1(e)
	}
}

// BenchmarkVerify walks and rechecks a whole chain. This is what `holdcall verify`
// costs, and it grows with the journal rather than staying flat, so it is the
// number that decides whether verification stays practical as a record ages.
func BenchmarkVerify(b *testing.B) {
	j := benchJournal(b)
	const entries = 2000
	for i := 0; i < entries; i++ {
		if err := j.Append(callRequest(int64(i + 1))); err != nil {
			b.Fatalf("seeding: %v", err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rep, err := j.Verify("")
		if err != nil {
			b.Fatalf("Verify: %v", err)
		}
		if !rep.OK {
			b.Fatalf("seeded journal did not verify: %s", rep.Problem)
		}
	}
	b.ReportMetric(float64(entries), "entries")
}
