package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// End to end, with the real binary: a real daemon, a real relay, a real child
// process on the other side. The tests above prove the pieces; these prove that
// what a client sends either arrives at a connector or does not.

// connectorSource is a downstream MCP server small enough to compile in a test.
// It writes down every frame it is handed, so "the connector was not reached" is
// checked against what the connector saw rather than against Nim's own account.
const connectorSource = `package main

import (
	"bufio"
	"encoding/json"
	"os"
)

func main() {
	log, _ := os.Create(os.Getenv("CONNECTOR_LOG"))
	defer log.Close()

	r := bufio.NewReaderSize(os.Stdin, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			log.Write(line)
			log.Sync()
			var env struct {
				ID     json.RawMessage ` + "`json:\"id\"`" + `
				Method string          ` + "`json:\"method\"`" + `
			}
			if json.Unmarshal(line, &env) == nil && len(env.ID) > 0 && env.Method != "" {
				os.Stdout.Write([]byte(` + "`" + `{"jsonrpc":"2.0","id":` + "`" + ` +
					string(env.ID) +
					` + "`" + `,"result":{"content":[{"type":"text","text":"served"}],"isError":false}}` + "`" + ` + "\n"))
			}
		}
		if err != nil {
			break
		}
	}
	// Reached only when stdin closes, which is how a relay tears its server down.
	os.WriteFile(os.Getenv("CONNECTOR_LOG")+".exited", []byte("yes"), 0o600)
}
`

type stack struct {
	nim       string // the built binary
	connector string // the built downstream server
	home      string
	log       string // the connector's record of what it received
	env       []string
}

// build compiles the binary and a connector once per test.
func build(t *testing.T, denyList string) *stack {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain to build the binary with")
	}

	dir := t.TempDir()
	s := &stack{
		nim:       filepath.Join(dir, "nim"),
		connector: filepath.Join(dir, "connector"),
		home:      filepath.Join(dir, "home"),
		log:       filepath.Join(dir, "received.log"),
	}

	src := filepath.Join(dir, "connector.go")
	if err := os.WriteFile(src, []byte(connectorSource), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][]string{
		{"build", "-o", s.nim, "."},
		{"build", "-o", s.connector, src},
	} {
		cmd := exec.Command("go", c...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(c, " "), err, out)
		}
	}

	if err := os.MkdirAll(s.home, 0o700); err != nil {
		t.Fatal(err)
	}
	conf := "[daemon]\n"
	if denyList != "" {
		conf += "\n[policy]\ndeny = [" + denyList + "]\n"
	}
	if err := os.WriteFile(filepath.Join(s.home, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}

	s.env = append(os.Environ(), "NIM_HOME="+s.home, "CONNECTOR_LOG="+s.log)
	return s
}

// daemon starts one and returns a function that kills it.
func (s *stack) daemon(t *testing.T) func() {
	t.Helper()
	cmd := exec.Command(s.nim, "daemon")
	cmd.Env = s.env
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	stop := func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}
	t.Cleanup(stop)

	// Wait for it to answer rather than guessing.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := s.run(t, "status")
		if strings.Contains(out, "daemon   running") {
			return stop
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the daemon did not come up")
	return stop
}

func (s *stack) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(s.nim, args...)
	cmd.Env = s.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// relay is a running `nim serve` with its stdio in this test's hands.
type relay struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  chan string
	stderr *strings.Builder
	mu     sync.Mutex
}

func (s *stack) serve(t *testing.T) *relay {
	t.Helper()
	cmd := exec.Command(s.nim, "serve", "--connector", "rig", "--client", "e2e", "--", s.connector)
	cmd.Env = s.env

	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var errbuf strings.Builder
	r := &relay{cmd: cmd, in: in, lines: make(chan string, 32), stderr: &errbuf}
	cmd.Stderr = &lockedWriter{w: &errbuf, mu: &r.mu}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(r.lines)
		br := bufio.NewReaderSize(out, 1<<20)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				r.lines <- string(line)
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { r.kill() })
	return r
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (r *relay) send(t *testing.T, frame string) {
	t.Helper()
	if _, err := io.WriteString(r.in, frame+"\n"); err != nil {
		t.Fatalf("writing to the relay: %v", err)
	}
}

// next reads one message from the relay, or gives up.
func (r *relay) next(t *testing.T) string {
	t.Helper()
	select {
	case line, ok := <-r.lines:
		if !ok {
			t.Fatal("the relay closed its output")
		}
		return line
	case <-time.After(15 * time.Second):
		t.Fatalf("no answer from the relay\nstderr:\n%s", r.errors())
		return ""
	}
}

// silent reports whether the relay says nothing for a while.
func (r *relay) silent(d time.Duration) bool {
	select {
	case <-r.lines:
		return false
	case <-time.After(d):
		return true
	}
}

func (r *relay) errors() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stderr.String()
}

func (r *relay) kill() {
	if r.cmd.Process != nil {
		r.cmd.Process.Kill()
		r.cmd.Wait()
	}
}

func (s *stack) received(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(s.log)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeLine(t *testing.T, line string) (id string, isError bool, text string) {
	t.Helper()
	var got struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the relay sent something that is not JSON-RPC: %v\n%s", err, line)
	}
	if len(got.Result.Content) > 0 {
		text = got.Result.Content[0].Text
	}
	return string(got.ID), got.Result.IsError, text
}

