# 5. Human approval

Status: accepted, m1-bootstrap, 2026-09-15. This is M6 on the dashboard.

## The question

`holdcall policy deny` and `holdcall policy allow` are both decided by a rule, in
advance, with nothing about the call itself ever read. That is the right
shape for "this tool is always fine" and "this tool is never fine" -- but
some calls are neither: safe most of the time, and worth a person's eyes
before the one time it matters. Deny closes the door before anyone can look;
allow never asks. Neither can express "hold this one for a human, with what
it is actually about to do in front of them."

Two things had to be settled together: what the human is shown, and who
counts as that human.

## What the human sees, and why it is the real parameters

`holdcall approve` prints a held call's exact `params.arguments`, pretty-printed,
never a summary and never anything the model that asked for the call wrote
about itself. `docs/milestones.md` stated this as the requirement before any
of it was built: "never a model-generated summary." The reason is the threat
model in `docs/security.md` -- for this milestone specifically, the adversary
is not only a downstream MCP server, it is the model driving the client. A
summary is exactly the artifact that adversary can shape: a tool call titled
`send_email` with a summary reading "confirms the meeting" is a different
call from one whose real arguments are `{"to":
"finance@example.com","body":"wire the deposit to account
..."}`, and a model motivated to get the call through has every reason to
write the first sentence about the second call. The only text a human can
trust here is the bytes the server would actually receive, which is why the
daemon holds those bytes in memory and `holdcall approve` prints them verbatim.

Nothing else about the call is asked for. Tool, agent, connector and age are
shown alongside the arguments because they are what the rest of `holdcall log`
and `holdcall policy explain` already show for any call; none of it is
interpreted or reworded on the way to the terminal.

## Who may approve, and what that does and does not defend against

**Anyone who can run `holdcall` as this user.** There is no separate approver
identity, no second credential, no session tied to a particular terminal.
`holdcall approve <id>` from any shell that can reach the daemon's socket decides
the call, exactly as `holdcall policy deny` can already be run by anyone who can
run the binary.

Say plainly what this means: **approval defends against the model driving
the client, not against the operator.** The threat model in
`docs/security.md` has always excluded "a user who reconfigures their
client" and, by the same logic, excludes an operator choosing to approve
their own calls -- that is the operator doing what they are entitled to do
with a machine they run as themselves. What approval closes is the case
where the *model* -- not the person running it -- decides a dangerous call
should go through. A rule that says `ask` means "a human, not the model,
decides this one," and the daemon holding the call in memory, off the
model's channel, out of any digest, is what makes that hold real rather than
cosmetic: the model sees a call that has not been answered yet, and nothing
about the daemon's socket lets it answer on the operator's behalf. See
`docs/security.md`'s attack table, row 32, for the model trying anyway.

This is a floor, not a stronger claim. Anything able to execute this binary
as this user can enrol an agent, add a rule, or approve a call -- the same
sentence `docs/decisions/0002` and `docs/decisions/0003` already say about
enrolment and policy. Approval does not change what an enrolment or a rule
is worth; it adds a third thing worth exactly the same.

## The timeout, and why it fails closed

`[daemon] approval_timeout` (default `2m`, a duration string, read by both
the daemon and the relay from the same `config.Config`) bounds how long a
held call waits. Two clocks, not one, both fail toward denial:

- **The daemon's own timer**, armed the instant a call starts being held, is
  the one that actually decides: if nobody calls `holdcall approve`/`holdcall reject`
  before it fires, it rejects the call itself -- the same `call.request`
  entry, decision `rejected`, that an explicit `holdcall reject` would write --
  and tells the relay so, unasked.
- **The relay's own wait**, on the same connection, is a backstop rather
  than the primary mechanism: it is armed for the same duration, started a
  moment later (after the round trip that told it the call was pending), so
  in the ordinary case it hears the daemon's own timeout fire first and
  never needs its own. Reaching it without an answer at all means the
  daemon did not even manage to send that -- unreachable, not merely
  undecided -- and the relay denies locally and drops the connection, the
  same terminal failure `decisionTimeout` already uses for a daemon that
  stops answering (`engine/internal/shim/shim.go`).

This is the same argument `docs/decisions/0001-failure-behaviour.md` makes
for every other way of not getting a decision, extended to a wait long
enough that "nobody has looked yet" is the ordinary case rather than the
exception: a call that is never decided must never sit in a state that lets
it through by default, and both clocks land on the one answer that upholds
that -- reject, journaled, recorded like any other decision.

