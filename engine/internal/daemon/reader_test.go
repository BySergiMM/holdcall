package daemon

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestBoundedReadRawReturnsALineUnderTheLimit(t *testing.T) {
	br := bufio.NewReaderSize(strings.NewReader("hello\nworld\n"), 16)
	line, err := boundedReadRaw(br, 1024)
	if err != nil {
		t.Fatalf("boundedReadRaw: %v", err)
	}
	if string(line) != "hello\n" {
		t.Fatalf("got %q, want %q", line, "hello\n")
	}
}

func TestBoundedReadRawReturnsAFinalLineWithNoTrailingNewline(t *testing.T) {
	br := bufio.NewReaderSize(strings.NewReader("no newline at all"), 16)
	line, err := boundedReadRaw(br, 1024)
	if err != nil {
		t.Fatalf("boundedReadRaw: %v", err)
	}
	if string(line) != "no newline at all" {
		t.Fatalf("got %q, want the whole line back on EOF", line)
	}
}

// The actual finding from the M3 final audit, isolated to this function: a
// peer sending bytes with no newline used to make ReadRaw (via
// mcp.Reader.ReadBytes) buffer without limit. boundedReadRaw must instead
// stop and report an error well before accumulating the whole input.
func TestBoundedReadRawRejectsALineOverTheLimitWithoutBufferingItAll(t *testing.T) {
	// 10 MiB with no newline, fed through a reader that reports its own byte
	// count as it is consumed -- if boundedReadRaw kept accumulating until
	// EOF (as the pre-fix implementation effectively did), this test would
	// observe the full 10 MiB having been read before any error came back.
	const total = 10 << 20
	const max = 1 << 20
	counting := &countingReader{r: bytes.NewReader(bytes.Repeat([]byte{'A'}, total))}
	br := bufio.NewReaderSize(counting, 4096)

	_, err := boundedReadRaw(br, max)
	if err == nil {
		t.Fatal("expected an error for a line far exceeding the limit")
	}
	if counting.n > max+65536 { // generous slack for internal buffering, nowhere near the full 10 MiB
		t.Fatalf("boundedReadRaw read %d bytes before giving up, want it to stop close to the %d byte limit", counting.n, max)
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}
