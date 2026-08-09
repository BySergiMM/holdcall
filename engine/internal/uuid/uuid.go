// Package uuid mints random identifiers in the form the Supabase mirror
// expects. nim_sessions.id and nim_calls.id/session_id are typed uuid there
// (see supabase/migrations); an id minted locally has to already look like
// one, or every row fails to sync once M8 exists.
package uuid

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"time"
)

var fallbackCounter atomic.Uint32

// New returns a random UUID (version 4, RFC 4122).
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Vanishingly rare, but nothing here may block on it. The clock alone
		// can repeat within a nanosecond-resolution tick under a tight loop
		// of rand failures, so a counter fills the rest of the id to keep
		// even consecutive fallbacks unique.
		binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint32(b[8:12], fallbackCounter.Add(1))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
