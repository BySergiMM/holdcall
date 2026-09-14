# Deployment

One thing in this repository is deployed: `dashboard/`, a static page of what
is true about the repository, built from it. `docs/dashboard.md` describes it,
and its *Deployment* section is the record of how it is published and why it
is public. This file exists for the engine, which is not deployed anywhere and
is not meant to be.

## The engine is not a service

Nim is a local engine. It is a Go binary that runs on a developer's machine,
talks to MCP servers over stdio and to its own daemon over a unix socket. It
has no server-side component and no hosted API.

The one HTML file in the engine, `engine/internal/console/index.html`, is
embedded in the binary and served by `nim console` over **loopback only**, and
since 2026-09-14 only to a loopback name in the `Host` header -- a page on
another site that rebinds its own name to 127.0.0.1 is refused. It makes three
same-origin requests and no external ones. It is an observability view of the
local journal, deliberately read-only and deliberately unreachable from a
network interface. It is not a website and cannot be hosted.

## Releases

`.github/workflows/release.yml` builds the five cross-compiled targets, runs
the engine test suite against them first, and publishes a GitHub Release with
`nim_<tag>_<os>_<arch>` archives and a `SHA256SUMS` covering all of them.
Nothing in it runs on its own: it triggers only on a tag matching `v*` being
pushed, which is a human action this repository does not take by itself, and
`workflow_dispatch` builds and checksums the same five archives as a dry run
without ever creating a release. The `publish` job is the only one with
`contents: write`, and only for the length of that job. `install.sh` at the
root downloads a release archive, verifies its checksum against `SHA256SUMS`,
and refuses on any mismatch; `tools/install-rig/test.sh` proves the refusal
bites.

## History worth keeping

Between 2026-08-11 11:39 and 14:13 the Vercel project `nim` was connected to
this GitHub repository with the framework preset **Next.js**, at a time when
there was no Next.js app here, so all fourteen deployments failed:

```
Error: No Next.js version detected. Make sure your package.json has "next" in
either "dependencies" or "devDependencies". Also check your Root Directory
setting matches the directory of your package.json file.
```

The git connection was removed the same day, and it has not been restored:
`dashboard/vercel.json` sets `github.enabled: false`, so a push never deploys
on its own. Those fourteen failed deployments still answer publicly with
Vercel's failure page (F-012), as does a v0 placeholder pushed on 2026-08-02
from a chat that was never in version control. Deleting them is a human's
call.

Two things are worth remembering from that. A red deployment on every commit
is noise, and noise that is always there teaches people to stop reading
deployment status -- the same failure mode as a verification check that cries
wolf. And the preset was never wrong about the repository; the repository
simply had nothing of the kind in it, until it did.

## What a hosted journal viewer would be

It is **M8**, and `docs/milestones.md` records why it is blocked on something
more basic than itself: syncing a record whose authenticity rests on an unkeyed
chain exports a liability rather than evidence (D-004). That decision comes
first.

The sync contract in `docs/milestones.md` sets out what such a viewer would
and would not be allowed to receive: never credentials, never call parameters,
never response bodies. `supabase/migrations/` holds the schema it would land
in; nothing writes to it.
