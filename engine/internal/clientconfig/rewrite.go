package clientconfig

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BySergiMM/nim/engine/internal/daemon"
)

// EntryStatus classifies one mcpServers entry against what nim init does to
// it.
type EntryStatus string

const (
	StatusWrapped      EntryStatus = "wrapped"      // a stdio entry now routed through nim
	StatusAlreadyNim   EntryStatus = "already"      // already through this running nim, unchanged
	StatusRepointed    EntryStatus = "repointed"    // was through a different nim path; repointed
	StatusStalePath    EntryStatus = "stale_path"   // through a different nim path; left alone (no --repoint)
	StatusHTTPSkipped  EntryStatus = "http"         // has "url"; left alone
	StatusInvalidName  EntryStatus = "invalid_name" // the server key fails the connector-name rule
	StatusUnrecognized EntryStatus = "unrecognized" // neither "command" nor "url"
)

// EntryPlan is one mcpServers entry, before and after whatever nim init
// would do to it. Before and After are equal, pretty-printed JSON when the
// entry is left alone.
type EntryPlan struct {
	Key    string
	Status EntryStatus
	Detail string // a human-readable reason, mainly for skips
	Before string
	After  string
}

// Group is one mcpServers object inside a file.
type Group struct {
	// Label says where this object lives: "" for a top-level mcpServers,
	// "projects.<path>" for one of Claude Code's per-project ones.
	Label   string
	Entries []EntryPlan
}

// Result is what BuildResult found in one file, and what the file would look
// like after every change nim init would make.
//
// Rewritten is computed unconditionally, whether or not nim init was asked
// to --write: a dry run and a real run take the exact same path through this
// package, so they can never disagree about what would happen.
type Result struct {
	File   File
	Found  bool
	Groups []Group

	Original     []byte
	OriginalMode fs.FileMode
	Rewritten    []byte
	Changed      bool
}

// BuildResult reads file (if it exists), classifies every mcpServers entry
// it finds across every group the file's Kind says to look in, and computes
// the file's content after nim init's changes.
func BuildResult(file File, nimPath, clientLabel string, repoint bool) (Result, error) {
	res := Result{File: file}

	data, err := os.ReadFile(file.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil // Found stays false; there is nothing more to do
		}
		return res, fmt.Errorf("reading %s: %w", file.Path, err)
	}
	res.Found = true
	res.Original = data
	res.OriginalMode = fs.FileMode(0o644)
	if info, statErr := os.Stat(file.Path); statErr == nil {
		res.OriginalMode = info.Mode().Perm()
	}

	top, err := decodeOrderedObject(data)
	if err != nil {
		return res, fmt.Errorf("%s is not a JSON object: %w", file.Path, err)
	}

	changed := false
	if v, ok := top.values["mcpServers"]; ok {
		newV, group, ch, err := rewriteGroup(v, "", nimPath, clientLabel, repoint)
		if err != nil {
			return res, fmt.Errorf("%s: %w", file.Path, err)
		}
		top = top.set("mcpServers", newV)
		res.Groups = append(res.Groups, group)
		changed = changed || ch
	}

	if file.Kind == KindClaudeCode {
		if v, ok := top.values["projects"]; ok {
			if newProjects, groups, ch, err := rewriteProjects(v, nimPath, clientLabel, repoint); err == nil {
				top = top.set("projects", newProjects)
				res.Groups = append(res.Groups, groups...)
				changed = changed || ch
			}
			// A malformed or unexpected "projects" value is left exactly as
			// it was read (top.values["projects"] is untouched): this file
			// may simply not be Claude Code's, and guessing at its shape
			// would risk corrupting something nim init was never meant to
			// touch.
		}
	}

	newRaw, err := top.marshalIndent()
	if err != nil {
		return res, err
	}
	res.Rewritten = newRaw
	res.Changed = changed
	return res, nil
}

// rewriteProjects walks Claude Code's "projects.<path>.mcpServers" objects.
func rewriteProjects(raw json.RawMessage, nimPath, clientLabel string, repoint bool) (json.RawMessage, []Group, bool, error) {
	projects, err := decodeOrderedObject(raw)
	if err != nil {
		return raw, nil, false, err
	}

	var groups []Group
	changed := false
	for _, projKey := range projects.keys {
		projObj, err := decodeOrderedObject(projects.values[projKey])
		if err != nil {
			continue // not an object; not this file's shape, leave it alone
		}
		msRaw, ok := projObj.values["mcpServers"]
		if !ok {
			continue
		}
		newMS, group, ch, err := rewriteGroup(msRaw, "projects."+projKey, nimPath, clientLabel, repoint)
		if err != nil {
			return raw, nil, false, fmt.Errorf("project %s: %w", projKey, err)
		}
		groups = append(groups, group)
		if ch {
			projObj = projObj.set("mcpServers", newMS)
			newProjRaw, err := projObj.marshalIndent()
			if err != nil {
				return raw, nil, false, err
			}
			projects = projects.set(projKey, newProjRaw)
			changed = true
		}
	}

	newRaw, err := projects.marshalIndent()
	if err != nil {
		return raw, nil, false, err
	}
	return newRaw, groups, changed, nil
}

