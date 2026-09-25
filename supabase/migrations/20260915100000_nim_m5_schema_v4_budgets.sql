-- Applied to nothing yet (M8 is blocked on D-004). Keeps the mirror's schema
-- in step with what the local journal writes since M5 (2026-09-15):
-- schema_version 4 adds budget_calls, carried by the two new kinds that
-- record a budget change. A budget itself lives outside the chain, in
-- nim_budgets locally, and is not mirrored: the chain entries are its record.

alter table public.nim_journal
    drop constraint if exists nim_journal_kind_check;
alter table public.nim_journal
    add constraint nim_journal_kind_check check (kind in
        ('session.start','call.request','call.outcome','session.end','anomaly',
         'rule.add','rule.remove','agent.add','agent.remove',
         'budget.add','budget.remove'));

alter table public.nim_journal add column if not exists budget_calls bigint;

comment on column public.nim_journal.budget_calls is
    'On budget.add / budget.remove: the cap being set or removed -- the number of allowed calls one session may make in the scope the entry''s agent, connector and tool carry. Null on every other kind.';
