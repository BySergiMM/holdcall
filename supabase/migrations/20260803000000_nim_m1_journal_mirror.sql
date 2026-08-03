-- Applied to project rsysotrzwipxosuwndux (shared; Nim is a tenant there).
--
-- Nim shares this database with another product. Everything it owns is
-- prefixed nim_ so it can be lifted into a dedicated project later with a
-- schema-only dump filtered on that prefix.
--
-- These tables are a READ MIRROR of the local SQLite journal. Nim decides
-- locally and always; nothing here participates in an authorization decision.
-- Contract: never credentials, never call parameters, never response bodies.

create table if not exists public.nim_sessions (
    id           uuid primary key,
    machine_id   text        not null,
    client       text,
    target       text        not null,
    started_at   timestamptz not null,
    ended_at     timestamptz,
    synced_at    timestamptz not null default now()
);

comment on table  public.nim_sessions is 'Nim: one run of a shim against one downstream MCP server.';
comment on column public.nim_sessions.machine_id is 'Opaque per-install id. Not a user, not a hostname.';
comment on column public.nim_sessions.target     is 'Name of the downstream MCP server, e.g. "github".';

create table if not exists public.nim_calls (
    id            uuid primary key,
    session_id    uuid        not null references public.nim_sessions(id) on delete cascade,
    seq           integer     not null,
    tool          text        not null,
    params_digest text        not null,
    decision      text        not null,
    ok            boolean,
    duration_ms   integer,
    occurred_at   timestamptz not null,
    synced_at     timestamptz not null default now(),
    constraint nim_calls_seq_unique  check (seq >= 0),
    constraint nim_calls_decision_ck check (decision in ('allow', 'deny', 'approved', 'rejected')),
    unique (session_id, seq)
);

comment on table  public.nim_calls is 'Nim: one tools/call, as recorded by the local daemon.';
comment on column public.nim_calls.params_digest is 'sha256 of the arguments. The arguments themselves never leave the machine.';
comment on column public.nim_calls.seq           is 'Position in the local journal; makes the mirror idempotent to re-sync.';

create index if not exists nim_calls_session_seq_idx on public.nim_calls (session_id, seq);
create index if not exists nim_calls_occurred_at_idx on public.nim_calls (occurred_at desc);

-- Locked by default: RLS on, no policies. Nothing reads this until M8 defines
-- who owns a machine_id. The engine would write with the service role.
alter table public.nim_sessions enable row level security;
alter table public.nim_calls    enable row level security;
