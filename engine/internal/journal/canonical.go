package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// SchemaVersion1 is the encoding this build writes. Every entry carries the
// version it was written under, so a later version can change the field list
// without invalidating what is already on disk: verification dispatches on the
// stored value rather than assuming the current one.
const SchemaVersion1 = 1

// SchemaVersion2 adds a seventeenth field, agent, on session.start.
//
// It is a new version rather than a reuse of an existing field because the
// encoding is the thing the chain commits to: seventeen fields hashed under a
// v1 domain would produce a different hash for the same entry depending on
// which build read it, which is exactly what a version exists to prevent.
//
// Entries written under version 1 keep verifying with the v1 encoder. Verify
// dispatches on what each entry stored, not on what this build writes, so a
// journal that spans both versions checks out end to end. That was the point
// of carrying schema_version from the start.
const SchemaVersion2 = 2

// SchemaVersion3 adds two fields, exec_path and exec_id, on agent.add and
// agent.remove.
//
// Same reasoning as v2: an enrolment change is a policy change, so it has to
// leave an entry a second implementation can reproduce byte for byte, and
// that needs somewhere to put the path the operator enrolled and the identity
// the daemon resolved it to. Reusing agent (already field 17) would not do --
// agent is the enrolment's name, exec_path and exec_id are what that name was
// bound to at the moment of the change, and collapsing the two would make a
// re-enrolment indistinguishable from the enrolment it replaced.
const SchemaVersion3 = 3

// SchemaVersion4 adds one field, budget_calls, field 20, on budget.add and
// budget.remove.
//
// Same reasoning as v3: a budget is policy, like a rule or an enrolment, so
// setting or removing one has to leave an entry a second implementation can
// reproduce byte for byte, and that needs somewhere to put the cap. Reusing
// an existing field would not do, for the same reason v3 could not reuse
// agent for exec_path and exec_id: budget_calls is what a budget.add or
// budget.remove entry is actually about, and nothing else carries it.
const SchemaVersion4 = 4

// CurrentSchemaVersion is what new entries are written under.
const CurrentSchemaVersion = SchemaVersion4

// The normative definition of everything below is docs/journal-format.md. It is
// worth keeping the two in step: a second implementation has to reproduce these
// bytes exactly, and it will be written from the document, not from this file.
const (
	domainV1 = "nim.journal.v1\n"
	domainV2 = "nim.journal.v2\n"
	domainV3 = "nim.journal.v3\n"
	domainV4 = "nim.journal.v4\n"

	// The genesis is not versioned with the entry encoding. It seeds the
	// chain from the install's identifier and has nothing to do with how many
	// fields an entry has; changing it would invalidate every existing chain
	// for no reason.
	genesisV1 = "nim.journal.v1.genesis\n"
)

// Field tags. The tag and the length prefix are what make the encoding
// unambiguous: there are no separators, so no value can be mistaken for one.
const (
	tagString byte = 's'
	tagInt    byte = 'i'
	tagBool   byte = 'b'
	tagNull   byte = 'n'
)

// canonicalEncode renders an entry as the bytes that get hashed, under the
// version that entry was written with.
//
// An unknown version returns nil rather than guessing. Verify rejects such an
// entry by name before it gets here, and a nil encoding would fail the hash
// comparison anyway -- but returning something plausible for a version this
// build does not understand is how a future entry would silently verify
// against the wrong rules.
func canonicalEncode(e Entry) []byte {
	switch e.SchemaVersion {
	case SchemaVersion1:
		return canonicalEncodeV1(e)
	case SchemaVersion2:
		return canonicalEncodeV2(e)
	case SchemaVersion3:
		return canonicalEncodeV3(e)
	case SchemaVersion4:
		return canonicalEncodeV4(e)
	default:
		return nil
	}
}

// knownSchemaVersion reports whether this build can verify an entry written
// under v.
func knownSchemaVersion(v int64) bool {
	return v == SchemaVersion1 || v == SchemaVersion2 || v == SchemaVersion3 || v == SchemaVersion4
}

