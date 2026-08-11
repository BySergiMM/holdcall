# Deployment

**There is nothing in this repository to deploy, and that is deliberate.**

Nim is a local engine. It is a Go binary that runs on a developer's machine,
talks to MCP servers over stdio and to its own daemon over a unix socket. It
has no server-side component, no hosted API, and no web frontend.

The one HTML file in the tree, `engine/internal/console/index.html`, is
embedded in the binary and served by `nim console` over **loopback only**. It
is an observability view of the local journal, deliberately read-only and
deliberately unreachable from a network interface. It is not a website and
cannot be hosted.

## Why `vercel.json` disables git deployments

The Vercel project `nim` (team `seergiiim`) is connected to this GitHub
repository with the framework preset **Next.js**. There is no Next.js app here,
so every push produced a failed build:

```
Error: No Next.js version detected. Make sure your package.json has "next" in
either "dependencies" or "devDependencies". Also check your Root Directory
setting matches the directory of your package.json file.
```

Fourteen deployments failed that way before this file existed, including the
Production build for the M1–M3 merge. A red deployment on every commit is
noise that trains people to ignore deployment status, which is the same failure
mode as a verification check that cries wolf.

`"git": { "deploymentEnabled": false }` stops Vercel building this repository
on push. It does not delete the project, touch the existing domains, or affect
the one deployment that is live.

## What is actually deployed today

`nim-seergiiim.vercel.app` serves a page generated with **v0** and pushed from
the Vercel CLI on 2026-08-02. Its source lives in a v0 chat, not in this
repository or any other — it has never been in version control. It is a
scaffold page, not a Nim dashboard.

## When a dashboard does exist

It is **M8**, and `docs/milestones.md` records why it is blocked on something
more basic than itself: syncing a record whose authenticity rests on an unkeyed
chain exports a liability rather than evidence. That decision comes first.

When it is built, re-enabling deployment means:

1. Delete `vercel.json`, or set `deploymentEnabled` to the branches that should
   deploy.
2. In the Vercel project, set **Root Directory** to wherever the app lives
   (`web/`, per the shape README describes), so the Next.js preset finds a
   `package.json`.
3. Set the Production Branch to the branch that should serve production.

Until then, the honest state is that Nim has no web deployment, and the sync
contract in `docs/milestones.md` sets out what such a dashboard would and would
not be allowed to receive.
