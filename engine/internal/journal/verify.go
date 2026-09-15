package journal

import (
	"database/sql"
	"fmt"
)

// VerifyReport is the outcome of walking the chain.
//
// Three outcomes, not two. A journal can be sound, it can be wrong, or it can
// be one this build has no way to judge -- and reporting the third as the
// second would accuse someone of tampering when the only thing that happened is
// that verification material went missing.
type VerifyReport struct {
	Entries int64  // chain length walked
	Head    string // hash of the last entry, "" when the journal is empty
	OK      bool   // the chain is self-consistent as far as it could be checked
	Empty   bool   // there was nothing to check
	Partial bool   // checked, but the seed was unknown so entry 1 stands unverified
	Problem string // first failure, naming the chain_seq it was found at

	// ExpectedHeadAt is the chain_seq the head passed to Verify was found at,
	// or 0 if it was not in the chain at all. Only meaningful when an expected
	// head was given.
	//
	// It exists to tell two very different things apart. A journal that has
	// grown since the head was recorded no longer *ends* at it, but still
	// contains it -- and everything up to it is still covered, because each
	// entry's hash commits to its predecessor. A journal that was truncated
	// and recomputed does not contain it anywhere.
	ExpectedHeadAt int64
}

// Verify walks the chain from the genesis and checks four things, in the order
// a break would be noticed: that chain_seq is contiguous from 1, that every
// schema_version is one this build understands, that each prev_hash matches the
// previous entry's hash, and that each hash matches the recomputation.
//
// expectHead, when not empty, is looked for in the chain -- not merely
// compared against the final hash. That is the only check here that catches a
// chain which has been truncated and recomputed, and looking rather than
// comparing is what keeps it usable: a journal that has grown since the head
// was recorded no longer ends at it, and saying "entries have been removed"
// about ordinary growth is an accusation, not a finding. An operator who is
// told that every time will stop reading it, which costs the one check that
// detects the attack.
//
// Finding the expected head anywhere in the chain establishes what the check
// is for: everything up to that point is intact, because each entry's hash
// commits to its predecessor.
//
// What Verify does not detect: an attacker who can write to nim.db can also
// recompute every hash from the genesis onwards, and the result verifies
// cleanly. Nothing in the chain is secret, so there is nothing they lack. Only
// a head recorded elsewhere, or a journal owned by another OS user, closes
// that. docs/journal-format.md says the same at more length; it is repeated
// here so nobody reads this function and concludes more than it does.
func (j *Journal) Verify(expectHead string) (VerifyReport, error) {
	rows, err := j.db.Query(
		`select chain_seq, schema_version, kind, session_id, seq, connector, tool,
		        params_digest, decision, ok, duration_ms, anomaly, occurred_at,
		        machine_id, client, protocol_version, agent, exec_path, exec_id, budget_calls, prev_hash, hash
		   from nim_journal order by chain_seq`)
	if err != nil {
		return VerifyReport{}, err
	}
	defer rows.Close()

	rep := VerifyReport{Partial: !j.seedKnown}
	expectedSeq := int64(1)
	prevHash := j.genesis
	first := true

	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return rep, err
		}

		// Without the seed, the first entry has nothing to be checked against.
		// Its own contents are still covered, one link later, by the prev_hash
		// of the entry after it.
		if first && !j.seedKnown {
			prevHash = e.PrevHash
		}
		first = false

		if e.ChainSeq != expectedSeq {
			rep.Problem = fmt.Sprintf(
				"chain_seq %d is missing: the entry after %d is %d, so %d %s been removed",
				expectedSeq, expectedSeq-1, e.ChainSeq, e.ChainSeq-expectedSeq,
				plural(e.ChainSeq-expectedSeq, "entry has", "entries have"))
			return rep, nil
		}
		if !knownSchemaVersion(e.SchemaVersion) {
			rep.Problem = fmt.Sprintf(
				"entry %d was written under schema_version %d, which this build does not know how to verify",
				e.ChainSeq, e.SchemaVersion)
			return rep, nil
		}
		if e.PrevHash != prevHash {
			rep.Problem = fmt.Sprintf(
				"entry %d does not link to the one before it: prev_hash is %s, expected %s",
				e.ChainSeq, short(e.PrevHash), short(prevHash))
			return rep, nil
		}
		if got := chainHash(e.PrevHash, canonicalEncode(e)); got != e.Hash {
			rep.Problem = fmt.Sprintf(
				"entry %d has been altered: its contents hash to %s but it stores %s",
				e.ChainSeq, short(got), short(e.Hash))
			return rep, nil
		}

		prevHash = e.Hash
		rep.Entries = e.ChainSeq
		rep.Head = e.Hash
		if expectHead != "" && e.Hash == expectHead {
			rep.ExpectedHeadAt = e.ChainSeq
		}
		expectedSeq++
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}

	if rep.Entries == 0 {
		// Nothing was checked. Saying so is not the same as saying the journal
		// is sound, and an empty journal is indistinguishable from one whose
		// every entry was lost.
		rep.Empty = true
		rep.Partial = false
		if expectHead != "" {
			rep.Problem = fmt.Sprintf(
				"the journal is empty, so it cannot have the head %s you expected", short(expectHead))
			return rep, nil
		}
		return rep, nil
	}

	if expectHead != "" && expectHead != rep.Head && rep.ExpectedHeadAt == 0 {
		rep.Problem = fmt.Sprintf(
			"the chain is internally consistent but the head %s you expected is nowhere in it, "+
				"and it now ends at %s: entries have been removed and the chain recomputed",
			short(expectHead), short(rep.Head))
		return rep, nil
	}

	rep.OK = true
	return rep, nil
}

// scanEntry rebuilds an entry from its stored columns, so the recomputation
// hashes what is on disk rather than what the writer meant to put there.
func scanEntry(rows *sql.Rows) (Entry, error) {
	var e Entry
	var seq, durationMS sql.NullInt64
	var ok sql.NullBool
	var connector, tool, digest, decision, anomaly sql.NullString
	var machineID, client, protocolVersion, agent, execPath, execIDCol sql.NullString
	var budgetCalls sql.NullInt64

	err := rows.Scan(
		&e.ChainSeq, &e.SchemaVersion, &e.Kind, &e.SessionID, &seq, &connector, &tool,
		&digest, &decision, &ok, &durationMS, &anomaly, &e.OccurredAt,
		&machineID, &client, &protocolVersion, &agent, &execPath, &execIDCol, &budgetCalls, &e.PrevHash, &e.Hash,
	)
	if err != nil {
		return e, err
	}

	if seq.Valid {
		e.Seq = &seq.Int64
	}
	if durationMS.Valid {
		e.DurationMS = &durationMS.Int64
	}
	if ok.Valid {
		e.OK = &ok.Bool
	}
	if budgetCalls.Valid {
		e.BudgetCalls = &budgetCalls.Int64
	}
	e.Connector = nullable(connector)
	e.Tool = nullable(tool)
	e.ParamsDigest = nullable(digest)
	e.Decision = nullable(decision)
	e.Anomaly = nullable(anomaly)
	e.MachineID = nullable(machineID)
	e.Client = nullable(client)
	e.ProtocolVersion = nullable(protocolVersion)
	e.Agent = nullable(agent)
	e.ExecPath = nullable(execPath)
	e.ExecID = nullable(execIDCol)
	return e, nil
}

func nullable(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func short(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
