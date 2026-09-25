# 4. Budgets

Status: accepted, m1-bootstrap, 2026-09-15. This is M5 on the dashboard.

## The question

`docs/milestones.md` sets M5 in one sentence: "Per session, decremented at
authorization time, not at execution time." Three things had to be settled
before that sentence was code rather than a slogan: what counts as a call for
the purpose of a cap, what a session actually is for the cap to reset on, and
why the count moves when a call is *decided* rather than when it is *run*.
None of the three is obvious, and getting any of them wrong would have made
the guarantee this milestone adds either too weak to matter or a different
guarantee than the one asked for.

## What a session is

One relay run: `session.start` to `session.end`. Nim already has this unit --
it is what `agent` and `connector` are properties of, and what every
`call.request` is numbered within. A budget reuses it rather than inventing a
second notion of "a client's use of a tool," because a second notion would
need its own boundary and its own answer to what a client restarting its
relay means, and the existing one already has both: a new relay run gets a
new session id, so a client that restarts starts a new session with a fresh
count. The docs say so plainly -- README.md and `docs/milestones.md`'s M5
entry both state it -- because it is the detail an operator relying on a
budget most needs to know before they trust it: a crash-and-restart loop is
not throttled by a budget scoped to one session, on purpose. A cap meant to
survive restarts needs a different subject than a session, and this milestone
does not build one.

## What counts

A budget counts `call.request` entries in its session whose `decision` is
`allow` or `approved` -- never `call.outcome`, and never a `deny`. Three
things follow from that, and each was a real choice:

**A refused call never counts against the budget that refused it, or any
other.** `CountAllowedCalls` (`internal/journal/budgets.go`) filters on
`decision`, so a session that spends its first five calls hitting a rule's
deny, or this same budget's own cap, arrives at its sixth call having spent
nothing. The alternative -- counting attempts regardless of decision -- would
make a budget and a rate limit the same thing, and they are not: a budget
caps how much a session may *do*, and a call Nim refused is something the
session was prevented from doing.

**A call the relay gave up on before forwarding still counts.** The relay's
`decisionTimeout` and SQLite's busy timeout mean an `allow` can be written to
the journal after the relay has already told the client "could not reach a
decision" and moved on (`docs/journal-format.md`'s "an allow is not evidence
the call was made"). That entry is indistinguishable, from the journal's own
point of view, from an allow the connector actually received -- both are
`allow` with no `call.outcome`, both read as `pending`. A budget that only
weighed confirmed outcomes would need to solve a problem M2 already declined
to solve, and would be wrong in the direction that matters less: undercounting
a budget lets more calls through than the operator asked for, which is a
policy gap, not merely an accounting one.

**Decremented at authorization time, not execution time, is therefore not a
choice between two ways to count the same thing -- it is the only choice
available.** Nim has no signal for "the connector actually ran this and
returned," only "Nim decided to let it through." `call.outcome` records
whether a call finished and how, but a denied call has no outcome and an
allowed one may never get one if the relay gives up waiting, so outcomes
cannot be what a budget is weighed against without also solving the pending-call
problem the M2 guarantee is explicit about not solving. Counting decisions is
what "per session, decremented at authorization time" was always going to
mean, once a session and a decision are the only two things Nim reliably
knows about a call.

## Why the check runs where it does, and not somewhere else

`daemon.answer` reads: rules decide, and only once they land on `allow` does
`checkBudgets` get asked anything. A budget that denied could instead have
been folded into `journal.Decide`'s precedence, alongside the rules
themselves -- and that was rejected. `Decide` picks among candidates that
already match a call's scope by specificity and effect; a budget is not a
competing rule with an effect and a specificity, it is a counter with a
session behind it, and giving it a slot in that precedence would have meant
inventing a specificity for something that is not scored the way a rule is.
Keeping the two separate -- rules decide, budgets afterward veto an allow --
is a smaller, more honest change than teaching `Decide` a second kind of
thing to compare.

