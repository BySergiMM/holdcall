package clientconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixture writes a JSON fixture into a fresh temp dir and returns its
// path, so each test gets a config file that only it can affect.
func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// nimPathFor gives every test its own nim path string, distinct from any
// real binary: BuildResult never runs it, only compares it, so it need not
// exist.
func nimPathFor(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "nim")
}

func mustBuildResult(t *testing.T, path, nimPath string) Result {
	t.Helper()
	res, err := BuildResult(File{Client: ClaudeCode, Path: path, Kind: KindFlat}, nimPath, ClaudeCode, false)
	if err != nil {
		t.Fatalf("BuildResult: %v", err)
	}
	return res
}

func entryByKey(t *testing.T, res Result, key string) EntryPlan {
	t.Helper()
	for _, g := range res.Groups {
		for _, e := range g.Entries {
			if e.Key == key {
				return e
			}
		}
	}
	t.Fatalf("no entry %q in result for %s", key, res.File.Path)
	return EntryPlan{}
}

// A plain stdio server gets its command replaced with the nim binary and its
// original command moved after "--", with a connector name matching its
// mcpServers key.
func TestInitWrapsAStdioServer(t *testing.T) {
	path := writeFixture(t, "claude.json", `{
		"mcpServers": {
			"github": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-github"]
			}
		}
	}`)
	nimPath := nimPathFor(t)
	res := mustBuildResult(t, path, nimPath)

	if !res.Changed {
		t.Fatal("expected the file to change")
	}
	e := entryByKey(t, res, "github")
	if e.Status != StatusWrapped {
		t.Fatalf("status = %v, want StatusWrapped", e.Status)
	}

	var rewritten struct {
		McpServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(res.Rewritten, &rewritten); err != nil {
		t.Fatalf("rewritten file is not valid JSON: %v\n%s", err, res.Rewritten)
	}
	got := rewritten.McpServers["github"]
	if got.Command != nimPath {
		t.Errorf("command = %q, want %q", got.Command, nimPath)
	}
	want := []string{"serve", "--connector", "github", "--client", ClaudeCode, "--", "npx", "-y", "@modelcontextprotocol/server-github"}
	if len(got.Args) != len(want) {
		t.Fatalf("args = %v, want %v", got.Args, want)
	}
	for i := range want {
		if got.Args[i] != want[i] {
			t.Fatalf("args = %v, want %v", got.Args, want)
		}
	}
}

// An entry already routed through the running nim binary, at the same path,
// must not be wrapped a second time -- doing so would nest "serve ... --
// nim serve ... -- real-command" and never spawn the real server.
func TestInitLeavesAnEntryAlreadyThroughNimAlone(t *testing.T) {
	nimPath := nimPathFor(t)
	path := writeFixture(t, "claude.json", `{
		"mcpServers": {
			"github": {
				"command": "`+jsonEscape(nimPath)+`",
				"args": ["serve", "--connector", "github", "--client", "claude-code", "--", "npx", "-y", "server-github"]
			}
		}
	}`)
	res := mustBuildResult(t, path, nimPath)

	if res.Changed {
		t.Fatalf("expected no change, got Rewritten:\n%s", res.Rewritten)
	}
	e := entryByKey(t, res, "github")
	if e.Status != StatusAlreadyNim {
		t.Fatalf("status = %v, want StatusAlreadyNim", e.Status)
	}
	if e.Before != e.After {
		t.Error("Before and After must be identical for an unchanged entry")
	}
}

// A second nim init run over an already-wrapped file must report no changes
// at all -- the property that makes it safe to run nim init repeatedly.
func TestInitRunTwiceIsIdempotent(t *testing.T) {
	nimPath := nimPathFor(t)
	path := writeFixture(t, "claude.json", `{
		"mcpServers": {
			"github": {"command": "npx", "args": ["-y", "server-github"]}
		}
	}`)

	first := mustBuildResult(t, path, nimPath)
	if !first.Changed {
		t.Fatal("expected the first run to change the file")
	}
	if err := os.WriteFile(path, first.Rewritten, 0o644); err != nil {
		t.Fatal(err)
	}

	second := mustBuildResult(t, path, nimPath)
	if second.Changed {
		t.Fatalf("second run changed the file again:\n%s", second.Rewritten)
	}
	if entryByKey(t, second, "github").Status != StatusAlreadyNim {
		t.Fatalf("status = %v, want StatusAlreadyNim", entryByKey(t, second, "github").Status)
	}
}

// An HTTP/SSE server (a "url" entry, no "command") is not something nim can
// sit in front of as a stdio relay, so it must be reported and left alone.
func TestInitLeavesHTTPServersAlone(t *testing.T) {
	path := writeFixture(t, "claude.json", `{
		"mcpServers": {
			"remote": {"url": "https://example.com/mcp"}
		}
	}`)
	res := mustBuildResult(t, path, nimPathFor(t))

	if res.Changed {
		t.Fatalf("expected no change, got:\n%s", res.Rewritten)
	}
	e := entryByKey(t, res, "remote")
	if e.Status != StatusHTTPSkipped {
		t.Fatalf("status = %v, want StatusHTTPSkipped", e.Status)
	}
	if !strings.Contains(string(res.Rewritten), "https://example.com/mcp") {
		t.Error("the url entry's content must survive untouched")
	}
}

// Keys nim init never looks at -- both alongside mcpServers and inside one
// server's entry -- must come back exactly as they went in.
func TestInitPreservesUnrelatedKeys(t *testing.T) {
	path := writeFixture(t, "claude.json", `{
		"theme": "dark",
		"mcpServers": {
			"github": {
				"command": "npx",
				"args": ["-y", "server-github"],
				"disabled": false,
				"customField": {"nested": [1, 2, 3]}
			}
		},
		"trailingKey": 42
	}`)
	res := mustBuildResult(t, path, nimPathFor(t))

	var rewritten map[string]json.RawMessage
	if err := json.Unmarshal(res.Rewritten, &rewritten); err != nil {
		t.Fatalf("rewritten file is not valid JSON: %v", err)
	}
	if string(rewritten["theme"]) != `"dark"` {
		t.Errorf(`theme = %s, want "dark"`, rewritten["theme"])
	}
	if string(rewritten["trailingKey"]) != "42" {
		t.Errorf("trailingKey = %s, want 42", rewritten["trailingKey"])
	}

	var servers map[string]json.RawMessage
	if err := json.Unmarshal(rewritten["mcpServers"], &servers); err != nil {
		t.Fatal(err)
	}
	var github map[string]json.RawMessage
	if err := json.Unmarshal(servers["github"], &github); err != nil {
		t.Fatal(err)
	}
	if string(github["disabled"]) != "false" {
		t.Errorf("disabled = %s, want false", github["disabled"])
	}
	if !strings.Contains(string(github["customField"]), `"nested"`) {
		t.Errorf("customField lost its content: %s", github["customField"])
	}
}

// The order of top-level keys is not something nim init has any business
// changing; Go's own map-based JSON decoding would sort them alphabetically
// on the way back out if this were not handled deliberately.
func TestInitPreservesKeyOrder(t *testing.T) {
	path := writeFixture(t, "claude.json", `{
		"zKey": 1,
		"aKey": 2,
		"mcpServers": {"github": {"command": "npx", "args": ["-y", "x"]}},
		"mKey": 3
	}`)
	res := mustBuildResult(t, path, nimPathFor(t))

	top, err := decodeOrderedObject(res.Rewritten)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"zKey", "aKey", "mcpServers", "mKey"}
	if len(top.keys) != len(want) {
		t.Fatalf("keys = %v, want %v", top.keys, want)
	}
	for i, k := range want {
		if top.keys[i] != k {
			t.Fatalf("keys = %v, want %v", top.keys, want)
		}
	}
}

