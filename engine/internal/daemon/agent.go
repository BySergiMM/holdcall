package daemon

import (
	"fmt"
	"time"

	"github.com/BySergiMM/nim/engine/internal/journal"
	"github.com/BySergiMM/nim/engine/internal/peer"
)

// Enrolment: binding a name the operator chose to the executable a client
// program runs.
//
// Nothing here affects a decision yet. This milestone only records what an
// agent is, so that the step which starts deciding has a subject that was
// never supplied by the thing being judged.
//
// Why an executable and not a label: a name sent by the caller is a claim, and
// a claim is what `--connector github -- <my own command>` was before the
// command binding closed it. An agent that could state its own identity could
// state a more privileged one, which does not merely bypass a policy, it
// inverts it. So the operator enrols, the kernel answers, and the agent is
// never asked.
//
// What this establishes and what it does not: two different client programs
// are different agents. Two windows of the same program are the same agent,
// because they run the same file, and nothing available to Nim distinguishes
// them. That is a real limit of the model, not an omission in the code.
//
// One property this shares with connector registration and should be read
// alongside it: enrolling is as privileged as running Nim. Anything that can
// execute this binary as this user can enrol an agent or replace an existing
// enrolment, exactly as it can register a connector. That is a property of
// running everything as one user, and it is the reason the step that adds
// grants has to say plainly what an enrolment is worth.

// resolveAgentImage turns the operator's path into the identity a running
// process will be matched against.
//
// The daemon resolves it, never the caller. A caller that could send a device
// and inode could enrol a name against numbers it did not obtain from the
// filesystem, which is the same shape of mistake as accepting a command to
// inject a credential into.
//
// Stat-ing a path is safe *here* in a way it is not at match time: the path is
// configuration the operator typed, not something a peer controls. At match
// time the identity comes from the kernel's view of a running process, which
// is what internal/peer exists for.
func resolveAgentImage(path string) (dev uint64, ino uint64, err error) {
	img, err := peer.ImageOfFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("cannot enrol %s: %w", path, err)
	}
	return img.Dev(), img.Ino(), nil
}

func handleAgentAdd(req Request, j *journal.Journal) Response {
	if err := validateAgentName(req.AgentName); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if err := validateAgentPath(req.AgentPath); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	dev, ino, err := resolveAgentImage(req.AgentPath)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if err := j.AddAgent(journal.Agent{
		Name:       req.AgentName,
		ExecDev:    dev,
		ExecIno:    ino,
		ExecPath:   req.AgentPath,
		EnrolledAt: time.Now(),
	}); err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("recording the enrolment: %v", err)}
	}
	return Response{ID: req.ID}
}

func handleAgentList(req Request, j *journal.Journal) Response {
	list, err := j.ListAgents()
	if err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("listing agents: %v", err)}
	}
	infos := make([]AgentInfo, len(list))
	for i, a := range list {
		infos[i] = AgentInfo{
			Name:       a.Name,
			ExecDev:    a.ExecDev,
			ExecIno:    a.ExecIno,
			ExecPath:   a.ExecPath,
			EnrolledAt: a.EnrolledAt.Format(time.RFC3339Nano),
			Current:    stillTheEnrolledFile(a),
		}
	}
	return Response{ID: req.ID, Agents: infos}
}

// stillTheEnrolledFile reports whether the file at the recorded path is the
// one that was enrolled.
//
// Only ever shown, never acted on. An enrolment that no longer matches is not
// a security finding: a device number can change when filesystems are mounted
// differently across a reboot, and an application that updates itself becomes
// a different file. Both leave an enrolment that will match no running
// process, and the operator needs to be told that in a list rather than
// discover it as a silent denial later.
func stillTheEnrolledFile(a journal.Agent) bool {
	dev, ino, err := resolveAgentImage(a.ExecPath)
	if err != nil {
		return false
	}
	return dev == a.ExecDev && ino == a.ExecIno
}

// handleAgentRemove is idempotent by design: removing a name that is not
// enrolled is success, not an error, so a caller does not have to check first.
// RemoveAgent reports that case as found = false with no error, and no entry
// is written for it -- there is nothing to record removing, and the chain
// records what changed, never what was attempted.
func handleAgentRemove(req Request, j *journal.Journal) Response {
	if err := validateAgentName(req.AgentName); err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	if _, _, err := j.RemoveAgent(req.AgentName); err != nil {
		return Response{ID: req.ID, Error: fmt.Sprintf("removing the enrolment: %v", err)}
	}
	return Response{ID: req.ID}
}
