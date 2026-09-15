-- Applied to nothing yet. The mirror has never received a row (M8 is blocked
-- on D-004); this keeps its schema in step with the local journal it would
-- copy from.
--
-- Since 2026-09-15 the local journal writes schema_version 3: enrolment
-- changes are entries (agent.add / agent.remove) carrying the enrolled path
-- and the resolved identity, and a rule entry's decision may be 'allow' as
-- well as 'deny' now that rules can grant (M4.5).

alter table public.nim_journal
    drop constraint if exists nim_journal_kind_check;
alter table public.nim_journal
    add constraint nim_journal_kind_check check (kind in
        ('session.start','call.request','call.outcome','session.end','anomaly',
         'rule.add','rule.remove','agent.add','agent.remove'));

alter table public.nim_journal add column if not exists exec_path text;
alter table public.nim_journal add column if not exists exec_id   text;

comment on column public.nim_journal.exec_path is
    'On agent.add / agent.remove: the path the operator enrolled. Diagnostic; never what an identity is matched on.';
comment on column public.nim_journal.exec_id is
    'On agent.add / agent.remove: the resolved executable identity as <dev>:<ino>, decimal.';
