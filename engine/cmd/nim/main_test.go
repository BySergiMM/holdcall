package main

import "testing"

// The specified invocation is "nim connector set <target> --env KEY": target
// before the flag. The standard flag package stops parsing flags at the
// first positional argument, which would silently swallow --env as a second
// positional and leave the env var empty -- caught by hand: the first
// implementation of this parser used flag.FlagSet and failed exactly this
// case.
func TestParseConnectorSetArgsAcceptsTargetBeforeTheFlag(t *testing.T) {
	target, key, err := parseConnectorSetArgs([]string{"github", "--env", "GITHUB_TOKEN"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != "github" || key != "GITHUB_TOKEN" {
		t.Fatalf("got (%q, %q), want (github, GITHUB_TOKEN)", target, key)
	}
}

func TestParseConnectorSetArgsAcceptsTheFlagBeforeTarget(t *testing.T) {
	target, key, err := parseConnectorSetArgs([]string{"--env", "GITHUB_TOKEN", "github"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != "github" || key != "GITHUB_TOKEN" {
		t.Fatalf("got (%q, %q), want (github, GITHUB_TOKEN)", target, key)
	}
}

func TestParseConnectorSetArgsAcceptsEqualsForm(t *testing.T) {
	target, key, err := parseConnectorSetArgs([]string{"github", "--env=GITHUB_TOKEN"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != "github" || key != "GITHUB_TOKEN" {
		t.Fatalf("got (%q, %q), want (github, GITHUB_TOKEN)", target, key)
	}
}

func TestParseConnectorSetArgsRequiresATarget(t *testing.T) {
	if _, _, err := parseConnectorSetArgs([]string{"--env", "K"}); err == nil {
		t.Fatal("expected an error with no target given")
	}
}

func TestParseConnectorSetArgsRequiresTheEnvFlag(t *testing.T) {
	if _, _, err := parseConnectorSetArgs([]string{"github"}); err == nil {
		t.Fatal("expected an error with no --env given")
	}
}

// The whole point of this parser: no code path may accept a secret value
// baked into argv. A caller reflexively typing the old "--env KEY=value"
// form must get a clear rejection, not a silent misparse (e.g. treating
// "value" as part of the key).
func TestParseConnectorSetArgsRejectsAValueInTheEnvFlag(t *testing.T) {
	for _, args := range [][]string{
		{"github", "--env", "GITHUB_TOKEN=ghp_secret"},
		{"github", "--env=GITHUB_TOKEN=ghp_secret"},
	} {
		if _, _, err := parseConnectorSetArgs(args); err == nil {
			t.Errorf("expected an error for args %v (old KEY=value form)", args)
		}
	}
}