// A credential must never be moved, dropped, or otherwise disturbed: env is
// copied verbatim, not merged or reconstructed field by field.
func TestInitPreservesEnvVerbatim(t *testing.T) {
	path := writeFixture(t, "claude.json", `{
		"mcpServers": {
			"github": {
				"command": "npx",
				"args": ["-y", "server-github"],
				"env": {"GITHUB_TOKEN": "ghp_secret", "OTHER": "value"}
			}
		}
	}`)
	res := mustBuildResult(t, path, nimPathFor(t))

	var rewritten struct {
		McpServers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(res.Rewritten, &rewritten); err != nil {
		t.Fatal(err)
	}
	env := rewritten.McpServers["github"].Env
	if env["GITHUB_TOKEN"] != "ghp_secret" || env["OTHER"] != "value" {
		t.Errorf("env = %v, want both original values preserved", env)
	}
}

// A server key nim init cannot use as a connector name -- the same rule the
// daemon enforces when a connector is registered -- must be reported and
// left alone rather than silently mangled into something that fits.
func TestInitSkipsAConnectorNameThatFailsValidation(t *testing.T) {
	path := writeFixture(t, "claude.json", `{
		"mcpServers": {
			"bad name!": {"command": "npx", "args": ["-y", "x"]}
		}
	}`)
	res := mustBuildResult(t, path, nimPathFor(t))

	if res.Changed {
		t.Fatalf("expected no change, got:\n%s", res.Rewritten)
	}
	e := entryByKey(t, res, "bad name!")
	if e.Status != StatusInvalidName {
		t.Fatalf("status = %v, want StatusInvalidName", e.Status)
	}
}

// A file that is not valid JSON at all must be refused outright: nothing
// about it can be trusted enough to rewrite part of it.
func TestInitRefusesAFileThatIsNotValidJSON(t *testing.T) {
	path := writeFixture(t, "claude.json", `{ this is not json `)
	_, err := BuildResult(File{Client: ClaudeCode, Path: path, Kind: KindFlat}, nimPathFor(t), ClaudeCode, false)
	if err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

// A file that parses as JSON but is not an object (an array, here) is
// refused the same way: there is no mcpServers to find inside it.
func TestInitRefusesAFileThatIsNotAJSONObject(t *testing.T) {
	path := writeFixture(t, "claude.json", `["not", "an", "object"]`)
	_, err := BuildResult(File{Client: ClaudeCode, Path: path, Kind: KindFlat}, nimPathFor(t), ClaudeCode, false)
	if err == nil {
		t.Fatal("expected an error for a non-object top level")
	}
}

// A file that is simply not there yet (Cursor never installed, say) is not
// an error: nim init has nothing to do and says so.
func TestInitReportsAMissingFileAsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	res, err := BuildResult(File{Client: Cursor, Path: path, Kind: KindFlat}, nimPathFor(t), Cursor, false)
	if err != nil {
		t.Fatalf("a missing file must not be an error: %v", err)
	}
	if res.Found {
		t.Fatal("Found = true for a file that does not exist")
	}
	if res.Changed {
		t.Fatal("Changed = true for a file that does not exist")
	}
}

// Claude Code keeps a second copy of mcpServers under each project; nim init
// must reach those too, using the project path as the connector's --client
// label context and leaving every other project key untouched.
func TestInitHandlesClaudeCodesPerProjectMcpServers(t *testing.T) {
	path := writeFixture(t, "claude.json", `{
		"mcpServers": {},
		"projects": {
			"/Users/me/code": {
				"allowedTools": ["Bash"],
				"mcpServers": {
					"local": {"command": "python3", "args": ["server.py"]}
				}
			},
			"/Users/me/other": {
				"mcpServers": {}
			}
		}
	}`)
	res, err := BuildResult(File{Client: ClaudeCode, Path: path, Kind: KindClaudeCode}, nimPathFor(t), ClaudeCode, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("expected a change inside the per-project mcpServers")
	}

	var found bool
	for _, g := range res.Groups {
		if g.Label != "projects./Users/me/code" {
			continue
		}
		for _, e := range g.Entries {
			if e.Key == "local" && e.Status == StatusWrapped {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("did not find a wrapped \"local\" entry under projects./Users/me/code, groups: %+v", res.Groups)
	}

	var rewritten struct {
		Projects map[string]struct {
			AllowedTools []string `json:"allowedTools"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(res.Rewritten, &rewritten); err != nil {
		t.Fatal(err)
	}
	if len(rewritten.Projects["/Users/me/code"].AllowedTools) != 1 {
		t.Errorf("allowedTools was not preserved: %+v", rewritten.Projects["/Users/me/code"])
	}
	if _, ok := rewritten.Projects["/Users/me/other"]; !ok {
		t.Error("a project with nothing to wrap must still survive in the output")
	}
}

// An entry through a nim binary at a path other than the one running now is
// reported, not silently repointed: --repoint has to be given explicitly.
func TestInitLeavesAStalePathAloneWithoutRepoint(t *testing.T) {
	otherNim := filepath.Join(t.TempDir(), "nim")
	runningNim := nimPathFor(t)
	path := writeFixture(t, "claude.json", `{
		"mcpServers": {
			"github": {
				"command": "`+jsonEscape(otherNim)+`",
				"args": ["serve", "--connector", "github", "--client", "claude-code", "--", "npx", "server"]
			}
		}
	}`)
	res := mustBuildResultWithRepoint(t, path, runningNim, false)

	if res.Changed {
		t.Fatalf("expected no change without --repoint, got:\n%s", res.Rewritten)
	}
	e := entryByKey(t, res, "github")
	if e.Status != StatusStalePath {
		t.Fatalf("status = %v, want StatusStalePath", e.Status)
	}
}

// With --repoint, a stale entry's command is updated to the running nim's
// path and nothing else about it changes.
func TestInitRepointsAnEntryWrappedByADifferentNimPath(t *testing.T) {
	otherNim := filepath.Join(t.TempDir(), "nim")
	runningNim := nimPathFor(t)
	path := writeFixture(t, "claude.json", `{
		"mcpServers": {
			"github": {
				"command": "`+jsonEscape(otherNim)+`",
				"args": ["serve", "--connector", "github", "--client", "claude-code", "--", "npx", "server"]
			}
		}
	}`)
	res := mustBuildResultWithRepoint(t, path, runningNim, true)

	if !res.Changed {
		t.Fatal("expected --repoint to change the file")
	}
	e := entryByKey(t, res, "github")
	if e.Status != StatusRepointed {
		t.Fatalf("status = %v, want StatusRepointed", e.Status)
	}

	var rewritten struct {
		McpServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(res.Rewritten, &rewritten); err != nil {
		t.Fatal(err)
	}
	got := rewritten.McpServers["github"]
	if got.Command != runningNim {
		t.Errorf("command = %q, want %q", got.Command, runningNim)
	}
	want := []string{"serve", "--connector", "github", "--client", "claude-code", "--", "npx", "server"}
	if len(got.Args) != len(want) {
		t.Fatalf("args changed during repoint: %v, want %v", got.Args, want)
	}
	for i := range want {
		if got.Args[i] != want[i] {
			t.Fatalf("args changed during repoint: %v, want %v", got.Args, want)
		}
	}
}

func mustBuildResultWithRepoint(t *testing.T, path, nimPath string, repoint bool) Result {
	t.Helper()
	res, err := BuildResult(File{Client: ClaudeCode, Path: path, Kind: KindFlat}, nimPath, ClaudeCode, repoint)
	if err != nil {
		t.Fatalf("BuildResult: %v", err)
	}
	return res
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1]) // strip the surrounding quotes json.Marshal adds
}

// The before/after a dry run prints must never show an env value: that block
// is where a client config keeps its API tokens, and a dry run is exactly the
// command an operator runs first, pipes into a file, or shares on a screen.
// The keys stay visible so the operator can see the block survives.
func TestInitNeverShowsAnEnvValue(t *testing.T) {
	path := writeFixture(t, "mcp.json", `{
	"mcpServers": {
		"github": {"command": "npx", "args": ["-y", "server-github"],
		           "env": {"GITHUB_TOKEN": "ghp_SUPERSECRET0000000000000", "OTHER": 42}}
	}
}`)
	res := mustBuildResult(t, path, nimPathFor(t))
	e := entryByKey(t, res, "github")
	for _, text := range []string{e.Before, e.After} {
		if strings.Contains(text, "ghp_SUPERSECRET") || strings.Contains(text, "42") {
			t.Fatalf("an env value was printed:\n%s", text)
		}
		if !strings.Contains(text, "GITHUB_TOKEN") || !strings.Contains(text, "(value not shown)") {
			t.Fatalf("the env keys were not shown:\n%s", text)
		}
	}
	// What is written keeps the real values, untouched.
	if !strings.Contains(string(res.Rewritten), "ghp_SUPERSECRET0000000000000") {
		t.Fatal("the rewritten file lost the env value")
	}
}