// rewriteGroup walks one mcpServers object and rewrites every entry that
// needs it.
func rewriteGroup(raw json.RawMessage, label, nimPath, clientLabel string, repoint bool) (json.RawMessage, Group, bool, error) {
	obj, err := decodeOrderedObject(raw)
	if err != nil {
		return raw, Group{Label: label}, false, fmt.Errorf("mcpServers is not a JSON object: %w", err)
	}

	group := Group{Label: label}
	changed := false
	for _, key := range obj.keys {
		newRaw, plan, entryChanged, err := rewriteEntry(key, obj.values[key], nimPath, clientLabel, repoint)
		if err != nil {
			return raw, Group{}, false, fmt.Errorf("entry %q: %w", key, err)
		}
		group.Entries = append(group.Entries, plan)
		if entryChanged {
			obj = obj.set(key, newRaw)
			changed = true
		}
	}

	newRaw, err := obj.marshalIndent()
	if err != nil {
		return raw, Group{}, false, err
	}
	return newRaw, group, changed, nil
}

// rewriteEntry classifies and, where appropriate, rewrites one mcpServers
// entry. It never errors on a malformed entry: a client config file that
// does not match what this package expects is left exactly as it was, with
// the reason recorded in EntryPlan.Detail, rather than aborting the whole
// run over one entry it does not understand.
func rewriteEntry(key string, raw json.RawMessage, nimPath, clientLabel string, repoint bool) (json.RawMessage, EntryPlan, bool, error) {
	before := preview(raw)
	unchanged := func(status EntryStatus, detail string) (json.RawMessage, EntryPlan, bool, error) {
		return raw, EntryPlan{Key: key, Status: status, Detail: detail, Before: before, After: before}, false, nil
	}

	entry, err := decodeOrderedObject(raw)
	if err != nil {
		return unchanged(StatusUnrecognized, "not a JSON object; left alone")
	}

	if _, ok := entry.values["url"]; ok {
		return unchanged(StatusHTTPSkipped, "HTTP/SSE server; left alone")
	}

	cmdRaw, ok := entry.values["command"]
	if !ok {
		return unchanged(StatusUnrecognized, `neither "command" nor "url"; left alone`)
	}
	var command string
	if err := json.Unmarshal(cmdRaw, &command); err != nil || command == "" {
		return unchanged(StatusUnrecognized, `"command" is not a non-empty string; left alone`)
	}

	var args []string
	if a, ok := entry.values["args"]; ok {
		_ = json.Unmarshal(a, &args) // malformed args is treated as empty below, not an error
	}

	if isNimCommand(command) && len(args) > 0 && args[0] == "serve" {
		if pathsEquivalent(command, nimPath) {
			return unchanged(StatusAlreadyNim, "already through Nim")
		}
		if !repoint {
			return unchanged(StatusStalePath, fmt.Sprintf(
				"routed through a nim binary at a different path (%s); re-run with --repoint to fix", command))
		}
		entry = entry.set("command", rawString(nimPath))
		newRaw, err := entry.marshalIndent()
		if err != nil {
			return raw, EntryPlan{}, false, err
		}
		return newRaw, EntryPlan{
			Key: key, Status: StatusRepointed,
			Detail: fmt.Sprintf("repointed from %s", command),
			Before: before, After: preview(newRaw),
		}, true, nil
	}

	if !daemon.ValidConnectorTarget(key) {
		return unchanged(StatusInvalidName, "not a valid connector name (letters, digits, '-', '_'; must start and end alphanumeric); skipped")
	}

	newArgs := append([]string{"serve", "--connector", key, "--client", clientLabel, "--", command}, args...)
	entry = entry.set("command", rawString(nimPath))
	entry = entry.set("args", rawStrings(newArgs))
	newRaw, err := entry.marshalIndent()
	if err != nil {
		return raw, EntryPlan{}, false, err
	}
	return newRaw, EntryPlan{
		Key: key, Status: StatusWrapped,
		Before: before, After: preview(newRaw),
	}, true, nil
}

// isNimCommand reports whether command names a nim binary, by its base name
// alone -- the same shallow check idempotence has to make, since a config
// file only ever records a path, not which binary is "really" running.
func isNimCommand(command string) bool {
	base := strings.TrimSuffix(filepath.Base(command), ".exe")
	return base == "nim"
}

// pathsEquivalent compares two paths the way this package needs to: after
// cleaning, not after resolving symlinks. nimPath is always the resolved,
// absolute path of the running binary (see resolveNimPath in cmd/nim), but a
// command already sitting in a config file may not be, and re-resolving a
// path nim init did not write itself risks failing on a file that no longer
// exists.
func pathsEquivalent(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// preview renders an entry for the before/after a dry run prints, with every
// env value hidden. An env block is where a client config keeps its API
// tokens -- the reason nim init tells the operator to leave it exactly as it
// is -- and a diff that printed it put real secrets into terminal scrollback
// and whatever a run was piped into. Found by review. The keys stay, so the
// operator can see the block survives the rewrite untouched.
func preview(raw json.RawMessage) string {
	entry, err := decodeOrderedObject(raw)
	if err != nil {
		return prettyOrRaw(raw)
	}
	envRaw, ok := entry.values["env"]
	if !ok {
		return prettyOrRaw(raw)
	}
	env, err := decodeOrderedObject(envRaw)
	if err != nil {
		return prettyOrRaw(raw)
	}
	for _, k := range env.keys {
		env = env.set(k, rawString("(value not shown)"))
	}
	redactedEnv, err := env.marshalIndent()
	if err != nil {
		return prettyOrRaw(raw)
	}
	redacted, err := entry.set("env", redactedEnv).marshalIndent()
	if err != nil {
		return prettyOrRaw(raw)
	}
	return prettyOrRaw(redacted)
}
