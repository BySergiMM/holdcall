package peer

import (
	"net"
	"strings"
	"testing"
)

// A secret may be released only to a peer the platform confirmed, and "the
// platform could not say" is not a confirmation. Verified is the one question
// that tells those apart, which Supported and Same alone do not: a caller that
// refuses only when supported && !same lets an unsupported platform through.
func TestAVerdictIsVerifiedOnlyWhenThePlatformAnsweredYes(t *testing.T) {
	cases := []struct {
		name         string
		v            Verdict
		wantVerified bool
		wantReason   string // a fragment; "" means no reason at all
	}{
		{"the platform cannot ask", Verdict{}, false, "cannot tell who"},
		{"asked, and it is another binary", Verdict{Supported: true, PID: 7}, false, "not running this binary"},
		{"asked, and it is the same binary as another user",
			Verdict{Supported: true, PID: 7, WrongUser: true, PeerUID: 1001, SelfUID: 1000}, false, "uid 1001"},
		{"asked, and it is this binary as this user", Verdict{Supported: true, Same: true, PID: 7}, true, ""},
		// Same without Supported cannot come out of a platform check, but the
		// type allows it, and Verified must not be fooled by it.
		{"a zero verdict that somehow says same", Verdict{Same: true}, false, "cannot tell who"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.v.Verified(); got != c.wantVerified {
				t.Errorf("Verified() = %v, want %v", got, c.wantVerified)
			}
			reason := c.v.Reason()
			switch {
			case c.wantReason == "" && reason != "":
				t.Errorf("a verified peer has a reason to be refused: %q", reason)
			case c.wantReason != "" && !strings.Contains(reason, c.wantReason):
				t.Errorf("Reason() = %q, want it to say %q", reason, c.wantReason)
			}
		})
	}
}

// Anything that is not a unix-domain connection is not something this package
// can ask the kernel about, and must come back unsupported rather than as an
// answer -- on every platform, including the ones that can answer for a real
// one.
func TestAConnectionThatIsNotAUnixSocketIsUnsupportedNotRefusedAndNotVerified(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	for name, conn := range map[string]net.Conn{"a pipe": a, "nothing": nil} {
		v := Check(conn)
		if v.Supported || v.Same || v.Verified() {
			t.Errorf("%s: Check = %+v, want the zero verdict", name, v)
		}
		if supported, same := IsSelf(conn); supported || same {
			t.Errorf("%s: IsSelf = (%v, %v), want (false, false)", name, supported, same)
		}
	}
}
