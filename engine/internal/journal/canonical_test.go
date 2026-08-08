package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sha256Of(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func sp(s string) *string { return &s }
func ip(v int64) *int64   { return &v }
func bp(v bool) *bool     { return &v }

// sample is a fully populated entry: every field carries a value, so a change
// to any of them shows up.
func sample() Entry {
	return Entry{
		ChainSeq:        7,
		SchemaVersion:   SchemaVersion1,
		Kind:            KindCallRequest,
		SessionID:       "0123456789abcdef",
		Seq:             ip(3),
		Connector:       sp("github"),
		Tool:            sp("create_issue"),
		ParamsDigest:    sp("e3b0c44298fc1c149afbf4c8996fb924"),
		Decision:        sp(DecisionObserved),
		OK:              bp(true),
		DurationMS:      ip(42),
		Anomaly:         sp("batch"),
		OccurredAt:      "2026-08-07T10:00:00.123456789Z",
		MachineID:       sp("machine"),
		Client:          sp("claude-code"),
		ProtocolVersion: sp("2025-06-18"),
	}
}

// A second implementation has to reproduce these exact bytes, so the encoding
// is pinned. If this fails and the change was deliberate, it is not a matter of
// updating the constant: entries already on disk were hashed under the old
// rule, which is what schema_version exists to express.
func TestCanonicalEncodingIsPinned(t *testing.T) {
	const want = "6cf347948fa5905ab898fdeabab8aa4c9b86c0244f4b18fdf1c335542c07d4e2"

	got := hex.EncodeToString(sha256Of(canonicalEncodeV1(sample())))
	if got != want {
		t.Fatalf("canonical_encode_v1 changed\n got: %s\nwant: %s\n"+
			"If this was intended, docs/journal-format.md and SchemaVersion must change with it.", got, want)
	}
}

// readField is a decoder written from docs/journal-format.md rather than from
// canonical.go. The encoding's whole promise is that a second implementation
// reproduces it, so something here has to read the bytes the way a stranger
// with the document would, instead of trusting the writer's own helpers.
func readField(t *testing.T, b []byte) (tag byte, payload []byte, rest []byte) {
	t.Helper()
	if len(b) < 9 {
		t.Fatalf("field header truncated: %d bytes left", len(b))
	}
	tag = b[0]
	length := uint64(0)
	for _, c := range b[1:9] {
		length = length<<8 | uint64(c)
	}
	if uint64(len(b)-9) < length {
		t.Fatalf("field claims %d bytes, only %d remain", length, len(b)-9)
	}
	return tag, b[9 : 9+length], b[9+length:]
}

// Sixteen fields, in the documented order, with the documented tags.
func TestEncodingMatchesTheDocumentedLayout(t *testing.T) {
	rest := canonicalEncodeV1(sample())

	const prefix = "nim.journal.v1\n"
	if string(rest[:len(prefix)]) != prefix {
		t.Fatalf("missing domain prefix, got %q", rest[:len(prefix)])
	}
	rest = rest[len(prefix):]

	wantTags := []byte{
		tagInt,    // 1  chain_seq
		tagInt,    // 2  schema_version
		tagString, // 3  kind
		tagString, // 4  session_id
		tagInt,    // 5  seq
		tagString, // 6  connector
		tagString, // 7  tool
		tagString, // 8  params_digest
		tagString, // 9  decision
		tagBool,   // 10 ok
		tagInt,    // 11 duration_ms
		tagString, // 12 anomaly
		tagString, // 13 occurred_at
		tagString, // 14 machine_id
		tagString, // 15 client
		tagString, // 16 protocol_version
	}

	var payloads [][]byte
	for i, want := range wantTags {
		var tag byte
		var payload []byte
		tag, payload, rest = readField(t, rest)
		if tag != want {
			t.Fatalf("field %d has tag %q, want %q", i+1, tag, want)
		}
		payloads = append(payloads, payload)
	}
	if len(rest) != 0 {
		t.Fatalf("%d bytes left after sixteen fields", len(rest))
	}

	if got := string(payloads[2]); got != KindCallRequest {
		t.Errorf("field 3 (kind) = %q", got)
	}
	if got := string(payloads[6]); got != "create_issue" {
		t.Errorf("field 7 (tool) = %q", got)
	}
	if len(payloads[9]) != 1 || payloads[9][0] != 1 {
		t.Errorf("field 10 (ok) = %v, want a single 0x01", payloads[9])
	}
	if len(payloads[0]) != 8 {
		t.Errorf("field 1 (chain_seq) is %d bytes, want a fixed 8", len(payloads[0]))
	}
}

// An entry with every optional field absent still carries sixteen fields.
func TestNullFieldsStillOccupyTheirPosition(t *testing.T) {
	rest := canonicalEncodeV1(Entry{Kind: "k", SessionID: "s", OccurredAt: "t"})
	rest = rest[len("nim.journal.v1\n"):]

	nulls := 0
	for i := 0; i < 16; i++ {
		var tag byte
		tag, _, rest = readField(t, rest)
		if tag == tagNull {
			nulls++
		}
	}
	if len(rest) != 0 {
		t.Fatalf("%d bytes left after sixteen fields", len(rest))
	}
	// chain_seq, schema_version, kind, session_id and occurred_at are always
	// present; the other eleven are not.
	if nulls != 11 {
		t.Fatalf("counted %d null fields, want 11", nulls)
	}
}

func TestEncodingIsDeterministic(t *testing.T) {
	a := canonicalEncodeV1(sample())
	b := canonicalEncodeV1(sample())
	if !bytes.Equal(a, b) {
		t.Fatal("the same entry encoded differently twice")
	}
}

// null, "" and 0 must never collide: an absent column and an empty one mean
// different things, and a chain that cannot tell them apart cannot detect one
// being turned into the other.
func TestNullEmptyAndZeroAreDistinct(t *testing.T) {
	null := Entry{Kind: "k", SessionID: "s", OccurredAt: "t"}

	empty := null
	empty.Tool = sp("")

	zero := null
	zero.Seq = ip(0)

	enc := func(e Entry) string { return hex.EncodeToString(canonicalEncodeV1(e)) }
	if enc(null) == enc(empty) {
		t.Error("a null field encoded the same as an empty string")
	}
	if enc(null) == enc(zero) {
		t.Error("a null field encoded the same as zero")
	}
	if enc(empty) == enc(zero) {
		t.Error("an empty string encoded the same as zero")
	}
}

// Without a length prefix, "xy" + "" and "x" + "y" would be indistinguishable.
// This is the property that makes the encoding unambiguous without separators.
func TestFieldBoundariesAreUnambiguous(t *testing.T) {
	a := Entry{Kind: "k", SessionID: "s", OccurredAt: "t", Tool: sp("xy"), ParamsDigest: sp("")}
	b := Entry{Kind: "k", SessionID: "s", OccurredAt: "t", Tool: sp("x"), ParamsDigest: sp("y")}

	if bytes.Equal(canonicalEncodeV1(a), canonicalEncodeV1(b)) {
		t.Fatal("two different field splits produced the same bytes")
	}
}

// Position is encoded, so an entry cannot be moved without its hash changing.
func TestChainSeqIsPartOfTheEncoding(t *testing.T) {
	a := sample()
	b := sample()
	b.ChainSeq = a.ChainSeq + 1

	if bytes.Equal(canonicalEncodeV1(a), canonicalEncodeV1(b)) {
		t.Fatal("chain_seq did not affect the encoding")
	}
}

// Deliberate: the encoder stores what was observed. Normalising here would mean
// the journal recorded something other than what arrived. NFC vs NFD of the
// same word must therefore encode differently.
func TestUnicodeIsEncodedVerbatim(t *testing.T) {
	// Written as escapes so the distinction survives any editor: the same word
	// as one precomposed rune, and as a plain letter plus a combining accent.
	nfc := Entry{Kind: "k", SessionID: "s", OccurredAt: "t", Tool: sp("caf\u00e9")}
	nfd := Entry{Kind: "k", SessionID: "s", OccurredAt: "t", Tool: sp("cafe\u0301")}
	same := Entry{Kind: "k", SessionID: "s", OccurredAt: "t", Tool: sp("caf\u00e9")}

	if bytes.Equal(canonicalEncodeV1(nfc), canonicalEncodeV1(nfd)) {
		t.Error("NFC and NFD encoded identically: something normalised the input")
	}
	if !bytes.Equal(canonicalEncodeV1(nfc), canonicalEncodeV1(same)) {
		t.Error("identical strings encoded differently")
	}
}

func TestIntegersEncodeAtFixedWidth(t *testing.T) {
	for _, v := range []int64{0, -1, 1, 255, -255, 1 << 40, -(1 << 40)} {
		e := Entry{Kind: "k", SessionID: "s", OccurredAt: "t", Seq: ip(v)}
		enc := canonicalEncodeV1(e)

		other := Entry{Kind: "k", SessionID: "s", OccurredAt: "t", Seq: ip(v + 1)}
		if bytes.Equal(enc, canonicalEncodeV1(other)) {
			t.Errorf("%d and %d encoded identically", v, v+1)
		}
		// Every entry has the same shape, so length cannot vary with the value.
		if len(enc) != len(canonicalEncodeV1(other)) {
			t.Errorf("encoding length changed with the value of an integer (%d)", v)
		}
	}
}

func TestBooleansAreDistinct(t *testing.T) {
	yes := Entry{Kind: "k", SessionID: "s", OccurredAt: "t", OK: bp(true)}
	no := Entry{Kind: "k", SessionID: "s", OccurredAt: "t", OK: bp(false)}
	null := Entry{Kind: "k", SessionID: "s", OccurredAt: "t"}

	if bytes.Equal(canonicalEncodeV1(yes), canonicalEncodeV1(no)) {
		t.Error("true and false encoded identically")
	}
	if bytes.Equal(canonicalEncodeV1(no), canonicalEncodeV1(null)) {
		t.Error("false encoded the same as null")
	}
}

// The genesis is per install, so two machines replaying the same entries do not
// produce the same chain.
func TestGenesisDependsOnTheMachine(t *testing.T) {
	if genesisHash("one") == genesisHash("two") {
		t.Fatal("two installs seeded the same chain")
	}
	if genesisHash("one") != genesisHash("one") {
		t.Fatal("the genesis is not stable for one install")
	}
}

func TestChainHashDependsOnBothInputs(t *testing.T) {
	enc := canonicalEncodeV1(sample())
	prevA := genesisHash("a")
	prevB := genesisHash("b")

	if chainHash(prevA, enc) == chainHash(prevB, enc) {
		t.Error("the previous hash did not affect the link")
	}
	if chainHash(prevA, enc) == chainHash(prevA, append(enc, 'x')) {
		t.Error("the entry contents did not affect the link")
	}
}
