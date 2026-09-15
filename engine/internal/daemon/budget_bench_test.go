package daemon

import (
	"testing"
	"time"
)

// BenchmarkDecisionWithBudget is BenchmarkDecisionRoundTrip with one budget
// configured -- the cost checkBudgets adds to every allowed call once M5
// exists, over and above what the M4.5 figures in docs/benchmarks.md
// already measured.
//
// The budget's cap is set far above b.N, so every call is allowed and none
// is ever denied: what this isolates is the added cost of MatchingBudgets
// (one indexed query against nim_budgets, the same shape as MatchingRules)
// plus CountAllowedCalls (one indexed read on nim_journal, bounded by
// (session_id, seq, kind) -- see journal.CountAllowedCalls) on the path
// every allowed call now takes, not the cost of a budget actually refusing
// one.
//
//	go test -run XXX -bench BenchmarkDecision -benchtime 2000x ./internal/daemon/ 2>/dev/null
func BenchmarkDecisionWithBudget(b *testing.B) {
	quiet(b)
	cfg, _ := start(b)
	policyChange(b, cfg, Request{
		ID: "bench-budget", Kind: KindBudgetSet, BudgetTool: "list_repos", BudgetCalls: MaxBudgetCalls,
	})
	conn, enc, dec := openSession(b, sockPath(cfg.Daemon.Socket), "bench-budget-session")
	defer conn.Close()

	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		samples = append(samples, decideOnce(b, conn, enc, dec, "bench-budget-session", i+1))
	}
	b.StopTimer()
	reportLatencies(b, samples)
}
