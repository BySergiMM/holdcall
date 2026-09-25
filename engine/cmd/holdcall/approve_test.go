package main

import (
	"bytes"
	"encoding/json"
	"github.com/BySergiMM/holdcall/engine/internal/daemon"
	"strings"
	"testing"
)

// The arguments a human approves are the model's to write, and a terminal
// obeys escape sequences and bidirectional overrides wherever they appear.
// What holdcall approve prints must show every byte as itself, or the human
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

// A call whose arguments the relay has not reported yet is not a call with
// no arguments, and holdcall approve must not let a human mistake one for the
// other: the daemon refuses to approve the first, and the text says why.
func TestApproveSaysWhenTheArgumentsHaveNotArrived(t *testing.T) {
	var out bytes.Buffer
	printPending(&out, daemon.PendingInfo{ID: "s1-1", Tool: "send_email", StartedAt: "2026-09-15T10:00:00Z"})
	if !strings.Contains(out.String(), "not received from the relay yet") || strings.Contains(out.String(), "(none)") {
		t.Fatalf("an unreported call was not described as such:\n%s", out.String())
	}

	out.Reset()
	printPending(&out, daemon.PendingInfo{ID: "s1-2", Tool: "ping", ArgumentsKnown: true, StartedAt: "2026-09-15T10:00:00Z"})
	if !strings.Contains(out.String(), "(none)") || strings.Contains(out.String(), "not received") {
		t.Fatalf("a call with no arguments was not described as such:\n%s", out.String())
	}
}
