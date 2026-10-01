package main

import (
	"bufio"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BySergiMM/holdcall/engine/internal/console"
)

// consoleGet reads path from c served by srv the way the page does, with the
// token in a header. The console refuses every read without it.
func consoleGet(t *testing.T, srv *httptest.Server, c *console.Server, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token())
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// waitForTheJournal returns once the journal can be opened for reading, which
// `holdcall log` does and refuses to do before the daemon has created it.
func waitForTheJournal(t *testing.T, s *stack) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := s.run(t, "log"); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the daemon never created a journal that can be read")
}

// What the command prints is the only way the person who started the console
// learns its token, so the address has to carry it where the page looks, in
// the fragment, which a browser keeps out of every request.
func TestTheAddressTheConsolePrintsCarriesTheTokenInItsFragment(t *testing.T) {
	got := consoleURL("127.0.0.1:7717", "abc123")
	if got != "http://127.0.0.1:7717/#token=abc123" {
		t.Errorf("consoleURL = %q", got)
	}
}

// The real command, a real browser-shaped client: the address it prints opens
// the record, and the same address without its token does not. This is the
// claim -- "a process that was not handed the address cannot read the journal"
// -- tried against the binary rather than against a handler built in a test.
func TestTheConsoleTheCommandStartsAnswersOnlyToWhoWasHandedItsAddress(t *testing.T) {
	s := build(t)
	s.daemon(t)
	// The daemon binds its socket before it opens the journal, so "running" does
	// not yet mean there is one for the console to read. Without this the test
	// passed wherever the daemon was quick and failed where it was not (a CI
	// runner, Windows above all) with the console saying it had no journal yet.
	waitForTheJournal(t, s)

	cmd := exec.Command(s.holdcall, "console", "--addr", "127.0.0.1:0")
	cmd.Env = s.env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// Kept, not discarded: a console that could not start says why on stderr and
	// prints no address, and "the first line is empty" is no help to whoever reads
	// a CI log.
	var stderr syncBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	// The first line is the address. Read it with a deadline rather than trust the
	// command to print one.
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- line
	}()
	var first string
	select {
	case first = <-lines:
	case <-time.After(20 * time.Second):
		t.Fatal("the console printed no address")
	}

	m := regexp.MustCompile(`^holdcall console on (http://127\.0\.0\.1:\d+)/#token=([0-9a-f]+)\s*$`).FindStringSubmatch(first)
	if m == nil {
		t.Fatalf("the first line is not the address with a token in its fragment: %q\nstderr: %s", first, stderr.String())
	}
	base, token := m[1], m[2]
	if raw, err := hex.DecodeString(token); err != nil || len(raw) < 16 {
		t.Fatalf("the token %q is not at least 128 bits of hex", token)
	}

	read := func(authorization string) int {
		req, _ := http.NewRequest(http.MethodGet, base+"/api/snapshot", nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if got := read(""); got != http.StatusUnauthorized {
		t.Errorf("a read with no token returned %d, want 401", got)
	}
	if got := read("Bearer " + strings.Repeat("0", len(token))); got != http.StatusUnauthorized {
		t.Errorf("a read with a wrong token returned %d, want 401", got)
	}
	if got := read("Bearer " + token); got != http.StatusOK {
		t.Errorf("a read with the printed token returned %d, want 200", got)
	}
}
