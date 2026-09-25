-- Applied to project rsysotrzwipxosuwndux (shared; Holdcall is a tenant there).
--
-- The journal became append-only and hash-chained, so the mirror follows: one
-- table of immutable entries, with nim_sessions and nim_calls as views over it.
-- A call is two entries -- request and outcome -- because a row that gets
-- updated cannot be part of a chain.
--
-- Still a READ MIRROR. Holdcall decides locally and always; nothing here
-- participates in an authorization decision.
-- Contract: never credentials, never call parameters, never response bodies.
--
-- Nothing writes to this yet: the sync that would fill it is a later milestone.
-- The schema lands now because the previous one could not have accepted a
-- single local row -- see the id note below.

-- The M1 tables are moved aside rather than dropped. Their rows were updated in
-- place, so they cannot be folded into a chain: hashing them now would assert
-- an integrity they never had. Drop them by hand once you are sure they are
-- empty, which they should be -- nothing has ever synced.
alter table if exists public.nim_sessions rename to nim_sessions_m1;
alter table if exists public.nim_calls    rename to nim_calls_m1;

create table if not exists public.nim_journal (
    -- text, not uuid. The identifier a chain entry naturally has is its
    -- position, and the local id for a call is (session_id, seq). The previous
    -- schema declared uuid while the engine produced "<hex>-<n>", which is not
    -- a valid uuid: the first row ever synced would have been rejected.
    machine_id       text        not null,
    chain_seq        bigint      not null,

    schema_version   integer     not null,
    kind             text        not null
                       check (kind in ('session.start','call.request','call.outcome','session.end','anomaly')),
    session_id       text        not null,
    seq              integer     check (seq is null or seq >= 0),
    connector        text,
    tool             text,
    params_digest    text,
    decision         text        check (decision is null or decision in
                       ('observed','allow','deny','approved','rejected')),
    ok               boolean,
    duration_ms      integer,
    anomaly          text        check (anomaly is null or anomaly in
                       ('batch','malformed_json','framing','duplicate_id')),
    occurred_at      timestamptz not null,
    machine_client   text,
    protocol_version text,
    prev_hash        text        not null,
    hash             text        not null,
    synced_at        timestamptz not null default now(),

    -- One chain per install, so entries from different machines never collide
    -- and re-syncing the same entry is idempotent.
    primary key (machine_id, chain_seq)
);

comment on table  public.nim_journal is
    'Holdcall: append-only, hash-chained record of what agents did, mirrored from the local journal.';
comment on column public.nim_journal.machine_id is
    'Opaque per-install id. Not a user, not a hostname. Also seeds the chain, so it scopes the primary key.';
comment on column public.nim_journal.params_digest is
    'sha256 of the arguments as they arrived. The arguments themselves never leave the machine.';
comment on column public.nim_journal.decision is
    'Only ever "observed" today: nothing is authorized yet. The rest of the vocabulary is reserved so it need not change later.';
comment on column public.nim_journal.hash is
    'Chain link. Detects corruption and edits that did not recompute the chain; it does not detect anyone who can write the source journal.';
comment on column public.nim_journal.machine_client is
    'Label the MCP client reported for itself. Self-asserted, so never a subject of policy.';

create index if not exists nim_journal_session_idx     on public.nim_journal (session_id, seq);
create index if not exists nim_journal_occurred_at_idx on public.nim_journal (occurred_at desc);
create index if not exists nim_journal_kind_idx        on public.nim_journal (kind);

-- The two views mirror the local ones, so a reader sees the same shape here as
-- in SQLite.
create or replace view public.nim_sessions as
select
    s.machine_id,
    s.session_id                      as id,
    s.machine_client                  as client,
    s.connector,
    s.occurred_at                     as started_at,
    (select e.occurred_at from public.nim_journal e
      where e.kind = 'session.end'
        and e.machine_id = s.machine_id
        and e.session_id = s.session_id
      order by e.chain_seq limit 1)   as ended_at
from public.nim_journal s
where s.kind = 'session.start';

create or replace view public.nim_calls as
select
    r.machine_id,
    r.session_id || '-' || r.seq as id,
    r.session_id,
    r.seq,
    s.connector,
    r.tool,
    r.params_digest,
    r.decision,
    o.ok,
    o.duration_ms,
    r.occurred_at
from public.nim_journal r
join public.nim_journal s
  on s.kind = 'session.start'
 and s.machine_id = r.machine_id
 and s.session_id = r.session_id
left join public.nim_journal o
  on o.kind = 'call.outcome'
 and o.machine_id = r.machine_id
 and o.session_id = r.session_id
 and o.seq = r.seq
where r.kind = 'call.request';

-- Locked by default: RLS on, no policies. Nothing reads this until ownership of
-- a machine_id is defined. The engine would write with the service role.
alter table public.nim_journal enable row level security;
