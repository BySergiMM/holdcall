package readmodel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/peer"
)

// anExecutable writes a file that looks enough like a program to be enrolled,
// and returns its path. Mirrors daemon.anExecutable: the same fixture shape,
// because stillEnrolled must agree with daemon.stillTheEnrolledFile about
// what "the enrolled file" means.
func anExecutable(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// A freshly enrolled agent's file has not moved, so it must read as current
// -- wherever this platform can tell at all.
func TestAFreshEnrolmentReadsAsCurrent(t *testing.T) {
	path := anExecutable(t, "claude")
	img, err := peer.ImageOfFile(path)
	if !peer.FileIdentitySupported {
		t.Skip("this platform cannot answer; see TestCurrentIsNilWhereThePlatformCannotAnswer")
	}
	if err != nil {
		t.Fatalf("ImageOfFile: %v", err)
	}

	a := journal.Agent{
		Name: "claude-code", ExecPath: path, ExecDev: img.Dev(), ExecIno: img.Ino(),
		EnrolledAt: time.Now(),
	}
	got := AgentFrom(a)
	if got.Current == nil || !*got.Current {
		t.Fatalf("a freshly enrolled agent read as %v, want current", got.Current)
	}
}

// An enrolment whose file has been replaced must read as stale, not fail and
// not silently follow the new file -- the same property
// daemon.TestAReplacedFileIsReportedStaleRatherThanFailing establishes for
// `nim agent list`, which this must agree with since both compute it the
// same way.
func TestAReplacedFileReadsAsStale(t *testing.T) {
	if !peer.FileIdentitySupported {
		t.Skip("this platform cannot answer; see TestCurrentIsNilWhereThePlatformCannotAnswer")
	}
	path := anExecutable(t, "claude")
	img, err := peer.ImageOfFile(path)
	if err != nil {
		t.Fatalf("ImageOfFile: %v", err)
	}
	a := journal.Agent{
		Name: "claude-code", ExecPath: path, ExecDev: img.Dev(), ExecIno: img.Ino(),
		EnrolledAt: time.Now(),
	}

	// Replace the file the way a self-updating application does: write the
	// new one alongside and rename over the old, so the identity is
	// guaranteed distinct rather than incidentally reused (ext4 will hand
	// back the inode number it just freed for a remove-then-create).
	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("writing the replacement: %v", err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("renaming over: %v", err)
	}

	got := AgentFrom(a)
	if got.Current == nil {
		t.Fatal("a replaced file read as unknown on a platform that can answer")
	}
	if *got.Current {
		t.Fatal("a replaced file was still reported as the enrolled one")
	}
}

// An enrolment whose file is simply gone must read as stale too, the same as
// a mismatch: nothing running today can be recognised as this enrolment
// either way.
func TestAMissingFileReadsAsStale(t *testing.T) {
	if !peer.FileIdentitySupported {
		t.Skip("this platform cannot answer; see TestCurrentIsNilWhereThePlatformCannotAnswer")
	}
	a := journal.Agent{
		Name: "gone", ExecPath: filepath.Join(t.TempDir(), "does-not-exist"),
		ExecDev: 1, ExecIno: 2, EnrolledAt: time.Now(),
	}
	got := AgentFrom(a)
	if got.Current == nil || *got.Current {
		t.Fatalf("a missing file read as %v, want stale", got.Current)
	}
}

// Wherever peer says this platform cannot answer, Current must be nil, not
// false. A caller that read nil as false would tell an operator their
// enrolment had gone stale when nothing was actually checked -- exactly the
// distinction peer.FileIdentitySupported exists to preserve, and the one a
// bare `if err != nil { return false }` would erase.
func TestCurrentIsNilWhereThePlatformCannotAnswer(t *testing.T) {
	a := journal.Agent{
		Name: "x", ExecPath: anExecutable(t, "x"), EnrolledAt: time.Now(),
	}
	got := AgentFrom(a)
	if peer.FileIdentitySupported {
		if got.Current == nil {
			t.Fatal("Current is nil even though peer reports this platform can answer")
		}
	} else if got.Current != nil {
		t.Fatal("Current answered on a platform peer reports cannot support it")
	}
}

// A rule with no agent or connector means "every", and that has to survive
// the projection as a nil pointer, not an empty string -- an empty string is
// never what nim_rules stores for "every" (see journal.go), and a reader
// that saw "" would not know whether that meant every or nothing.
func TestRuleFromKeepsEveryAsNil(t *testing.T) {
	agent, connector := "cursor", "github"
	rows := []journal.Rule{
		{Tool: "rm", CreatedAt: time.Now()},
		{Tool: "write_file", Agent: &agent, Connector: &connector, CreatedAt: time.Now()},
	}
	got := RulesFrom(rows)
	if got[0].Agent != nil || got[0].Connector != nil {
		t.Errorf("a scopeless rule gained a scope: %+v", got[0])
	}
	if got[1].Agent == nil || *got[1].Agent != "cursor" || got[1].Connector == nil || *got[1].Connector != "github" {
		t.Errorf("a scoped rule lost its scope: %+v", got[1])
	}
}

// Projecting must copy, the same discipline EventFrom holds to: a reader
// that could write through Connector.Command would be reaching back into the
// row the driver just scanned.
func TestConnectorFromCopiesTheCommand(t *testing.T) {
	command := []string{"npx", "-y", "server"}
	c := journal.Connector{Target: "github", EnvKey: "GITHUB_TOKEN", Command: command, UpdatedAt: time.Now()}

	got := ConnectorFrom(c)
	got.Command[0] = "changed"

	if command[0] != "npx" {
		t.Fatal("ConnectorFrom aliased the journal's own slice")
	}
}

// The journal never holds a connector's secret, and neither may this
// projection: only the env var name and the authorized argv reach it.
func TestConnectorFromNeverCarriesASecret(t *testing.T) {
	c := journal.Connector{Target: "github", EnvKey: "GITHUB_TOKEN", Command: []string{"npx"}, UpdatedAt: time.Now()}
	out, err := json.Marshal(ConnectorFrom(c))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret", "credential", "token_value"} {
		if hasField(t, out, forbidden) {
			t.Errorf("Connector JSON carries a %q field: %s", forbidden, out)
		}
	}
}

