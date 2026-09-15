package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The arguments a human approves are the model's to write, and a terminal
// obeys escape sequences and bidirectional overrides wherever they appear.
// What nim approve prints must show every byte as itself, or the human
// approves what they saw rather than what was there.
func TestApproveShowsEveryByteOfTheArgumentsAsItself(t *testing.T) {
	// A well-formed argument: JSON already keeps the ESC escaped, and the
	// bidirectional override is a valid, invisible character.
	wellFormed := json.RawMessage(`{"path":"/tmp/safe\u001b[2K/etc/passwd","note":"\u202Ereversed"}`)
	out := prettyArguments(wellFormed)
	if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x202E) {
		t.Fatalf("a control or format character reached the terminal:\n%q", out)
	}
	if !strings.Contains(out, `\u001b`) || !strings.Contains(out, `\u202E`) {
		t.Errorf("the escapes were not shown as text:\n%s", out)
	}

	// The fallback path, for bytes json.Indent refuses: a raw ESC inside a
	// string is not valid JSON and used to be printed as it came.
	raw := json.RawMessage("{\"path\":\"\x1b[2Kgone\"}")
	out = prettyArguments(raw)
	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("a raw ESC reached the terminal on the fallback path:\n%q", out)
	}
	if !strings.Contains(out, `\u001B`) || !strings.Contains(out, "gone") {
		t.Errorf("the fallback did not show the bytes as text:\n%s", out)
	}

	// Ordinary arguments are untouched: indentation, unicode and all.
	plain := json.RawMessage(`{"text":"café ñandú 日本語 🚀","n":42}`)
	out = prettyArguments(plain)
	for _, want := range []string{"café ñandú 日本語 🚀", `"n": 42`} {
		if !strings.Contains(out, want) {
			t.Errorf("ordinary arguments were altered:\n%s", out)
		}
	}
}