**A budget never grants.** It has no effect field, no way to express "allow,"
and `checkBudgets` is consulted only when the decision already reads `allow`.
A call a rule denies is never offered to a budget at all, so there is no path
by which a budget's headroom could turn a `deny` into anything else --
`TestABudgetCannotMakeADeniedCallPass` holds the daemon to exactly that. This
is deliberate and mirrors what `docs/decisions/0002` and
`docs/decisions/0003` established for rules: the ceiling a session meets is
set by what already denies it, and nothing added since has been allowed to
lower that ceiling by accident or raise it by omission. A budget is
symmetric with an allow rule in the opposite direction -- where an allow rule
can *raise* what a more general deny would otherwise refuse, a budget can
only ever *lower* what the rules, taken together, already allow.

## Storage and the chain

`nim_budgets` sits outside the hash chain, exactly as `nim_rules` and
`nim_agents` do: it is configuration, the daemon's answer to "what may
happen," not the record of what did. `budget.add` and `budget.remove` are
what make a change to it auditable -- written in the same SQLite transaction
as the change to `nim_budgets` itself, so the chain never describes a budget
that was never stored and no budget exists that the chain does not know
about (`TestABudgetAndItsEntryAreOneChange`,
`TestABudgetChangeThatCannotBeRecordedIsNotMade`,
`internal/journal/budgets_test.go`). This is the same guarantee M4 established
for rules and F-018 closed for enrolments, applied to a third kind of policy
change.

Recording the cap needed a field the schema did not have: `budget_calls`,
field 20, added at schema_version 4 (`canonical_encode_v4`,
`docs/journal-format.md`). The same reasoning that forced v2 and v3 into
existence forced this one -- hashing a twentieth field under the v3 domain
would let one entry hash two different ways depending on which build read
it, which is exactly what a schema version exists to prevent. `nim_journal`'s
`kind` CHECK constraint widened the same way it did for `rule.add` and
`agent.add` before it: SQLite cannot alter a CHECK in place, so an existing
`nim_journal` is rebuilt once, on open, copying every row verbatim
(`TestADatabaseFromTheCurrentBuildGainsTheBudgetKindsAndStillVerifies`, which
opens a database built exactly as the pre-M5 code left it and checks the
chain out unchanged before and after).

## What `nim policy explain` says about a budget, and what it does not

`nim policy explain <tool> [--agent] [--connector]` lists every budget whose
scope matches the call it describes, and each one's cap. It does not say how
much of that cap a session has already used, because `explain` is not given a
session -- it answers "what would apply to a call shaped like this," the same
question it has always answered for rules, and a session's count is not part
of that shape. Inventing one (an empty count, or the busiest recent session's
count, or anything else) would be a guess dressed up as an answer, and worse
than saying nothing: an operator who reads a number from `nim policy explain`
reasonably expects it to be true of the call they are about to make, not an
average across every session that ever asked. `checkBudgets`, the daemon's
own counting, is the only place a real count is computed, and it is computed
against the one session actually asking.

## What this does not do

**No time bounds.** A budget counts calls, not calls per hour or calls since
a clock reading; `docs/decisions/0003`'s closing section named this
alongside budgets as needing "a subject to talk about," and a session is the
subject this milestone chose -- a clock is a different one, for whenever it
is asked for.

**No budget that survives a restarted relay.** Stated above and worth
repeating here: per session means exactly that, and a client that restarts
its relay is a new session with nothing carried over. An operator who wants a
cap that survives a crash loop needs a subject other than a session, which
this milestone does not build.

**No conditions beyond scope and a count.** A budget is `(agent, connector,
tool, calls)`, the same four things a rule's scope is plus one number. It
does not read `params.arguments`, and a call's cost is not weighed -- every
allowed call spends exactly one unit of whichever budgets cover it,
regardless of what the call actually asked a connector to do.