// canonicalEncodeV4 is v3 plus budget_calls, field 20.
//
// Set on budget.add, carrying the cap being set. On budget.remove it carries
// the cap that was removed, so the chain says what stopped applying rather
// than only that something did -- exactly the reasoning v3's exec_path and
// exec_id follow for an enrolment. Every other kind leaves it null.
func canonicalEncodeV4(e Entry) []byte {
	var b bytes.Buffer
	b.WriteString(domainV4)

	putInt(&b, e.ChainSeq)                 // 1
	putInt(&b, e.SchemaVersion)            // 2
	putString(&b, e.Kind)                  // 3
	putString(&b, e.SessionID)             // 4
	putIntOrNull(&b, e.Seq)                // 5
	putStringOrNull(&b, e.Connector)       // 6
	putStringOrNull(&b, e.Tool)            // 7
	putStringOrNull(&b, e.ParamsDigest)    // 8
	putStringOrNull(&b, e.Decision)        // 9
	putBoolOrNull(&b, e.OK)                // 10
	putIntOrNull(&b, e.DurationMS)         // 11
	putStringOrNull(&b, e.Anomaly)         // 12
	putString(&b, e.OccurredAt)            // 13
	putStringOrNull(&b, e.MachineID)       // 14
	putStringOrNull(&b, e.Client)          // 15
	putStringOrNull(&b, e.ProtocolVersion) // 16
	putStringOrNull(&b, e.Agent)           // 17
	putStringOrNull(&b, e.ExecPath)        // 18
	putStringOrNull(&b, e.ExecID)          // 19
	putIntOrNull(&b, e.BudgetCalls)        // 20

	return b.Bytes()
}

// canonicalEncodeV3 is v2 plus exec_path and exec_id, fields 18 and 19.
//
// Both are set on agent.add, carrying the enrolment being made. On
// agent.remove both are set from the enrolment being removed, so the chain
// says what was removed, not only that something was. Every other kind
// leaves them null, exactly as v2's agent is null off session.start.
func canonicalEncodeV3(e Entry) []byte {
	var b bytes.Buffer
	b.WriteString(domainV3)

	putInt(&b, e.ChainSeq)                 // 1
	putInt(&b, e.SchemaVersion)            // 2
	putString(&b, e.Kind)                  // 3
	putString(&b, e.SessionID)             // 4
	putIntOrNull(&b, e.Seq)                // 5
	putStringOrNull(&b, e.Connector)       // 6
	putStringOrNull(&b, e.Tool)            // 7
	putStringOrNull(&b, e.ParamsDigest)    // 8
	putStringOrNull(&b, e.Decision)        // 9
	putBoolOrNull(&b, e.OK)                // 10
	putIntOrNull(&b, e.DurationMS)         // 11
	putStringOrNull(&b, e.Anomaly)         // 12
	putString(&b, e.OccurredAt)            // 13
	putStringOrNull(&b, e.MachineID)       // 14
	putStringOrNull(&b, e.Client)          // 15
	putStringOrNull(&b, e.ProtocolVersion) // 16
	putStringOrNull(&b, e.Agent)           // 17
	putStringOrNull(&b, e.ExecPath)        // 18
	putStringOrNull(&b, e.ExecID)          // 19

	return b.Bytes()
}

// canonicalEncodeV2 is v1 plus agent, field 17.
//
// agent is the client program a session belongs to, as the daemon derived it
// from the kernel. It sits on session.start for the same reason connector
// does: it is a property of the session, and two immutable entries that both
// describe one thing can contradict each other where a join cannot.
func canonicalEncodeV2(e Entry) []byte {
	var b bytes.Buffer
	b.WriteString(domainV2)

	putInt(&b, e.ChainSeq)                 // 1
	putInt(&b, e.SchemaVersion)            // 2
	putString(&b, e.Kind)                  // 3
	putString(&b, e.SessionID)             // 4
	putIntOrNull(&b, e.Seq)                // 5
	putStringOrNull(&b, e.Connector)       // 6
	putStringOrNull(&b, e.Tool)            // 7
	putStringOrNull(&b, e.ParamsDigest)    // 8
	putStringOrNull(&b, e.Decision)        // 9
	putBoolOrNull(&b, e.OK)                // 10
	putIntOrNull(&b, e.DurationMS)         // 11
	putStringOrNull(&b, e.Anomaly)         // 12
	putString(&b, e.OccurredAt)            // 13
	putStringOrNull(&b, e.MachineID)       // 14
	putStringOrNull(&b, e.Client)          // 15
	putStringOrNull(&b, e.ProtocolVersion) // 16
	putStringOrNull(&b, e.Agent)           // 17

	return b.Bytes()
}

