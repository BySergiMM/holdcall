# Security

Holdcall exists to put a decision, a human, and a record between an agent and the
tools it can reach. A weakness in Holdcall is therefore worth more to us than a
feature, and the project already publishes the ones it knows about:
`docs/security.md` lists every attack that has been run against a real build
with its result, and the *Known gaps* table lists what is open and why;
the status page at https://holdcall.vercel.app/status/ shows the same,
generated from this repository.

## Reporting

If you find something that is not on that list, please tell us privately
before anyone else: open a
[private vulnerability report](https://github.com/BySergiMM/holdcall/security/advisories/new)
on GitHub. Include the build you tested (`holdcall version`), the platform, and
the steps; a failing test in the repository's own style is the best possible
report, but a shell transcript is enough.

We answer within seven days, say what we understood and what we will do,
and credit you in `CHANGELOG.md` and on the status page unless you ask us
not to. There is no bounty programme.

## What is in scope

- The relay forwarding a `tools/call` that the daemon did not allow, or one
  the human did not approve.
- A process that is not this binary obtaining a decision, a credential, or
  an approval from the daemon socket.
- A credential reaching a process other than the one connector it was
  registered for, or appearing in argv, SQLite, a log, or the console.
- A held call's arguments reaching the journal, a log, or a network.
- A journal edit that `holdcall verify` does not detect *and* that the format
  says it should.

## What is known and out of scope

The chain is unkeyed: a local attacker who can write `holdcall.db` can rewrite
history and it will verify, which is why `holdcall verify --expect-head` exists
and why the project never calls the journal tamper-proof. Peer identity is
unsupported on Windows, and nothing here has run there. Enrolling an agent
is as privileged as running Holdcall. These are documented decisions, not
findings; `docs/security.md` and `docs/decisions/` say why.