func hasField(t *testing.T, doc []byte, field string) bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatal(err)
	}
	_, ok := m[field]
	return ok
}

// fakePolicy is a PolicySource with no database behind it, the same shape as
// fake's other methods: a projection is a function of its input.
type fakePolicy struct {
	rules      []journal.Rule
	budgets    []journal.Budget
	agents     []journal.Agent
	connectors []journal.Connector
	err        error
}

func (f *fakePolicy) ListRules() ([]journal.Rule, error)     { return f.rules, f.err }
func (f *fakePolicy) ListBudgets() ([]journal.Budget, error) { return f.budgets, f.err }
func (f *fakePolicy) MatchingRules(agent, connector, tool string) ([]journal.Rule, error) {
	return f.rules, f.err
}
func (f *fakePolicy) MatchingBudgets(agent, connector, tool string) ([]journal.Budget, error) {
	return f.budgets, f.err
}
func (f *fakePolicy) ListAgents() ([]journal.Agent, error)         { return f.agents, f.err }
func (f *fakePolicy) ListConnectors() ([]journal.Connector, error) { return f.connectors, f.err }

// TakePolicy assembles all three listings from one source, and none of them
// is nil when the source returns none -- the console encodes this as JSON,
// and an absent list should read as "none", not as a field that vanished.
func TestTakePolicyAssemblesAllThreeAndNeverReturnsNilSlices(t *testing.T) {
	pol, err := TakePolicy(&fakePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if pol.Rules == nil || pol.Budgets == nil || pol.Agents == nil || pol.Connectors == nil {
		t.Fatalf("an empty policy has nil slices: %+v", pol)
	}
	out, err := json.Marshal(pol)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"rules":[]`, `"budgets":[]`, `"agents":[]`, `"connectors":[]`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("empty policy JSON = %s, want %s", out, want)
		}
	}
}

// A read failure on any of the three must be reported, not swallowed into an
// empty-looking policy.
func TestTakePolicyReportsAReadFailure(t *testing.T) {
	if _, err := TakePolicy(&fakePolicy{err: errors.New("journal unavailable")}); err == nil {
		t.Fatal("TakePolicy hid a read failure")
	}
}

// A budget reaches the reader with its scope and its cap, copied, in the
// order the journal lists them. Found missing by the M5 review: nim policy
// list showed budgets while nim status and the console, which read this
// projection, showed a session as unbounded that was not.
func TestTakePolicyProjectsBudgets(t *testing.T) {
	agent := "claude-code"
	when := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	pol, err := TakePolicy(&fakePolicy{budgets: []journal.Budget{
		{ID: 1, Tool: journal.BudgetToolAll, Calls: 100, CreatedAt: when},
		{ID: 2, Agent: &agent, Tool: "rm", Calls: 3, CreatedAt: when.Add(time.Second)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pol.Budgets) != 2 {
		t.Fatalf("budgets = %+v, want two", pol.Budgets)
	}
	if pol.Budgets[0].Tool != "*" || pol.Budgets[0].Calls != 100 || pol.Budgets[0].Agent != nil {
		t.Errorf("the all-tools budget was projected as %+v", pol.Budgets[0])
	}
	if pol.Budgets[1].Agent == nil || *pol.Budgets[1].Agent != agent || pol.Budgets[1].Calls != 3 {
		t.Errorf("the agent-scoped budget was projected as %+v", pol.Budgets[1])
	}
	if pol.Budgets[1].Agent == &agent {
		t.Error("the projection aliases the journal's memory")
	}
	if pol.Budgets[0].CreatedAt != "2026-09-15T10:00:00Z" {
		t.Errorf("created_at = %q", pol.Budgets[0].CreatedAt)
	}
}

// Explain says what a call shape gets and which rule decided it, through
// journal.Decide -- the one precedence function -- so a console that shows
// it cannot drift from what the daemon does.
func TestExplainNamesTheDecidingRuleAndTheBudgetsThatApply(t *testing.T) {
	agent := "claude-code"
	rules := []journal.Rule{
		{ID: 1, Tool: journal.RuleToolDefault, Effect: journal.DecisionDeny},
		{ID: 2, Agent: &agent, Tool: "rm", Effect: journal.DecisionAsk},
	}
	budgets := []journal.Budget{{ID: 1, Tool: "rm", Calls: 5}}
	ex, err := Explain(&fakePolicy{rules: rules, budgets: budgets}, agent, "", "rm")
	if err != nil {
		t.Fatal(err)
	}
	if ex.Decision != journal.DecisionAsk || !ex.ByRule || ex.Rule == nil || ex.Rule.Tool != "rm" {
		t.Fatalf("the exact, agent-scoped ask should decide: %+v", ex)
	}
	if len(ex.Matching) != 2 || len(ex.Budgets) != 1 || ex.Budgets[0].Calls != 5 {
		t.Errorf("matching rules and budgets were not all reported: %+v", ex)
	}

	none, err := Explain(&fakePolicy{}, "", "", "ls")
	if err != nil {
		t.Fatal(err)
	}
	if none.Decision != journal.DecisionAllow || none.ByRule || none.Rule != nil || none.Matching == nil || none.Budgets == nil {
		t.Errorf("no matching rule is allow, with empty lists rather than nil: %+v", none)
	}
}
