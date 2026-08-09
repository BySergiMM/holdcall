package uuid

import (
	"regexp"
	"testing"
)

var pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// The Supabase mirror stores this value in a uuid column; anything that does
// not match RFC 4122's textual form is rejected at insert time.
func TestNewIsShapedLikeAUUID(t *testing.T) {
	for i := 0; i < 100; i++ {
		got := New()
		if !pattern.MatchString(got) {
			t.Fatalf("New() = %q, does not match a version-4 UUID", got)
		}
	}
}

func TestNewDoesNotRepeat(t *testing.T) {
	seen := make(map[string]bool, 10000)
	for i := 0; i < 10000; i++ {
		id := New()
		if seen[id] {
			t.Fatalf("New() repeated %q after %d draws", id, i)
		}
		seen[id] = true
	}
}
