# 6. What a hosted mirror may claim

Status: accepted, 2026-09-16. Resolves D-004 on the dashboard and unblocks
M8 as planned work with a narrower contract than the one first written.

## The question

M8 was blocked on something more basic than itself. The journal's chain is
unkeyed (F-003, accepted): nothing in it is secret, so anyone able to write
`nim.db` can recompute every hash from the genesis and a rewritten history
verifies cleanly. A mirror that copied such a record to a hosted page and
called it evidence would export a liability. D-004 offered two ways out:
give the journal a key the agent cannot reach, or present the mirror as
"what this machine reported". One of the two had to be chosen before a
viewer was built.

## The decision

**The mirror presents what this machine reported, and says so on every
row.** The journal does not get a key before M8.

The key was the more attractive answer and it is not available. A key the
agent cannot reach has to live under a principal the agent is not: a second
OS user, a hardware token, or an enclave. Nim runs as the operator's own
user on the operator's own machine, and the agent -- the model driving an
MCP client -- runs there too, with the same identity. Every place a key
could be put, the process that would sign with it and the process that
would forge with it are the same user. This is the same wall F-006 stands
against ("enrolment is as privileged as running Nim"): a second principal is
what closes both, and it is a deployment decision for the machine, not a
change this repository can make on its own. Pretending otherwise -- a key
in the credential store, say -- would produce signatures the agent could
mint, which is worse than no signatures, because it would look like
authenticity.

So the honest claim is the weaker one, and the project's own rule applies:
say exactly what was verified and nothing more. A hosted mirror may say:

- these rows are what the machine named here reported, in this order, at
  these times, and the chain it reported links as shown;
- `nim verify --expect-head` on that machine, against a head recorded
  elsewhere, is what turns the record into evidence -- the mirror can hold
  such heads, and can show where the local chain diverges from one, but it
  cannot stand in for that check.

It may not say "tamper-proof", "tamper-evident", "audit log", or "the agent
did this". README already forbids the first three for the local journal;
the mirror inherits the prohibition and adds the fourth, because between the
machine and the mirror there is one more party that could have written the
rows -- the sync itself.

## What follows for M8

The sync contract in `docs/milestones.md` stands unchanged: never
credentials, never call parameters, never response bodies, never the
decision inputs -- only agent, connector and tool names, decisions, timings
and the link hashes. Three things are added by this decision:

1. **Provenance on every row.** Each synced entry carries the machine id it
   came from and the head the machine reported at sync time, and the viewer
   labels the table "reported by <machine>", never "recorded".
2. **Heads as the one strong claim.** The mirror stores heads the operator
   pins from the machine (`nim verify --expect-head` material) separately
   from the rows the sync writes, under a different write path, so a sync
   that rewrites rows cannot also rewrite what they are compared against.
   The viewer shows agreement or divergence between the two; it does not
   average them.
3. **The engine never depends on the mirror.** The sync is a separate,
   opt-in command (`nim sync`, not the daemon), off by default, so a machine
   with no network and no account behaves exactly as today. Nothing hosted
   decides anything, as the milestone already said.

D-005 (whether the public page is search-indexed) is resolved the same day
in the reversible direction: `noindex` stays. A page that lists a project's
weaknesses can be indexed later if that is ever wanted; it cannot be
reliably un-indexed once crawled.

## What this does not settle

Which hosting receives the sync, and under whose account. The schema in
`supabase/migrations/` is the target this repository has prepared, but a
project, its credentials and its cost are the operator's, and M8 cannot
start without them. That is the one input the milestone still needs.
