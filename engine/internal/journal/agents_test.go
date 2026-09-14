package journal

import (
	"testing"
	"time"
)

// An enrolment and the entry recording it are one transaction: the chain
// never describes an enrolment that does not exist, and no enrolment exists
// that the chain does not know about. Removing a name that was never
// enrolled writes nothing -- the "none" case RemoveRule has for a rule that
// is not there, except an enrolment removal is not an error: the daemon's
// handler relies on that to keep `nim agent remove` idempotent.
func TestAnEnrolmentAndItsEntryAreOneChange(t *testing.T) {
	j, _ := openTemp(t)
	before := chainLength(t, j)

	enrolledAt := time.Now()
	if err := j.AddAgent(Agent{
		Name: "claude-code", ExecDev: 7, ExecIno: 42, ExecPath: "/bin/claude", EnrolledAt: enrolledAt,
	}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if got := chainLength(t, j); got != before+1 {
		t.Fatalf("enrolling wrote %d entries, want 1", got-before)
	}
	entries, _ := j.EntriesSince(before, 10)
	e := entries[0]
	if e.Kind != KindAgentAdd || e.SessionID != "" || e.Agent == nil || *e.Agent != "claude-code" ||
		e.ExecPath == nil || *e.ExecPath != "/bin/claude" || e.ExecID == nil || *e.ExecID != "7:42" {
		t.Fatalf("the agent.add entry does not describe the enrolment: %+v", e)
	}

	a, found, err := j.RemoveAgent("claude-code")
	if err != nil {
		t.Fatalf("RemoveAgent: %v", err)
	}
	if !found || a.Name != "claude-code" || a.ExecDev != 7 || a.ExecIno != 42 {
		t.Fatalf("RemoveAgent returned %+v, found=%v", a, found)
	}
	if agents, _ := j.ListAgents(); len(agents) != 0 {
		t.Fatalf("%d enrolments remain after removal", len(agents))
	}
	if got := chainLength(t, j); got != before+2 {
		t.Fatalf("removing wrote %d entries, want 1", got-before-1)
	}
	entries, _ = j.EntriesSince(before+1, 10)
	if entries[0].Kind != KindAgentRemove || *entries[0].Agent != "claude-code" ||
		*entries[0].ExecPath != "/bin/claude" || *entries[0].ExecID != "7:42" {
		t.Fatalf("the agent.remove entry does not describe the removed enrolment: %+v", entries[0])
	}

	// The "none" case: a name nothing enrolled writes nothing, and is not an
	// error -- unlike RemoveRule, which refuses a change to nothing.
	_, found, err = j.RemoveAgent("claude-code")
	if err != nil {
		t.Fatalf("removing an absent enrolment returned an error: %v", err)
	}
	if found {
		t.Fatal("RemoveAgent reported found=true for a name that was just removed")
	}
	if got := chainLength(t, j); got != before+2 {
		t.Fatalf("removing an absent enrolment left %d entries behind", got-before-2)
	}
}

// Either half failing rolls back the other, in both directions: an enrolment
// the chain does not know about, or an entry describing an enrolment that was
// never stored, is exactly the lie one transaction exists to make impossible.
func TestAnEnrolmentChangeThatCannotBeRecordedIsNotMade(t *testing.T) {
	j, path := openTemp(t)
	before := chainLength(t, j)

	// The entry cannot be written: the enrolment must not exist afterwards.
	tamper(t, path, `create trigger no_agent_entries before insert on nim_journal
		when new.kind = 'agent.add' begin select raise(abort, 'no'); end`)
	if err := j.AddAgent(Agent{Name: "claude-code", ExecDev: 1, ExecIno: 1, ExecPath: "/bin/claude", EnrolledAt: time.Now()}); err == nil {
		t.Fatal("AddAgent succeeded although its entry could not be written")
	}
	if agents, _ := j.ListAgents(); len(agents) != 0 {
		t.Fatalf("an enrolment was stored with no entry recording it: %+v", agents)
	}
	tamper(t, path, `drop trigger no_agent_entries`)

	// The enrolment cannot be stored: the chain must not say it was.
	tamper(t, path, `create trigger no_agents before insert on nim_agents
		begin select raise(abort, 'no'); end`)
	if err := j.AddAgent(Agent{Name: "claude-code", ExecDev: 1, ExecIno: 1, ExecPath: "/bin/claude", EnrolledAt: time.Now()}); err == nil {
		t.Fatal("AddAgent succeeded although the enrolment could not be stored")
	}
	if got := chainLength(t, j); got != before {
		t.Fatalf("the chain gained %d entries for an enrolment that was never stored", got-before)
	}
}

// Re-enrolling a name replaces the identity it was bound to, and writes a
// fresh agent.add carrying the new one -- not an update to the old entry.
// That is the whole point of F-018: every rule scoped to this name now
// applies to a different executable, and the chain has to say so.
func TestReEnrollingWritesAnAgentAddWithTheNewIdentity(t *testing.T) {
	j, _ := openTemp(t)
	before := chainLength(t, j)

	if err := j.AddAgent(Agent{
		Name: "claude-code", ExecDev: 1, ExecIno: 1, ExecPath: "/bin/old", EnrolledAt: time.Now(),
	}); err != nil {
		t.Fatalf("first enrolment: %v", err)
	}
	if err := j.AddAgent(Agent{
		Name: "claude-code", ExecDev: 2, ExecIno: 2, ExecPath: "/bin/new", EnrolledAt: time.Now(),
	}); err != nil {
		t.Fatalf("re-enrolment: %v", err)
	}

	if got := chainLength(t, j); got != before+2 {
		t.Fatalf("two enrolments under one name wrote %d entries, want 2", got-before)
	}
	entries, _ := j.EntriesSince(before, 10)
	if len(entries) != 2 || entries[0].Kind != KindAgentAdd || entries[1].Kind != KindAgentAdd {
		t.Fatalf("re-enrolling did not write two agent.add entries: %+v", entries)
	}
	if *entries[0].ExecPath != "/bin/old" || *entries[0].ExecID != "1:1" {
		t.Fatalf("the first entry lost its identity: %+v", entries[0])
	}
	if *entries[1].ExecPath != "/bin/new" || *entries[1].ExecID != "2:2" {
		t.Fatalf("the second entry does not carry the new identity: %+v", entries[1])
	}

	// The enrolment itself -- what a decision actually reads -- reflects only
	// the new identity. The old entry stays in the chain as history.
	a, found, err := j.AgentNamed("claude-code")
	if err != nil || !found {
		t.Fatalf("AgentNamed: %v, found=%v", err, found)
	}
	if a.ExecDev != 2 || a.ExecIno != 2 || a.ExecPath != "/bin/new" {
		t.Fatalf("the enrolment did not move to the new identity: %+v", a)
	}
}

// Removing an enrolment writes agent.remove carrying the identity that was
// removed, so the chain says what stopped being enrolled, not only that
// something did.
func TestRemovingWritesAnAgentRemoveWithTheOldIdentity(t *testing.T) {
	j, _ := openTemp(t)
	if err := j.AddAgent(Agent{
		Name: "claude-code", ExecDev: 9, ExecIno: 99, ExecPath: "/bin/claude", EnrolledAt: time.Now(),
	}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}

	if _, _, err := j.RemoveAgent("claude-code"); err != nil {
		t.Fatalf("RemoveAgent: %v", err)
	}

	entries, _ := j.EntriesSince(0, 10)
	remove := entries[len(entries)-1]
	if remove.Kind != KindAgentRemove || remove.Agent == nil || *remove.Agent != "claude-code" {
		t.Fatalf("the last entry is not agent.remove for claude-code: %+v", remove)
	}
	if remove.ExecPath == nil || *remove.ExecPath != "/bin/claude" {
		t.Fatalf("agent.remove lost the removed exec_path: %+v", remove)
	}
	if remove.ExecID == nil || *remove.ExecID != "9:99" {
		t.Fatalf("agent.remove lost the removed exec_id: %+v", remove)
	}
}

// An enrolment change is covered by the chain like a rule or a call: editing
// it after the fact breaks verification.
func TestAlteringAnAgentAddEntryBreaksTheChain(t *testing.T) {
	j, path := openTemp(t)
	if err := j.AddAgent(Agent{
		Name: "claude-code", ExecDev: 1, ExecIno: 1, ExecPath: "/bin/claude", EnrolledAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if rep, _ := j.Verify(""); !rep.OK {
		t.Fatalf("the chain does not verify with an enrolment entry in it: %s", rep.Problem)
	}
	tamper(t, path, `update nim_journal set exec_path = '/bin/someone-else' where kind = 'agent.add'`)
	rep, err := j.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Fatal("a rewritten agent.add entry still verified")
	}
}

// A read-only handle can look at the enrolments and change none of them.
func TestAReadOnlyJournalCannotChangeEnrolments(t *testing.T) {
	w, path := openTemp(t)
	if err := w.AddAgent(Agent{
		Name: "claude-code", ExecDev: 1, ExecIno: 1, ExecPath: "/bin/claude", EnrolledAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	w.Close()

	j, err := OpenReadOnly(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if agents, err := j.ListAgents(); err != nil || len(agents) != 1 {
		t.Fatalf("ListAgents = %v, %v", agents, err)
	}
	if err := j.AddAgent(Agent{Name: "cursor", ExecDev: 2, ExecIno: 2, ExecPath: "/bin/cursor", EnrolledAt: time.Now()}); err == nil {
		t.Error("a read-only journal accepted an enrolment")
	}
	if _, _, err := j.RemoveAgent("claude-code"); err == nil {
		t.Error("a read-only journal removed an enrolment")
	}
}
