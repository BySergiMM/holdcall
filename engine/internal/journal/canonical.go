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

// The normative definition of everything below is docs/journal-format.md. It is
// worth keeping the two in step: a second implementation has to reproduce these
// bytes exactly, and it will be written from the document, not from this file.
const (
	domainV1  = "nim.journal.v1\n"
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