// One allowed call and one refused one, through the whole stack.
func TestRealRelayForwardsOneCallAndRefusesTheOther(t *testing.T) {
	s := build(t, `"dangerous_tool"`)
	s.daemon(t)
	r := s.serve(t)

	allowed := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"café 🚀"}}}`
	refused := `{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"dangerous_tool","arguments":{}}}`

	r.send(t, allowed)
	id, isError, _ := decodeLine(t, r.next(t))
	if id != "1" || isError {
		t.Errorf("the allowed call came back as id=%s isError=%v", id, isError)
	}

	r.send(t, refused)
	id, isError, text := decodeLine(t, r.next(t))
	if id != "9007199254740993" {
		t.Errorf("id came back as %s: a large id was not preserved", id)
	}
	if !isError {
		t.Error("the refusal did not reach the client as an error")
	}
	if !strings.Contains(text, "policy") {
		t.Errorf("the client was told %q, want the policy refusal", text)
	}

	// What the connector actually saw. Byte-identical for the allowed call, and
	// no trace at all of the refused one.
	got := s.received(t)
	if !strings.Contains(got, allowed) {
		t.Errorf("the connector did not receive the allowed call verbatim:\n%s", got)
	}
	if strings.Contains(got, "dangerous_tool") {
		t.Errorf("the refused call reached the connector:\n%s", got)
	}

	r.kill()

	// And the record agrees with what happened.
	out, err := s.run(t, "log")
	if err != nil {
		t.Fatalf("nim log: %v\n%s", err, out)
	}
	if !strings.Contains(out, "allow") || !strings.Contains(out, "deny") {
		t.Errorf("nim log does not show both decisions:\n%s", out)
	}
	if !strings.Contains(out, "denied") {
		t.Errorf("nim log does not report the refused call as denied:\n%s", out)
	}
	if strings.Contains(out, "(no response)") {
		t.Errorf("nim log still reports a refusal as an unanswered call:\n%s", out)
	}

	if out, err := s.run(t, "verify"); err != nil || !strings.Contains(out, "self-consistent") {
		t.Errorf("nim verify: %v\n%s", err, out)
	}
	if out, _ := s.run(t, "status"); strings.Contains(out, "gaps           1") {
		t.Errorf("a refused call was reported as a gap:\n%s", out)
	}
}

// With no daemon there is nothing to record a call against, so nothing goes
// through. This is the whole of fail-closed, checked against the connector.
func TestRealRelayWithNoDaemonReachesNothing(t *testing.T) {
	s := build(t, "")
	// No daemon started, and none may start: the relay spawns one when it can,
	// so the socket is pointed somewhere it cannot bind.
	s.env = append(s.env, "TMPDIR=/nonexistent-for-nim")
	if err := os.WriteFile(filepath.Join(s.home, "config.toml"),
		[]byte("[daemon]\nsocket = \"/nonexistent-for-nim/nim.sock\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := s.serve(t)
	r.send(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`)

	_, isError, text := decodeLine(t, r.next(t))
	if !isError {
		t.Error("a call was answered as a success with no daemon to decide it")
	}
	if !strings.Contains(text, "could not reach a decision") {
		t.Errorf("the client was told %q, want the could-not-decide refusal", text)
	}
	if got := s.received(t); strings.Contains(got, "tools/call") {
		t.Errorf("a call reached the connector with no daemon to record it:\n%s", got)
	}
	if !strings.Contains(r.errors(), "denied") {
		t.Errorf("the relay did not say on stderr that calls would be denied:\n%s", r.errors())
	}
}

// Anything that is not a tools/call still goes straight through, daemon or no
// daemon. Fail-closed applies to calls, not to the protocol.
func TestRealRelayStillPassesEverythingElse(t *testing.T) {
	s := build(t, "")
	s.daemon(t)
	r := s.serve(t)

	for _, frame := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
	} {
		r.send(t, frame)
		if id, isError, _ := decodeLine(t, r.next(t)); isError {
			t.Errorf("%s was refused (id %s)", frame, id)
		}
		if !strings.Contains(s.received(t), frame) {
			t.Errorf("the connector did not receive %s verbatim", frame)
		}
	}

	// A notification expects nothing back, and must not be held up either.
	r.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if !r.silent(300 * time.Millisecond) {
		t.Error("Nim answered a notification")
	}
	if !strings.Contains(s.received(t), "notifications/initialized") {
		t.Error("a notification did not reach the connector")
	}
}

// When the relay dies, a connector that reads its stdin dies with it. The kernel
// closes the pipe; nothing in Nim has to run, which is what makes it hold when
// the relay is killed outright.
//
// Measured on darwin/arm64 only. The same is expected on Linux and Windows and
// is not claimed until it is run there.
func TestAConnectorReadingStdinDiesWithTheRelay(t *testing.T) {
	s := build(t, "")
	s.daemon(t)
	r := s.serve(t)

	// Make sure the connector is up and reading before killing anything.
	r.send(t, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	r.next(t)

	r.cmd.Process.Kill()
	r.cmd.Wait()

	marker := s.log + ".exited"
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the connector was still running after the relay was killed")
}