The client is told which kind of refusal it got. `mcp.DeniedByHuman`
("a human reviewing this call's real arguments rejected it") is what an
explicit `holdcall reject` or the daemon's own timeout both answer with, because
from the calling agent's side both mean the same thing: this specific call
did not clear the hold. `mcp.DeniedApprovalTimedOut` is reserved for the
relay's own local backstop -- a distinct sentence for a distinct failure,
the outage rather than the refusal, following the same "different sentences
on purpose" argument `engine/internal/mcp/deny.go` already makes for
`DeniedByPolicy` versus `DeniedNoDecision`.

## Pending calls live only in memory

A held call exists nowhere but the daemon's own process: `tool`, `agent`,
`connector`, the digest, the real arguments, and the clock -- all of it in
a `pendingRegistry`, never in SQLite, never in the journal, until it is
decided. This is deliberate on both sides of that sentence. Nothing about
the arguments can leak into the record while a call is held, because there
is no record of it yet to leak into; and nothing about a held call survives
the daemon dying, because there is nowhere else it lives.

That has a real consequence, stated rather than glossed: **if the daemon
process itself is killed outright, every call it was holding vanishes with
no journal entry at all**, exactly as an ordinary session in progress does
today when the daemon dies mid-flight (`docs/journal-format.md`'s "what the
record can and cannot tell you about losses" already says this about
sessions and outcomes, and it is the same fact here). The relay's own
approval-timeout backstop is what a client sees in that case: no answer
ever arrives, so it denies locally after `approval_timeout` and reports the
timeout text, but nothing is written to say the call was ever held at all.

**A daemon that shuts down in the ordinary way -- its connections closing
rather than the process being cut off mid-write -- does reject what it was
holding.** Each connection's own cleanup, the same one that writes a
`session.end` when a client disconnects, also rejects and journals every
call that connection had pending (`internal/daemon/daemon.go`'s deferred
cleanup in `handle`, and the explicit path for a graceful `session.end`
event). So an operator who stops the daemon with a signal it can act on
sees every held call resolved to `rejected` in the record; only a hard kill
loses that, and it loses it for exactly the reasons every other in-flight
state this daemon holds is already documented to be lost that way.

## What this still does not do

No conditions read `params.arguments` to decide whether to ask automatically
-- a rule is still keyed on `(agent, connector, tool)`, and `ask` is a fourth
value that column can hold, not a new kind of matching. No queueing or
notification beyond `holdcall approve` listing what is held: an operator has to
run it to find out anything is waiting. No delegation -- there is no way to
name who besides "anyone who can run holdcall" may decide a held call, because
Holdcall has no notion of a second identity to delegate to. Those would each need
a subject this milestone does not have, the same argument
`docs/decisions/0003`'s closing section makes about conditions, time bounds
and budgets.

## Addendum, 2026-09-15: nothing to approve until the arguments have arrived

Review of the merged implementation found a gap between the design and the
code. The daemon holds a call the moment an `ask` rule wins and answers
`pending`; the relay's `call.arguments` event follows on the same
connection a moment later. `holdcall approve <id>` reaches the daemon on a
different connection, and nothing tied the two together: an approval sent in
that moment was recorded, journaled and forwarded with the daemon never
having held a byte of what was approved, and `holdcall approve` showed such a
call exactly as it shows one that carries no arguments -- `(none)`.

Two changes close it. A held call now records whether the relay has
reported its arguments, separately from what they are, and an *approve* is
refused until it has, in words that say to run `holdcall approve` again; a
*reject* goes through regardless, because refusing what was not seen is the
safe direction. And the first report is the one: a second `call.arguments`
for the same call is ignored rather than replacing what a human may already
have read, so the bytes shown are the bytes approved. `holdcall approve` says
"not received from the relay yet" for the one state and "(none)" for the
other.

What this does not change: the window itself. The relay sends the report
immediately after reading `pending`, so under a human's hands it is closed
before `holdcall approve` can be typed. It mattered for anything scripted on top
of `holdcall approve`, and for the guarantee's wording: the only text a human can
trust is the bytes the server would receive, and now nothing can be approved
before those bytes exist on the daemon's side.

