package daemon

import (
	"fmt"
	"regexp"
)

// Size limits for the request/response protocol. These bound Request-kind
// messages specifically (see handle's requestKinds branch); Event messages
// are deliberately left uncapped here, since a large tool-call result is a
// legitimate M1 case (the relay-rig tests a 512 KiB payload) and this
// protocol family has nothing to do with that one.
const (
	// MaxRequestBytes bounds the whole raw line for a Request-kind message,
	// checked before the second, fuller json.Unmarshal into the Request
	// struct -- the cheap peek at just "kind" still has to happen first,
	// since that is how a Request is told apart from an Event at all, but
	// this avoids the more expensive full parse (and, for connector.set,
	// building an ID by handing the secret string to the OS credential
	// store) for anything absurdly oversized.
	MaxRequestBytes = 64 * 1024
	// MaxTargetLen and MaxEnvKeyLen bound identifiers, not payloads; both
	// are generous for any real connector or environment variable name.
	MaxTargetLen = 128
	MaxEnvKeyLen = 128
	// MaxSecretLen is generous for any real API token, certificate, or key
	// material Nim is likely to inject, without allowing unbounded growth.
	MaxSecretLen = 32 * 1024
	// MaxCommandArgs and MaxCommandArgLen bound a connector's registered
	// argv. Both are far past any real MCP server invocation
	// (`npx -y @modelcontextprotocol/server-github` is three) while keeping
	// what gets stored in nim_connectors, and re-read on every spawn,
	// bounded.
	MaxCommandArgs   = 64
	MaxCommandArgLen = 4096
)

// targetPattern is a strict allowlist, not a blacklist: it is easier to
// prove a pattern that only accepts a safe alphabet excludes ".", "..", "/",
// "\", and whitespace than to enumerate everything a blacklist has to catch.
// Anything this does not match is rejected outright -- see validateTarget.
var targetPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_-]{0,126}[A-Za-z0-9])?$`)

// validateTarget is the single place a connector target name is accepted or
// rejected, applied before it reaches any backend (SQLite or a credential
// store). A target is the one piece of Request data that ends up inside a
// filesystem path on Windows (store_windows.go's path()), so this is not
// optional hardening: an unvalidated target there is a path traversal.
//
// The allowlist requires starting and ending with an alphanumeric character
// and permits '-'/'_' in between, which structurally cannot produce ".",
// "..", "/", "\", leading/trailing whitespace, or an empty string -- there
// is nothing to silently strip or escape, so a target that does not match
// is rejected with a clear error rather than sanitized.
func validateTarget(target string) error {
	if len(target) > MaxTargetLen {
		return fmt.Errorf("target is %d characters, over the %d limit", len(target), MaxTargetLen)
	}
	if !targetPattern.MatchString(target) {
		return fmt.Errorf("target %q is invalid: must be 1-127 characters, start and end with a letter or digit, and contain only letters, digits, '-' or '_'", target)
	}
	return nil
}

func validateEnvKey(key string) error {
	if key == "" {
		return fmt.Errorf("env key must not be empty")
	}
	if len(key) > MaxEnvKeyLen {
		return fmt.Errorf("env key is %d characters, over the %d limit", len(key), MaxEnvKeyLen)
	}
	return nil
}

// validateCommand checks a connector's registered argv.
//
// A connector must have one: it is what the credential is authorized to be
// injected into, and a connector without it is a stored secret with no
// statement about who may receive it. Registering one without a command is
// refused here rather than accepted and refused later at spawn time, so the
// failure lands on the person configuring it, not on the agent using it.
func validateCommand(command []string) error {
	if len(command) == 0 {
		return fmt.Errorf(
			"a connector needs the command it belongs to, so Nim knows what its credential may be injected into: " +
				"nim connector set <target> --env KEY -- <command> [args...]")
	}
	if len(command) > MaxCommandArgs {
		return fmt.Errorf("command has %d arguments, over the %d limit", len(command), MaxCommandArgs)
	}
	if command[0] == "" {
		return fmt.Errorf("the command's program name must not be empty")
	}
	for i, arg := range command {
		if len(arg) > MaxCommandArgLen {
			return fmt.Errorf("command argument %d is %d bytes, over the %d limit", i, len(arg), MaxCommandArgLen)
		}
	}
	return nil
}

func validateSecret(secret string) error {
	if secret == "" {
		return fmt.Errorf("secret must not be empty")
	}
	if len(secret) > MaxSecretLen {
		return fmt.Errorf("secret is %d bytes, over the %d limit", len(secret), MaxSecretLen)
	}
	return nil
}
