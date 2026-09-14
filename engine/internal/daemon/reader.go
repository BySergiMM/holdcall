package daemon

import (
	"bufio"
	"fmt"
)

// maxLineBytes bounds how much of a single incoming line this daemon socket
// will ever buffer, independently of and before MaxRequestBytes (which only
// rejects an already-fully-buffered Request-kind message).
//
// Found during the M3 final audit: handle() used to read connections with
// mcp.NewReader, whose bufio.Reader.ReadBytes('\n') buffers a line of
// unbounded length -- correct for the shim's own client<->downstream relay,
// where a large tool-call result is legitimate (see mcp.NewReader's own
// comment), but wrong here. Every message this specific socket carries is
// small: Event.Digest is always a fixed-length SHA-256 hex hash, never raw
// tool output, and Request-kind messages are already capped at
// MaxRequestBytes (64 KiB). A raw, unauthenticated connection (nc, python,
// any local process -- this check runs before peer verification and before
// MaxRequestBytes, since both require the line to be read first) sending a
// single line with no newline used to grow the daemon's memory without
// bound; verified live: 200 MiB sent from one unauthenticated connection in
// 0.2s grew the daemon's RSS by the same amount, with no pushback at all.
// The relay fails closed when the daemon is unreachable (see shim.decide),
// so crashing the single shared daemon this way would deny every tools/call
// on the machine, not just the attacker's own session -- one unauthenticated
// local process disabling every agent's tools, which is not merely an
// availability nuisance. 1 MiB is generous headroom over any legitimate
// message on this connection while remaining a hard, small bound.
const maxLineBytes = 1 << 20

// boundedReadRaw reads one newline-delimited message from br, the same
// contract as mcp.Reader.ReadRaw (including returning a final line with no
// trailing newline as-is on EOF), except it refuses to accumulate more than
// max bytes: once exceeded, it returns an error instead of continuing to
// grow its buffer, so the caller can close the connection rather than keep
// serving it.
func boundedReadRaw(br *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > max {
			return nil, fmt.Errorf("message exceeds %d byte limit", max)
		}
		if err == nil {
			return buf, nil
		}
		if err == bufio.ErrBufferFull {
			continue // ReadSlice hit its own internal buffer, not the newline; keep accumulating
		}
		if len(buf) > 0 {
			return buf, nil
		}
		return nil, err
	}
}
