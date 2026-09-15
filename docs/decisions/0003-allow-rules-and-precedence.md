# 3. Allow rules, and the precedence between them and deny

Status: accepted, m1-bootstrap, 2026-09-15. This is M4.5 on the dashboard.

## The question

M4 could not say "only Claude Code may touch github; nothing else may." A
rule could only take a capability away, so the sentence an operator actually
wanted -- an allow-list -- had no rule to write. Adding one needed three
things settled at once: what an allow rule looks like, what happens when an
allow and a deny both name the same call, and D-002 answered again, because
"enrolment restricts" stops being true the moment an allow rule exists.

The instinct to reach for a policy language here -- Cedar, the spike M4
deferred -- was checked against what M4.5 actually needs to say, which is:
add one more effect to the existing rule, and decide which of several
matching rules wins. Neither needs conditions on arguments, on time, or a
grammar. A precedence is not a policy language, and building one before the
model needs it would be choosing syntax with nothing yet to say.

## The model

`effect` is `deny` or `allow`. `tool` is either an exact name or `*`, and `*`
means only one thing: a default, for every tool, set by `nim policy default
deny|allow` and nothing else. It is not a wildcard or a prefix -- there is no
matching shorter than the whole tool name, anywhere in this model, before or
after this milestone. `nim policy deny <tool>`, `allow <tool>` and the exact
match at decision time are unchanged from M4 except that "*" is refused there
as an ordinary tool name, so there remains exactly one way to write a rule
that matches more than one tool.

**A client that genuinely calls a tool named "*" is not special-cased away
from this.** The matching query does not distinguish "the literal tool `*`
was called" from "no default applies to this specific tool" -- both ask
`tool = ? or tool = '*'` and get the same row back when the exact tool
argument is itself `*`. A default rule and a rule for a tool literally named
`*` are, deliberately, the same row. Documented here rather than guarded
against, because guarding against it would mean the daemon lies about what a
client actually sent, which is a worse property than an edge case nobody is
expected to hit.

**Precedence, evaluated in one function, `journal.Decide`:**

1. **Higher specificity wins.** Specificity is 2 for an exact tool match, 0
   for a default; +1 if the rule names an agent; +1 if it names a connector.
   An exact-tool rule beats a default naming the same agent and connector. A
   rule naming both an agent and a connector beats one naming only one of
   them.
2. **At equal specificity, deny beats allow.** An operator who wrote both,
   at the same specificity, gets the safer of the two rather than whichever
   happened to be added second.
3. **No matching rule at all is allow.** This is the M4 baseline, restated:
   a fresh install, or a tool no rule mentions, behaves exactly as it did
   before allow rules existed.

A tie that survives both of those -- two rules of equal specificity and
equal effect, naming different things, both matching one call -- cannot
change the decision, because they agree on it. It is broken only for
determinism (the lower id, the rule added first), so that two evaluations of
the same rule set, and `nim policy explain`, never disagree about which rule
was "the" reason.

`journal.Decide` takes the candidate rules and nothing else -- no database,
no session. `journal.MatchingRules` gathers those candidates: every rule
whose agent is null or equal, connector is null or equal, and tool is either
the exact one or `*`. Splitting the two means the precedence itself has a
table-driven test with no SQLite in it (`TestDecideAppliesSpecificityThenDenyOverAllow`,
`internal/journal/decide_test.go`), and the query is one `select`, not a
scan through every rule in the table -- the unique index on `(agent,
connector, tool)` bounds the candidate set to at most eight rows for any one
call.

## Uniqueness has not changed

A scope and a tool still hold exactly one rule -- the unique index is
unchanged -- but now that rule can be either effect. Adding an allow where a
deny already exists for the same `(agent, connector, tool)` is refused the
same way adding a second deny always was: `ErrRuleExists`. This is
deliberate. A scope that could hold both an allow and a deny would need its
own second precedence, invented for a case an operator can already express
by removing the old rule first. `nim policy remove` does not need to know
which effect it is removing, for the same reason: a scope and tool name at
most one rule regardless of effect.

## D-002, again

M4's answer was: a session no enrolment matched is bound by the rules that
name no agent, and by nothing else -- safe, because rules only ever took
capabilities away, so an unenrolled program had exactly the ceiling every
session had before agents existed.

That argument used one fact about the model that is no longer true: that no
rule can widen what a session may do. An allow rule can. So the case 0002
deferred -- "wherever enrolment is what grants, an unknown agent must be
denied" -- now has an answer, and it is the same mechanical fact restated for
this model rather than a new mechanism:

**A session with no agent is bound by the rules that name no agent, and by
nothing else. That has not changed. What changed is what those rules can
say.**

An allow-list per agent -- "only claude-code may touch github" -- is now
`nim policy default deny` (a deny naming no agent, so it binds every
session, enrolled or not) plus `nim policy allow <tool> --agent claude-code`
(more specific than the default, so it wins for that agent alone). An
unenrolled program meets only the default -- there is no rule naming no
agent that grants it anything -- and is denied. Enrolment is what grants,
exactly as the deferred case demanded.

The mechanism that makes this hold is `MatchingRules`' scope match, unchanged
from M4: a rule naming an agent applies only to sessions derived as that
agent. An unenrolled session's agent is `""`, which is never a stored value,
so an agent-scoped allow can never accidentally reach it. Nothing about
precedence weakens that; precedence only decides among rules that already
apply.

Tested at the answer level --
`TestDefaultDenyDeniesAnUnknownAgentWhileAnAgentScopedAllowAdmitsAnEnrolledOne`,
`internal/daemon/decide_test.go` -- and end to end with real processes --
`TestADefaultDenyClosesEverythingAndAnAgentScopedAllowReopensOneToolForOneAgent`,
`cmd/nim/e2e_test.go` -- following the shape
`TestTwoRealAgentsAgainstOneConnectorReceiveDifferentVerdicts` set for M4.

## `nim policy explain`

The precedence is meant to be inspectable, not just correct: `nim policy
explain <tool> [--agent] [--connector]` asks the daemon, through
`policy.explain`, which rule would decide a call shaped like that, and why.
The CLI never reads `nim_rules` itself -- every rule-shaped answer comes
through the daemon, over the same peer-verified socket as everything else,
exactly as `nim policy list` already did. The explanation is computed by
calling `journal.Decide` on the real candidates, never a second copy of the
precedence, so what it says can never drift from what a real call would get.

## What this still does not do

No conditions on arguments -- an allow or deny is still keyed on `(agent,
connector, tool)` alone, never on what a call's parameters say. No time
bounds, no budgets, no human approval. Those need a subject beyond a rule's
scope to talk about -- an argument, a clock, a counter -- which is what
"a policy language" would actually be for. Two rules and a precedence
between them said everything M4.5 needed to say; the day one of those is
needed, that is the day a language is worth its cost, not before.

Docs/milestones.md states this the same way for M4.5 as a milestone: what it
delivers, and what would still need a language.
