# The dashboard

`dashboard/` is a static page describing what Nim guarantees, what it does not,
and what has never been tested. It is built from this repository and published
as HTML with no server behind it.

It exists because the project's real state was spread across seven documents, a
CI log, and a conversation, and no single place said "here is what is actually
true today". It is meant to be that place. Which makes it, structurally, a very
good place to put things that are not true — so most of the design below is
about stopping that.

## The two halves

    dashboard/data/state.json     written by hand      the claims
    the repository                read at build time   the evidence

`scripts/generate.mjs` combines them into `src/data/generated.json`, which the
page renders. Nothing else writes that file, it is gitignored, and a stale copy
would be exactly the second source of truth this is meant to avoid.

## What makes it refuse

The generator exits non-zero, and writes nothing, when:

- a guarantee or attack cites a test that is not in the tree
- a guarantee claims `verified` with no test at all behind it
- an attack claims `pass` with no test at all behind it
- an attack claims `not_tested` while citing tests
- a guarantee names an implementation file that does not exist
- a platform is claimed `verified` when every cited test is build-tagged away
  from it
- a status, severity or cross-reference is not one of the known values

This is not decorative. The first run of the real `state.json` failed with nine
problems — every one a test name written from memory that did not exist. The
claims were corrected against the tree; the check was not relaxed.

`scripts/generate.test.mjs` is a mutation-test suite over that behaviour: it
corrupts a copy of `state.json` in each of the ways above and requires the
generator to refuse. A check nobody has watched fail has not been shown to work.
CI runs the whole thing, so renaming or deleting a test breaks the dashboard
build — which is the point, because otherwise the page would keep showing a
property as verified after the thing verifying it had gone.

## What it deliberately cannot do

**No runtime state, ever.** The journal, live sessions, enrolled agents,
connectors and daemon health are not on the page and are not going on it. Nim is
local-first; that state lives on the machine running Nim, and shipping it to a
hosted page would export the thing it is supposed to protect. The runtime
section says NOT AVAILABLE and names the local command for each datum instead.
`nim console` serves that over loopback, which is where it belongs.

**No secrets, by construction rather than by filtering.** The generator reads
`engine/`, `docs/` and `git log`. It never opens `nim.db`, the socket, or a
credential store, and a test asserts it does not even name a path into them.
A second test scans the generated output for home directories, absolute paths
and the common shapes of a token.

**Nothing live.** Every value is baked in at build time and is true of one
commit, which the page names. The CI block is a snapshot with its own commit and
timestamp, and the page marks it stale when that commit is not the one being
built. Nothing polls GitHub — a green badge that had gone red hours ago is worse
than no badge.

## The limits of the check, stated plainly

The check proves a cited test **exists**. It cannot prove the test establishes
the sentence printed next to it. A wrong summary written beside a real test name
passes, and no automatic check can catch that.

Platform columns are weaker still. The build refuses a platform marked verified
when every cited test is tagged away from it, but it cannot refuse a test that
*compiles* on a platform without establishing anything there — `pathswap_test.go`
compiles on Windows and proves nothing on it. Those columns rest on judgement.

`verified` on this page means a test passes. It does not mean audited, reviewed
by anyone outside this project, or proven. Nobody external has looked at Nim.

The Break Nim table lists the attacks someone thought of, not the attacks that
exist. It grows when Nim gets more scrutiny, not when it gets worse.

The page says all of this on itself, in a section called "What this dashboard
cannot tell you". That section is not an apology — it is the part that makes the
rest readable, because a page of green checkmarks with no stated boundary is
indistinguishable from marketing.

## Working on it

    cd dashboard
    npm install
    npm run check     # generate + mutation tests + typecheck
    npm run dev
    npm run build     # static export to dashboard/out

To change a claim, edit `data/state.json` and run `npm run check`. If it refuses,
the claim is wrong or the evidence moved — fix the claim, not the check.

## Looking at it

    cd dashboard
    npm run preview        # builds, then serves on http://127.0.0.1:4321

`preview` serves the real production build with the same headers `vercel.json`
sets, CSP included — which is the setting most likely to break the page in
production and not at all in `next dev`. It binds to 127.0.0.1 only, refuses
any path resolving outside `out/`, and needs no network.

## Deployment

Prepared, not deployed.

`vercel.json` sets `github.enabled: false`, so even if the repository is ever
linked to the project again, a push will not deploy on its own. It also sends
`X-Robots-Tag: noindex, nofollow, noarchive, nosnippet`, a strict
`Content-Security-Policy` (`default-src 'none'`, no external origins — the page
loads nothing off-host), `X-Frame-Options: DENY` and `Referrer-Policy:
no-referrer`.

Checked read-only on 2026-08-12: the Vercel project `nim` already has **Vercel
Authentication** enabled for `all_except_custom_domains`, and no custom domain
is attached. A deployment would therefore be readable only by members of the
Vercel team, not by the public.

Two things to know before that changes:

- **A custom domain bypasses it.** The protection is scoped
  `all_except_custom_domains`. Attaching a domain would make the page public
  unless Trusted IPs or password protection is enabled first.
- **Nothing reconnects GitHub to Vercel automatically**, and nothing should.
  Deployment is a deliberate act: `vercel deploy --prebuilt` from `dashboard/`,
  or the Vercel dashboard.

Whether to deploy at all is D-003 on the page, and is not a decision this
repository should take by itself.
