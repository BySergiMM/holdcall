# 2. What an unknown agent may do

Status: accepted, m1-bootstrap, 2026-09-14. This is D-002 on the dashboard.

## The question

The daemon derives an agent for every session from the kernel: the process
that spawned the relay, and the file it is executing, matched against the
enrolments the operator made. Most sessions match nothing. Nobody has enrolled
anything, or the program is not one the operator cares to name, or an
enrolment went stale when the application updated itself. Those sessions have
no agent.

Once rules can be scoped to an agent, that session needs an answer. The worry
when the question was opened: if "not enrolled" were the more permissive
state, enrolling would be a downgrade, and anything that could choose would
choose not to be enrolled.

## The answer, for the policy that exists

**A session with no agent is bound by the rules that name no agent, and by
nothing else.**

That is not a bypass, because of what a rule can say. Rules only deny. A rule
naming an agent takes something away from that agent's sessions; there is no
rule that gives anything. So enrolment can only ever restrict, an unenrolled
program has exactly what the global rules allow, and that is exactly what it
had before agents existed. Nobody gains by avoiding enrolment: the ceiling is
the same, and the operator who wanted to lower it for one program can.

Stated as the cases a decision meets:

| session's agent | rule names | applies |
|---|---|---|
| none | no agent | yes |
| none | `cursor` | no |
| `cursor` | no agent | yes |
| `cursor` | `cursor` | yes |
| `cursor` | `claude-code` | no |

The same holds for connectors: a rule naming one applies to sessions started
for it, and the connector is a property of the session, taken from
`session.start`, never from the call.

## What this does not answer

An allow-list per agent -- "only Claude Code may touch github; nothing else
may" -- is the case where an unknown agent must be denied, because enrolment
would be what grants. That needs an allow rule, and an allow rule needs a
precedence between allow and deny, which is a policy language. That is M4.5,
with the Cedar spike in front of it, and it has to answer this question again
for that model rather than inherit this answer.

Until then the direction is fixed: enrolment restricts. A rule that could
widen what a session may do would invert the sentence above, and must not be
added to the deny-only model as a convenience.

## What an enrolment is worth

The same as a connector registration or a rule: anything able to run this
binary as this user can make one, change one, or remove one. That is a
property of running everything as one OS user, and running everything as one
OS user is not what stops an enrolment being abused -- the record is. So the
entry is now the record: `agent.add` and `agent.remove` carry every enrolment
change the same way `rule.add` and `rule.remove` carry every policy change,
written in the same transaction as the change itself (F-018, closed). A rule
names an agent; the chain now says what that name was bound to at every point
it could have been asked to enforce anything, not only what the rule said.
`docs/security.md` says the same under *What protects what*.
