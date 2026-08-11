//go:build linux

package peer

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ImageOf returns the identity of the file pid is executing.
//
// /proc/<pid>/exe is a magic link the kernel resolves to the inode the process
// is running. Stat-ing the link itself is what makes this an identity rather
// than a filename: reading it and stat-ing the resulting string would compare
// whatever file is at that path now, which the process's owner controls, and
// is defeated with no race at all (see pathswap_test.go).
//
// A pid belonging to another user, or one that has exited, yields an error and
// a zero Image -- never a usable identity.
func ImageOf(pid int) (Image, error) {
	fi, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return Image{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Image{}, fmt.Errorf("stat of /proc/%d/exe returned no unix metadata", pid)
	}
	return Image{dev: uint64(st.Dev), ino: st.Ino}, nil
}

// ParentOf returns the pid that spawned pid.
//
// This is what makes an agent identity possible without asking anyone to
// declare one: a relay is spawned by the MCP client, so the client's own
// process is the relay's parent, and the daemon can walk from the socket peer
// to its parent and ask what file *that* is executing.
//
// Read from /proc/<pid>/status rather than /proc/<pid>/stat. stat puts the
// parent in the fourth whitespace-separated field, but the second field is the
// executable's name in parentheses and a process can put spaces and
// parentheses in its own name -- so splitting stat on whitespace is a parser a
// process can influence, which is the wrong shape for something feeding an
// identity decision. status is one key per line.
//
// A parent of 0 is refused rather than returned: it means the process has been
// reparented or is one this cannot describe, and treating that as an identity
// would be inventing one.
func ParentOf(pid int) (int, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		rest, found := strings.CutPrefix(line, "PPid:")
		if !found {
			continue
		}
		ppid, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			return 0, fmt.Errorf("parsing PPid for %d: %w", pid, err)
		}
		if ppid <= 0 {
			return 0, fmt.Errorf("process %d reports no parent", pid)
		}
		return ppid, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("no PPid line in /proc/%d/status", pid)
}
