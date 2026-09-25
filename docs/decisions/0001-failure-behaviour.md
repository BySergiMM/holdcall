# 1. What Holdcall does when it cannot do its job

Status: accepted, integration/trunk.

## The apparent conflict

The two lineages took opposite positions and both were described as
"fail-open" and "fail-closed", as if answering one question.

- `m1-bootstrap` fails **open**: a shim that cannot reach the daemon spawns
  the downstream anyway, with no credential and no recording.
- the audit branch fails **closed**: a shim that cannot reach the daemon
  denies every `tools/call`.

They are not actually in conflict. They answer different questions, and the
merge only looked impossible because one word was doing two jobs.

## The three questions

**1. Is a connector configured for this target?**

Most targets have none. "No connector" is not a failure, it is the ordinary
state, and a target the operator never configured must keep working exactly as
it does with no credential support at all. **Open**, and calling this
"fail-open" was the confusion: nothing failed.

**2. May this call proceed?**

This is the authorization question, and it is the product. If it failed open,
`kill $(pgrep holdcall)` would be a universal enforcement bypass — every guarantee
Holdcall makes would be one signal away from not holding, and an agent able to run
a shell could always send that signal. **Closed**, on every path: no daemon, a
slow daemon, a closed socket, a mismatched reply, a journal write that failed.

**3. Can this credential be released?**

A connector *is* configured but its secret cannot be retrieved, or has no
authorized command. Starting the downstream without the credential it was set
up to need produces failures that look like the remote service's fault.
**Closed**: refuse to spawn.

## What this means concretely

| Situation | Behaviour | Why |
|---|---|---|
| No connector configured | spawn, no credential | not a failure |
| Connector configured, secret unavailable | refuse to spawn | question 3 |
| Connector configured, no authorized command | refuse to spawn | question 3 |
| Daemon unreachable at spawn | **spawn, deny every call** | see below |
| Daemon dies mid-session | every later call denied | question 2 |
| Journal write fails | deny that call | question 2 |
| Socket removed under a live daemon | existing connections survive; new ones cannot dial, so their calls are denied | question 2 |

## The one case worth explaining

**Daemon unreachable at spawn time: start the relay, then deny everything.**

Refusing to spawn would be defensible, and it is what question 3 does. It is
rejected here because it is worse for the operator without being safer. A
client that cannot start its MCP server shows "server failed"; a client whose
server starts but whose tools are refused shows the tool list, completes
`initialize`, and gets an explicit refusal naming Holdcall on each call. The second
tells the user what is wrong. Neither leaks anything, because no credential is
injected in either case.

It is not less safe: with no daemon there is no credential to inject and no
call can be allowed, so the downstream that starts is one that can do nothing.

A consequence to be aware of: if the daemon comes back, that shim keeps its
connection state from when it started. It reconnects for decisions but was
never given a credential, so its calls may be allowed by Holdcall and then rejected
by the remote service for want of one. That is a degraded state, not an unsafe
one, and it resolves when the session restarts.

## What is deliberately not claimed

Failing closed protects the *decision*. It does not protect against an agent
that never involves Holdcall at all: nothing here stops a client being reconfigured
to spawn the MCP server directly, and no in-process shim can. That is a
property of where Holdcall sits, and closing it needs the connector to be
unreachable except through Holdcall — a network or sandbox boundary, not a flag.

Holdcall's guarantee is about calls that go through it. It is worth stating plainly
rather than implying more.
