package main

import (
	"flag"
	"io"
	"slices"
	"testing"
)

// server is the stand-in downstream every case below binds its connector to.
// A connector must name one: see TestParseConnectorSetArgsRequiresACommand.
var server = []string{"npx", "-y", "@modelcontextprotocol/server-github"}

func withServer(args ...string) []string {
	return append(append([]string{}, args...), append([]string{"--"}, server...)...)
}

// The specified invocation is "nim connector set <target> --env KEY": target
// before the flag. The standard flag package stops parsing flags at the
// first positional argument, which would silently swallow --env as a second
// positional and leave the env var empty -- caught by hand: the first
// implementation of this parser used flag.FlagSet and failed exactly this
// case.
func TestParseConnectorSetArgsAcceptsTargetBeforeTheFlag(t *testing.T) {
	target, key, command, err := parseConnectorSetArgs(withServer("github", "--env", "GITHUB_TOKEN"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != "github" || key != "GITHUB_TOKEN" {
		t.Fatalf("got (%q, %q), want (github, GITHUB_TOKEN)", target, key)
	}
	if !slices.Equal(command, server) {
		t.Fatalf("got command %v, want %v", command, server)
	}
}

func TestParseConnectorSetArgsAcceptsTheFlagBeforeTarget(t *testing.T) {
	target, key, _, err := parseConnectorSetArgs(withServer("--env", "GITHUB_TOKEN", "github"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != "github" || key != "GITHUB_TOKEN" {
		t.Fatalf("got (%q, %q), want (github, GITHUB_TOKEN)", target, key)
	}
}

func TestParseConnectorSetArgsAcceptsEqualsForm(t *testing.T) {
	target, key, _, err := parseConnectorSetArgs(withServer("github", "--env=GITHUB_TOKEN"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != "github" || key != "GITHUB_TOKEN" {
		t.Fatalf("got (%q, %q), want (github, GITHUB_TOKEN)", target, key)
	}
}

func TestParseConnectorSetArgsRequiresATarget(t *testing.T) {
	if _, _, _, err := parseConnectorSetArgs(withServer("--env", "K")); err == nil {
		t.Fatal("expected an error with no target given")
	}
}

func TestParseConnectorSetArgsRequiresTheEnvFlag(t *testing.T) {
	if _, _, _, err := parseConnectorSetArgs(withServer("github")); err == nil {
		t.Fatal("expected an error with no --env given")
	}
}

// A connector with no command is a stored secret with no statement about
// what may receive it, which is exactly what let any caller name a target
// alongside a command of its own and be handed the credential. Refusing at
// registration puts the failure on the person configuring it rather than on
// the agent that later hits it.
func TestParseConnectorSetArgsRequiresACommand(t *testing.T) {
	for _, args := range [][]string{
		{"github", "--env", "GITHUB_TOKEN"},
		{"github", "--env", "GITHUB_TOKEN", "--"},
	} {
		if _, _, _, err := parseConnectorSetArgs(args); err == nil {
			t.Errorf("expected an error for args %v (no command after --)", args)
		}
	}
}

// Everything after -- belongs to the downstream server, including things
// that look like Nim's own flags. Without this a server taking --env would
// have its arguments silently stolen by this parser.
func TestParseConnectorSetArgsTakesEverythingAfterTheSeparatorVerbatim(t *testing.T) {
	want := []string{"my-server", "--env", "SOMETHING", "--flag=x", "positional"}
	_, _, command, err := parseConnectorSetArgs(
		append([]string{"github", "--env", "GITHUB_TOKEN", "--"}, want...))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(command, want) {
		t.Fatalf("got command %v, want %v", command, want)
	}
}

// The whole point of this parser: no code path may accept a secret value
// baked into argv. A caller reflexively typing the old "--env KEY=value"
// form must get a clear rejection, not a silent misparse (e.g. treating
// "value" as part of the key).
func TestParseConnectorSetArgsRejectsAValueInTheEnvFlag(t *testing.T) {
	for _, args := range [][]string{
		withServer("github", "--env", "GITHUB_TOKEN=ghp_secret"),
		withServer("github", "--env=GITHUB_TOKEN=ghp_secret"),
	} {
		if _, _, _, err := parseConnectorSetArgs(args); err == nil {
			t.Errorf("expected an error for args %v (old KEY=value form)", args)
		}
	}
}

// runServe used to reject a missing command before the daemon was ever asked.
// That broke the documented way to run a connector that holds a credential --
// `nim serve --connector github`, with no `--` at all -- because the command
// it should run lives with the connector, and only shim.Run knows whether the
// daemon supplied one. Caught after the merge, by running the form the README
// tells people to use.
//
// This asserts the parsing contract rather than spawning anything: a serve
// invocation with no trailing command must be accepted here and left for
// shim.Run to resolve.
func TestServeAcceptsNoCommandSoAConnectorCanSupplyIt(t *testing.T) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	connector := fs.String("connector", "", "")
	_ = fs.String("client", "", "")
	if err := fs.Parse([]string{"--connector", "github"}); err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if *connector != "github" {
		t.Fatalf("connector = %q, want github", *connector)
	}
	if got := fs.Args(); len(got) != 0 {
		t.Fatalf("expected no trailing command, got %v", got)
	}
	// The guard that used to live here is gone; the only thing that may
	// refuse is shim.Run, once it knows what the daemon answered.
}

// An agent is identified by one executable file, so the invocation is two
// positionals rather than nim serve's `--` convention, which would suggest
// arguments that are never used.
func TestParseAgentAddArgs(t *testing.T) {
	abs := func(p string) (string, error) { return "/abs/" + p, nil }

	name, path, err := parseAgentAddArgs([]string{"claude-code", "Claude"}, abs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "claude-code" || path != "/abs/Claude" {
		t.Fatalf("got (%q, %q)", name, path)
	}

	for _, args := range [][]string{
		{},
		{"only-a-name"},
		{"a", "b", "c"},
		{"--exec", "/bin/sh"},
	} {
		if _, _, err := parseAgentAddArgs(args, abs); err == nil {
			t.Errorf("expected an error for %v", args)
		}
	}
}

// The path is made absolute against the operator's own directory, because that
// is the only place a relative path means what they think. The daemon runs
// somewhere else and refuses anything relative.
func TestAgentAddResolvesThePathBeforeSendingIt(t *testing.T) {
	called := ""
	abs := func(p string) (string, error) { called = p; return "/resolved" + p, nil }

	_, path, err := parseAgentAddArgs([]string{"claude-code", "./Claude"}, abs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != "./Claude" {
		t.Fatalf("the raw path was not resolved, got %q", called)
	}
	if path != "/resolved./Claude" {
		t.Fatalf("the resolved path was not used, got %q", path)
	}
}
