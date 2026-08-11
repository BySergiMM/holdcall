# Deployment

**There is nothing in this repository to deploy, and that is deliberate.**

Nim is a local engine. It is a Go binary that runs on a developer's machine,
talks to MCP servers over stdio and to its own daemon over a unix socket. It
has no server-side component, no hosted API, and no web frontend.

The one HTML file in the tree, `engine/internal/console/index.html`, is
embedded in the binary and served by `nim console` over **loopback only**. It
makes three same-origin requests and no external ones. It is an observability
view of the local journal, deliberately read-only and deliberately unreachable
from a network interface. It is not a website and cannot be hosted.

## What is deployed at nim-seergiiim.vercel.app

A page generated with **v0** and pushed from the Vercel CLI on 2026-08-02. Its
source lives in a v0 chat, not in this repository or any other — it has never
been in version control. It is a scaffold, not a Nim dashboard, and nothing in
this repo produces it.

## History worth keeping

Between 2026-08-11 11:39 and 14:13 the Vercel project `nim` was connected to
this GitHub repository with the framework preset **Next.js**. There is no
Next.js app here, so all fourteen deployments failed with:

```
Error: No Next.js version detected. Make sure your package.json has "next" in
either "dependencies" or "devDependencies". Also check your Root Directory
setting matches the directory of your package.json file.
```

including the Production build for the M1–M3 merge. The git connection was
removed on 2026-08-11, so pushes no longer trigger builds. The project, its
domains and its one live deployment were left in place.

Two things are worth remembering from that. A red deployment on every commit is
noise, and noise that is always there teaches people to stop reading deployment
status — the same failure mode as a verification check that cries wolf. And the
preset was never wrong about the repository; the repository simply had nothing
of the kind in it.

## When a dashboard does exist

It is **M8**, and `docs/milestones.md` records why it is blocked on something
more basic than itself: syncing a record whose authenticity rests on an unkeyed
chain exports a liability rather than evidence. That decision comes first.

When it is built, connecting it means:

1. Put the app somewhere of its own — `web/`, per the shape the README
   describes.
2. In the Vercel project, connect the repository again and set **Root
   Directory** to that path, so the Next.js preset finds a `package.json`.
3. Set the Production Branch to whichever branch should serve production.

The sync contract in `docs/milestones.md` sets out what such a dashboard would
and would not be allowed to receive: never credentials, never call parameters,
never response bodies.
