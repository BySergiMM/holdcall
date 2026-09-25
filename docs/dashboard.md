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

**Evidence does not exist**

- a guarantee or attack cites a test that is not in the tree
- a guarantee names an implementation file that does not exist

**A claim outruns its evidence**

- a guarantee claims `verified` with no test at all behind it
- an attack claims `pass` with no test at all behind it
- an attack claims `not_tested` while citing tests
- a platform is claimed `verified` when every cited test is build-tagged away
  from it

**A status is asserted rather than argued**

- any status, severity, layer state or CI conclusion that is not a known value —
  all of them render through one lookup that falls back to neutral grey, so an
  invented status does not look wrong, it looks calm
- an attack row with no written assessment, whatever its status. `fail` and
  `not_applicable` need no test to be legitimate, which made them the cheapest
  place to park an inconvenient row
- a finding missing a problem, impact, evidence or way out
- a milestone marked `done` that still lists outstanding work
- a decision marked `resolved` with no `decidedOn` date
- a CI snapshot reporting `success` while listing a failed job
- an id that appears twice, which would double-count in every total

**Runtime state or a secret tries to get in**

- `runtime.available` is anything but `false`, or `runtime` carries any key
  beyond its fixed four
- any string anywhere in `state.json` matching a token shape, a private key
  block, or an absolute user path

This is not decorative, and it is not theoretical. The first run of the real
`state.json` failed with nine problems — every one a test name written from
memory that did not exist. The `done`-with-pending-work rule failed on its first
run too, catching M2. The claims were corrected; the checks were not relaxed.

`scripts/generate.test.mjs` is a mutation-test suite over all of it: three
dozen tests that corrupt a copy of `state.json` in each of the ways above and
require a refusal, one of which walks every shape in `scripts/sensitive.mjs`
-- the one list both scanners read -- and proves each is caught. Most of them exist because the attack worked first — a deliberate
red-team pass on 2026-08-12 tried sixteen bypasses and fourteen got through.
A check nobody has watched fail has not been shown to work.

CI runs the whole thing, so renaming or deleting a test breaks the dashboard
build — which is the point, because otherwise the page would keep showing a
property as verified after the thing verifying it had gone. CI also refuses to
go green when a cited test was *skipped* rather than run, via
`.github/scripts/assert-evidence-ran.sh`.

## What it deliberately cannot do

**No runtime state, ever.** The journal, live sessions, enrolled agents,
rules, connectors and daemon health are not on the page and are not going on
it. Nim is local-first; that state lives on the machine running Nim, and
shipping it to a hosted page would export the thing it is supposed to protect.
The runtime section says NOT AVAILABLE and names the local command for each
datum instead: `nim console` serves the journal and its sessions over
loopback, and `nim agent list`, `nim policy list` and `nim connector list`
show the rest.

**No secrets, by construction and then by scanning anyway.** The generator reads
`engine/`, `docs/` and `git log`. It never opens `nim.db`, the socket, or a
credential store, and a test asserts it does not even name a path into them.

Construction is not enough on its own, because `state.json` is written by hand
and the page contains prose written straight into `.tsx` files. So it is scanned
twice: `generate.mjs` walks every string in the declared data, and
`scan-output.mjs` walks every byte of `out/` after the build — the only artefact
whose contents are the thing actually served. That second scan is what catches a
token in a component, in the props Next serialises into the HTML, or in an
inlined chunk; the data-file scan sees none of those.

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

Deployed, publicly, by a deliberate decision (D-003 on the page, 2026-08-12):
the page is written to be public, states its own limits, and carries no
runtime data, no secrets and no local paths -- enforced at build time, not
promised. Deployment protection on the Vercel project is off. That decision is
about this page only and extends to nothing else the project publishes.

Since 2026-09-25 the same deployment serves two pages. The product page at
`/` is the public face of Nim: a real session's `nim log`, the `nim approve`
moment, how the relay fits, what Nim does not promise, and an early-access
form. The status page, this dashboard, moved to `/status/`; every anchor it
had still works there. Both are built from the same repository by the same
generator, and the output scan covers both. The product page is meant to be
found, so the `noindex` header (D-005) now applies to `/status` only.

The early-access form is the one thing here with a server behind it:
`api/waitlist.js`, a Vercel function outside Next that stores an address and
a timestamp in a private Blob store and nothing else, one record per address.
Its token is a Vercel environment variable, never in the repository. When the
store is absent the function answers 503 and the page says sign-ups are not
open yet, rather than pretending.

How it got there is worth keeping. An earlier version of this section said the
project had Vercel Authentication enabled and that a deployment would be
readable only by the team. Reading the setting was not enough: the protection
was scoped `all_except_custom_domains`, Vercel treats the project's own
assigned production domain as a custom domain, and the page was readable by
anyone at that hostname for about two minutes before it was noticed by
fetching every hostname anonymously (F-011). The lesson is the general one:
verify access control by fetching, never by reading the configuration that is
supposed to provide it.

Two things remain true:

- `vercel.json` sets `github.enabled: false`, so a push never deploys on its
  own; deployment is by hand from `dashboard/`, with the project linked
  through `VERCEL_ORG_ID` and `VERCEL_PROJECT_ID`: `vercel build --prod` over
  the same `npm run build` the checks run, then `vercel deploy --prebuilt
  --prod`. Last done on 2026-09-16 from this branch, and verified by fetching the
  production domain anonymously afterwards.
- Until 2026-09-16, historical deployment URLs and stale branch aliases also
  answered publicly (F-012), serving Vercel's failure page and an abandoned
  placeholder. They were removed that day; every one of them answers 404 now,
  and one previous production build is kept as a rollback candidate.

The build still sends `X-Robots-Tag: noindex, nofollow, noarchive, nosnippet`
(D-005: public and indexed are different things), a strict
`Content-Security-Policy` (`default-src 'none'`), `X-Frame-Options: DENY` and
`Referrer-Policy: no-referrer`.