// canonicalEncodeV1 renders an entry as the bytes that get hashed.
//
// Sixteen fields in a fixed order, never sorted and never omitted: an absent
// field is encoded as null, so the output always carries all sixteen. prev_hash
// and hash are excluded -- they are the chain, not the content.
func canonicalEncodeV1(e Entry) []byte {
	var b bytes.Buffer
	b.WriteString(domainV1)

	putInt(&b, e.ChainSeq)                 // 1
	putInt(&b, e.SchemaVersion)            // 2
	putString(&b, e.Kind)                  // 3
	putString(&b, e.SessionID)             // 4
	putIntOrNull(&b, e.Seq)                // 5
	putStringOrNull(&b, e.Connector)       // 6
	putStringOrNull(&b, e.Tool)            // 7
	putStringOrNull(&b, e.ParamsDigest)    // 8
	putStringOrNull(&b, e.Decision)        // 9
	putBoolOrNull(&b, e.OK)                // 10
	putIntOrNull(&b, e.DurationMS)         // 11
	putStringOrNull(&b, e.Anomaly)         // 12
	putString(&b, e.OccurredAt)            // 13
	putStringOrNull(&b, e.MachineID)       // 14
	putStringOrNull(&b, e.Client)          // 15
	putStringOrNull(&b, e.ProtocolVersion) // 16

	return b.Bytes()
}

// putString writes the string's UTF-8 bytes verbatim: no escaping, no trimming,
// and no Unicode normalisation. Normalising belongs to the decision path, where
// two spellings of one name must not resolve differently. Doing it here would
// mean the journal recorded something other than what was observed.
func putString(b *bytes.Buffer, s string) {
	header(b, tagString, uint64(len(s)))
	b.WriteString(s)
}

// putInt writes a fixed-width int64. No decimal form, so no question about
// leading zeros, signs or separators.
func putInt(b *bytes.Buffer, v int64) {
	header(b, tagInt, 8)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(v))
	b.Write(buf[:])
}

func putBool(b *bytes.Buffer, v bool) {
	header(b, tagBool, 1)
	if v {
		b.WriteByte(1)
		return
	}
	b.WriteByte(0)
}

// putNull writes a tag with no payload. null, "" and 0 are three different
// encodings, so an absent column and an empty string never collide.
func putNull(b *bytes.Buffer) { header(b, tagNull, 0) }

func putStringOrNull(b *bytes.Buffer, s *string) {
	if s == nil {
		putNull(b)
		return
	}
	putString(b, *s)
}

func putIntOrNull(b *bytes.Buffer, v *int64) {
	if v == nil {
		putNull(b)
		return
	}
	putInt(b, *v)
}

func putBoolOrNull(b *bytes.Buffer, v *bool) {
	if v == nil {
		putNull(b)
		return
	}
	putBool(b, *v)
}

func header(b *bytes.Buffer, tag byte, length uint64) {
	b.WriteByte(tag)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], length)
	b.Write(buf[:])
}

// genesisHash seeds the chain, per install. Two installs do not produce the same
// chain from the same entries, so their journals stay distinguishable when they
// eventually share one table.
func genesisHash(machineID string) string {
	sum := sha256.Sum256(append([]byte(genesisV1), machineID...))
	return hex.EncodeToString(sum[:])
}

// chainHash links an entry to its predecessor.
//
// The previous hash enters as its 32 raw bytes, not as its hex text. Hashing
// the text would work too, but only if every implementation agreed on the case
// of the hex, which is one more thing to get wrong.
func chainHash(prevHex string, encoded []byte) string {
	prev, err := hex.DecodeString(prevHex)
	if err != nil {
		// Unreachable with hashes this package produced; a corrupted prev_hash
		// is caught by Verify, which compares the recomputation and reports the
		// entry. Falling back to the raw text keeps that comparison meaningful
		// instead of panicking inside the verifier.
		prev = []byte(prevHex)
	}
	h := sha256.New()
	h.Write(prev)
	h.Write(encoded)
	return hex.EncodeToString(h.Sum(nil))
}
