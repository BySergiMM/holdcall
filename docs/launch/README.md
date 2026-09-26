# Launch material

Texts and assets for announcing Holdcall, kept here so they are versioned
with the claims they make. Nothing in this directory is published by the
site or the dashboard. Every number and every claim below must be true of
the tagged release it is posted for; when the docs change, this changes.

## Before posting anything

- [x] Make the repository public (done 2026-09-26; every link below was
      fetched without a session afterwards).
- [x] Tag the release the post names (`v0.1.1`, built by the workflow on
      2026-09-26; `install.sh` from the raw URL installed it and it reported
      its version).
- [ ] Re-read `docs/milestones.md`: the platform claims (macOS executed,
      Linux and Windows compiled and never run) must match the text.
- [ ] Open https://holdcall.vercel.app in a private window: title, image,
      form.
- [ ] Decide the domain. Posts below use the vercel.app URL on purpose.

## Show HN

**Title** (79 characters max; this one is 74):

    Show HN: Holdcall – a local relay that holds an AI agent's tool call for you

**Body:**

Holdcall sits between an MCP client (Claude Code, Cursor, Claude Desktop)
and the MCP servers it talks to. It runs on your machine, needs no
network, and decides every tool call before it reaches the server: allow,
deny, or hold it for a human. A held call shows you the real arguments,
every byte, and waits for `holdcall approve` or `holdcall reject`. Nobody
deciding within the timeout is a rejection, and the model is told a human
said no, so it does not retry.

Everything it sees goes into an append-only SQLite journal, hash-chained,
that you can verify offline with `holdcall verify`; the format is
documented so a second program can check it. Credentials never sit in the
client's config file: they live in the OS keychain, bound to the one
command they may be injected into. The agent's identity comes from the
executable that spawned the relay, as the kernel reports it, not from
anything on the wire.

It is a single Go binary. `holdcall init` rewrites your client's config so
every stdio server goes through it, with backups. There is a read-only
console on loopback for the journal, the held calls and the policy, and a
public status page where every claim links to the test that proves it.

What it does not do, so you do not have to find out: the chain is unkeyed,
so anyone who can write the journal file can recompute it; it has only
ever run on macOS, Linux and Windows are compiled but never executed; and
rules match tool names, not what a tool does with its arguments.

I built it because the first time an agent force-pushed for me I realised
I had approved nothing, and I had no record. Source and the design notes,
including the findings against it, are in the repository.

https://github.com/BySergiMM/holdcall — https://holdcall.vercel.app

## X / Twitter

Single post, 280 characters or fewer (this one is 268):

    An AI agent asked to force-push. Holdcall held the call, showed me the
    real arguments, and waited. I said no; the model was told a human said
    no. Every call it sees goes into a hash-chained journal you verify
    offline. Local, one Go binary, no network. https://holdcall.vercel.app

Thread, if it lands:

1. Holdcall sits between your MCP client and its servers. Every tool call is
   decided where it happens: allow, deny, or hold for a human. No network.
2. A held call waits with its real arguments on screen, every byte. Approve
   or reject from the CLI. Nobody deciding is a rejection, and the model is
   told so.
3. The record is an append-only SQLite journal, hash-chained. `holdcall
   verify` walks it offline. The format is documented for a second checker.
4. What it does not promise: the chain is unkeyed, it has only ever run on
   macOS, and rules match names, not intent. The status page says which
   test proves what.
5. https://github.com/BySergiMM/holdcall

## LinkedIn (Spanish)

He publicado Holdcall, una herramienta local para quien deja que un agente
de IA use herramientas de verdad (GitHub, ficheros, despliegues) a través
de MCP.

Se pone entre el cliente (Claude Code, Cursor, Claude Desktop) y los
servidores MCP y decide cada llamada antes de que llegue: permitir,
denegar, o retenerla para que la mire una persona. Una llamada retenida
muestra los argumentos reales y espera; si nadie decide, se rechaza, y el
modelo recibe que un humano ha dicho que no.

Todo lo que ve va a un registro encadenado por hash que se verifica sin
red. Las credenciales no están en el fichero de configuración del cliente,
sino en el llavero del sistema, atadas al único comando que puede
recibirlas.

Un binario en Go, sin cuenta, sin red. Lo que no promete está escrito en
la misma página que lo que sí: https://holdcall.vercel.app

## Assets

- `docs/images/console.gif`: overview, a call arrives held, its arguments,
  the journal after the rejection. Made from `tools/console-demo`.
- `docs/images/console-held.png`, `docs/images/console.png`: stills.
- `dashboard/public/og.png`: the card that link previews show.
