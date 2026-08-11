package daemon

import (
	"log"
	"net"

	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/peer"
)

// deriveAgent works out which enrolled client program is behind a connection.
//
// The walk is: the kernel's record of who is on this socket, then that
// process's parent, then what file the parent is executing, then whether any
// enrolment matches it. A relay is spawned by the MCP client, so the relay's
// parent is the client's own process.
//
// Nothing in that chain is supplied by the thing being identified. There is no
// agent field on Event and there is not meant to be: an identity a caller
// could state is a claim, and a claim from a restricted agent naming an
// unrestricted one would invert a policy rather than merely bypass it. This is
// the same shape as the connector command binding -- the daemon derives, the
// caller does not assert.
//
// A wrapper is not the program. What this matches is the image the parent is
// *executing*, which is not always the path someone typed: on macOS, spawning
// a relay from `sh` produces a parent whose running image is /bin/bash,
// because sh execs it and the exec replaces the image. Enrolling /bin/sh
// therefore matches nothing, and enrolling /bin/bash matches. The same applies
// to any launcher that re-execs -- an application bundle's outer stub, a shell
// script, a version manager. Nothing here can see through that, and an
// enrolment that names a launcher will simply never match, which is why
// `nim agent list` reports what it resolved rather than only what was typed.
//
// An empty name is the ordinary answer, not a failure. It means no enrolment
// matched: nobody has enrolled an agent, or the program spawning relays is not
// one, or an enrolment has gone stale because its file was replaced. Nothing
// decides anything on this yet, so an unknown agent costs nothing today. The
// milestone that starts deciding has to say what an unknown agent may do, and
// that is a deliberate choice rather than something to be inherited from here.
//
// Failures are logged once per connection and are otherwise indistinguishable
// from "not enrolled". That is right while this is only recorded: refusing a
// session because a pid could not be read would turn an observability feature
// into an outage.
func deriveAgent(conn net.Conn, j *journal.Journal) string {
	pid, supported, err := peer.PIDOf(conn)
	if !supported || err != nil {
		return ""
	}
	ppid, err := peer.ParentOf(pid)
	if err != nil {
		// The client exited between connecting and now, or is one we may not
		// inspect. Either way there is no identity to record.
		return ""
	}
	img, err := peer.ImageOf(ppid)
	if err != nil {
		return ""
	}
	name, found, err := j.AgentByImage(img.Dev(), img.Ino())
	if err != nil {
		log.Printf("looking up the agent for this connection: %v", err)
		return ""
	}
	if !found {
		return ""
	}
	return name
}
