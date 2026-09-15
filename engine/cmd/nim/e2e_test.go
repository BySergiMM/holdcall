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
	"syscall"
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
//
// It used to take a deny list to write into config.toml. Policy no longer
// lives there -- a config.toml carrying it is refused, see
// TestAStaleDenyListInConfigIsRefused -- so a test that wants a rule asks the
// running daemon for one with `nim policy deny`, as an operator would.
func build(t *testing.T) *stack {
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
	if err := os.WriteFile(filepath.Join(s.home, "config.toml"), []byte("[daemon]\n"), 0o600); err != nil {
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
	// The connector inherits the relay's stderr. Wait waits for that pipe to
	// close, so a connector that outlived a killed relay would hang the test
	// forever; this bounds it.
	cmd.WaitDelay = 5 * time.Second

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

// deny adds a rule through the real CLI and checks the CLI said so.
func (s *stack) deny(t *testing.T, args ...string) {
	t.Helper()
	out, err := s.run(t, append([]string{"policy", "deny"}, args...)...)
	if err != nil || !strings.Contains(out, "recorded in the journal") {
		t.Fatalf("nim policy deny %v: %v\n%s", args, err, out)
	}
}

// allow is deny's counterpart, added for M4.5.
func (s *stack) allow(t *testing.T, args ...string) {
	t.Helper()
	out, err := s.run(t, append([]string{"policy", "allow"}, args...)...)
	if err != nil || !strings.Contains(out, "recorded in the journal") {
		t.Fatalf("nim policy allow %v: %v\n%s", args, err, out)
	}
}

// policyDefault sets deny or allow across every tool through the real CLI:
// nim policy default deny|allow [--agent] [--connector].
func (s *stack) policyDefault(t *testing.T, effect string, args ...string) {
	t.Helper()
	out, err := s.run(t, append([]string{"policy", "default", effect}, args...)...)
	if err != nil || !strings.Contains(out, "recorded in the journal") {
		t.Fatalf("nim policy default %s %v: %v\n%s", effect, args, err, out)
	}
}

// One allowed call and one refused one, through the whole stack.
func TestRealRelayForwardsOneCallAndRefusesTheOther(t *testing.T) {
	s := build(t)
	s.daemon(t)
	s.deny(t, "dangerous_tool")
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
	s := build(t)
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
	s := build(t)
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
	s := build(t)
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

// launcherSource is a client program small enough to compile in a test: it
// runs the command it is given as a child and waits for it, which is what an
// MCP client does with a relay. Two copies of it are two different files,
// and therefore two different agents.
const launcherSource = `package main

import (
	"os"
	"os/exec"
)

func main() {
	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
}
`

// serveVia is serve with the relay spawned by a launcher, so that the relay's
// parent -- what the daemon derives the agent from -- is a program of the
// test's choosing rather than the test binary.
func (s *stack) serveVia(t *testing.T, launcher string) *relay {
	t.Helper()
	cmd := exec.Command(launcher, s.nim, "serve", "--connector", "rig", "--client", "e2e", "--", s.connector)
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
	// The relay and its connector inherit the launcher's stderr pipe, and Wait
	// waits for it to close. Killing the launcher alone leaves both alive, so
	// without a bound Wait would never return.
	cmd.WaitDelay = 5 * time.Second
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

// The acceptance test docs/milestones.md sets for M4, run with real
// processes: two agents against one connector receive different verdicts,
// and the record distinguishes them.
//
// Two client programs are built -- the same source, two files, two inodes --
// and enrolled under two names. A rule refuses one tool for the first. Each
// then spawns a relay, exactly as an MCP client would, and sends the same
// call. Nothing on the wire says which agent is which; the daemon works it
// out from the process that spawned the relay.
func TestTwoRealAgentsAgainstOneConnectorReceiveDifferentVerdicts(t *testing.T) {
	s := build(t)
	dir := filepath.Dir(s.nim)
	src := filepath.Join(dir, "launcher.go")
	if err := os.WriteFile(src, []byte(launcherSource), 0o600); err != nil {
		t.Fatal(err)
	}
	alpha, beta := filepath.Join(dir, "alpha"), filepath.Join(dir, "beta")
	for _, bin := range []string{alpha, beta} {
		if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", bin, err, out)
		}
	}
	s.daemon(t)
	for name, bin := range map[string]string{"alpha": alpha, "beta": beta} {
		if out, err := s.run(t, "agent", "add", name, bin); err != nil || !strings.Contains(out, "enrolled") {
			t.Fatalf("enrolling %s: %v\n%s", name, err, out)
		}
	}
	s.deny(t, "dangerous_tool", "--agent", "alpha")

	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dangerous_tool","arguments":{}}}`
	verdict := func(launcher string) bool {
		t.Helper()
		r := s.serveVia(t, launcher)
		r.send(t, call)
		_, isError, _ := decodeLine(t, r.next(t))
		// Ended the way a client ends a session: stdin closes, the relay
		// stops its connector and exits, and the launcher returns.
		r.in.Close()
		r.cmd.Wait()
		return !isError
	}
	if verdict(alpha) {
		t.Error("alpha's call was allowed; the rule names alpha")
	}
	if !verdict(beta) {
		t.Error("beta's call was refused; no rule names beta")
	}

	// The record says which was which -- not from a label the relay chose
	// (both said --client e2e) but from what the daemon derived.
	out, err := s.run(t, "log", "--json")
	if err != nil {
		t.Fatalf("nim log --json: %v\n%s", err, out)
	}
	agents := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var e struct {
			Kind  string `json:"kind"`
			Agent string `json:"agent"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && e.Kind == "session.start" && e.Agent != "" {
			agents[e.Agent] = true
		}
	}
	if !agents["alpha"] || !agents["beta"] {
		t.Errorf("the journal does not distinguish the two agents; saw %v", agents)
	}
	if out, _ := s.run(t, "policy", "list"); !strings.Contains(out, "alpha") {
		t.Errorf("nim policy list does not show the rule:\n%s", out)
	}
}

// D-002 for the allow model (docs/decisions/0003's last section), run end to
// end the way TestTwoRealAgentsAgainstOneConnectorReceiveDifferentVerdicts
// runs M4's: a default deny closes every tool for every session, and an
// agent-scoped allow is the enrolment that reopens one tool for one enrolled
// agent -- "only alpha may read_file", the shape docs/decisions/0002 said
// needed a precedence and a new answer. beta is built but never enrolled, so
// its call meets only the default: an unknown agent is denied, not merely
// unprivileged the way M4's deny-only model left it.
func TestADefaultDenyClosesEverythingAndAnAgentScopedAllowReopensOneToolForOneAgent(t *testing.T) {
	s := build(t)
	dir := filepath.Dir(s.nim)
	src := filepath.Join(dir, "launcher.go")
	if err := os.WriteFile(src, []byte(launcherSource), 0o600); err != nil {
		t.Fatal(err)
	}
	alpha, beta := filepath.Join(dir, "alpha"), filepath.Join(dir, "beta")
	for _, bin := range []string{alpha, beta} {
		if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", bin, err, out)
		}
	}
	s.daemon(t)
	if out, err := s.run(t, "agent", "add", "alpha", alpha); err != nil || !strings.Contains(out, "enrolled") {
		t.Fatalf("enrolling alpha: %v\n%s", err, out)
	}
	// beta is deliberately never enrolled.

	s.policyDefault(t, "deny")
	s.allow(t, "read_file", "--agent", "alpha")

	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{}}}`
	verdict := func(launcher string) bool {
		t.Helper()
		r := s.serveVia(t, launcher)
		r.send(t, call)
		_, isError, _ := decodeLine(t, r.next(t))
		r.in.Close()
		r.cmd.Wait()
		return !isError
	}
	if !verdict(alpha) {
		t.Error("alpha's call was refused; alpha has an allow more specific than the default deny")
	}
	if verdict(beta) {
		t.Error("beta's call was allowed; beta is unenrolled and meets only the default deny")
	}

	if out, err := s.run(t, "policy", "explain", "read_file", "--agent", "alpha"); err != nil ||
		!strings.Contains(out, "ALLOW") {
		t.Errorf("nim policy explain read_file --agent alpha: %v\n%s", err, out)
	}
	if out, err := s.run(t, "policy", "explain", "read_file"); err != nil || !strings.Contains(out, "DENY") {
		t.Errorf("nim policy explain read_file (no agent): %v\n%s", err, out)
	}

	if out, _ := s.run(t, "policy", "list"); !strings.Contains(out, "alpha") || !strings.Contains(out, "allow") {
		t.Errorf("nim policy list does not show the allow rule:\n%s", out)
	}

	// Removing the default leaves the M4 baseline for beta: still nothing
	// grants it read_file, but nothing else refuses it either.
	if out, err := s.run(t, "policy", "remove", "--default"); err != nil || !strings.Contains(out, "recorded in the journal") {
		t.Fatalf("nim policy remove --default: %v\n%s", err, out)
	}
	if !verdict(beta) {
		t.Error("beta was still refused after the default was removed; the M4 baseline is allow")
	}
}

// A config.toml that still carries the deny list is refused, with the way
// forward in the message, on every command -- not read past.
func TestAStaleDenyListInConfigIsRefused(t *testing.T) {
	s := build(t)
	if err := os.WriteFile(filepath.Join(s.home, "config.toml"),
		[]byte("[daemon]\n\n[policy]\ndeny = [\"dangerous_tool\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"status"}, {"daemon"}, {"policy", "list"}} {
		out, err := s.run(t, args...)
		if err == nil {
			t.Errorf("nim %v ran with a stale [policy] section:\n%s", args, out)
		}
		if !strings.Contains(out, "nim policy deny") {
			t.Errorf("nim %v did not say where policy went:\n%s", args, out)
		}
	}
}

// stubbornSource is a connector that never reads its stdin, so the kernel
// closing that pipe tells it nothing. Only the relay stopping it does.
const stubbornSource = `package main

import (
	"os"
	"time"
)

func main() {
	os.WriteFile(os.Getenv("CONNECTOR_LOG")+".started", []byte("yes"), 0o600)
	time.Sleep(time.Hour)
}
`

// A connector that ignores its stdin used to outlive a relay that was asked
// to stop: the signal handler flushed the journal and exited, and the child
// carried on with whatever credential it had been given while the record
// said the session was over. The relay now stops its child before it goes.
//
// Asked to stop, not killed outright: a relay that receives SIGKILL runs
// nothing, and then only a connector that reads its stdin notices. That is a
// limit of where the relay sits, stated in docs/security.md, and this test
// does not claim otherwise.
func TestAConnectorIgnoringStdinIsStoppedWithTheRelay(t *testing.T) {
	s := build(t)
	src := filepath.Join(filepath.Dir(s.nim), "stubborn.go")
	if err := os.WriteFile(src, []byte(stubbornSource), 0o600); err != nil {
		t.Fatal(err)
	}
	stubborn := filepath.Join(filepath.Dir(s.nim), "stubborn")
	if out, err := exec.Command("go", "build", "-o", stubborn, src).CombinedOutput(); err != nil {
		t.Fatalf("building the connector: %v\n%s", err, out)
	}
	s.daemon(t)

	cmd := exec.Command(s.nim, "serve", "--connector", "rig", "--", stubborn)
	cmd.Env = s.env
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	started := s.log + ".started"
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatal("the connector never started")
	}
	// The connector's pid, from the relay's point of view, is not exposed;
	// what is observable is whether a process running that binary remains.
	running := func() bool {
		out, _ := exec.Command("pgrep", "-f", stubborn).Output()
		return strings.TrimSpace(string(out)) != ""
	}
	if !running() {
		t.Fatal("pgrep cannot see the connector; the test cannot observe what it needs to")
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()

	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !running() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the connector outlived the relay it was spawned by")
}
