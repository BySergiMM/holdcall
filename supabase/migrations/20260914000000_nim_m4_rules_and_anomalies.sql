-- Applied to nothing yet. Holdcall's mirror has never received a row (M8 is blocked
-- on D-004), and this file exists so the mirror's schema does not fall behind
-- the local one it would copy from.
--
-- M4 adds two entry kinds and two anomaly kinds to the local journal:
--
--   rule.add / rule.remove   a policy change, in the chain like a call is
--   duplicate_key            an object naming a key twice, refused by the relay
--   unreadable_call          a tools/call with no readable tool name, refused
--
-- Locally the table was rebuilt, because SQLite cannot widen a CHECK in
-- place. Postgres can.

alter table public.nim_journal
    drop constraint if exists nim_journal_kind_check;
alter table public.nim_journal
    add constraint nim_journal_kind_check check (kind in
        ('session.start','call.request','call.outcome','session.end','anomaly',
         'rule.add','rule.remove'));

alter table public.nim_journal
    drop constraint if exists nim_journal_anomaly_check;
alter table public.nim_journal
    add constraint nim_journal_anomaly_check check (anomaly is null or anomaly in
        ('batch','malformed_json','framing','duplicate_id','duplicate_key','unreadable_call'));

-- The agent the daemon derived a session from, hashed into session.start at
-- schema_version 2, and the scope of a rule on rule.add / rule.remove.
alter table public.nim_journal add column if not exists agent text;

comment on column public.nim_journal.agent is
    'Enrolled client program the daemon derived from the kernel; never sent by the relay. On rule entries, the rule''s agent scope.';
comment on column public.nim_journal.session_id is
    'The session an entry belongs to. Empty string, not null, on rule.add and rule.remove: a policy change belongs to no session.';
