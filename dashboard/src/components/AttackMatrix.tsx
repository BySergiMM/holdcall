"use client";

import { useMemo, useState } from "react";
import { state, tone, label } from "@/lib/state";

const ORDER = { fail: 0, not_tested: 1, partial: 2, pass: 3, not_applicable: 4 } as const;

/**
 * The BREAK HOLDCALL matrix.
 *
 * Sorted worst-first by default, and it stays that way: an attack table that
 * opens on its successes is a marketing page. The filter can hide rows, so the
 * header always states the true total alongside what is shown.
 */
export default function AttackMatrix() {
  const [filter, setFilter] = useState<string>("all");

  const rows = useMemo(() => {
    const all = [...state.attacks].sort(
      (a, b) => ORDER[a.status] - ORDER[b.status] || a.name.localeCompare(b.name)
    );
    return filter === "all" ? all : all.filter((a) => a.status === filter);
  }, [filter]);

  const counts = state.summary.attacks;
  const buttons: [string, string, number][] = [
    ["all", "all", counts.total],
    ["fail", "not defended", counts.fail],
    ["not_tested", "not tested", counts.notTested],
    ["partial", "partial", counts.partial],
    ["pass", "defended", counts.pass],
    ["not_applicable", "n/a", counts.notApplicable],
  ];

  return (
    <>
      <div style={{ display: "flex", flexWrap: "wrap", gap: "0.35rem", marginBottom: "0.9rem" }}>
        {buttons.map(([key, text, n]) => (
          <button
            key={key}
            onClick={() => setFilter(key)}
            aria-pressed={filter === key}
            style={{
              font: "inherit",
              fontFamily: "var(--mono)",
              fontSize: 11,
              cursor: "pointer",
              padding: "0.2rem 0.5rem",
              borderRadius: "var(--radius)",
              border: `1px solid ${filter === key ? "var(--accent)" : "var(--border)"}`,
              background: filter === key ? "var(--accent-soft)" : "var(--surface)",
              color: filter === key ? "var(--accent)" : "var(--text-2)",
            }}
          >
            {text} <span style={{ opacity: 0.65 }}>{n}</span>
          </button>
        ))}
      </div>

      {filter !== "all" && (
        <p style={{ fontSize: 12, color: "var(--text-3)", marginBottom: "0.6rem" }}>
          Showing {rows.length} of {counts.total}. {counts.total - rows.length} rows are hidden by this filter.
        </p>
      )}

      {rows.map((a) => (
        <details key={a.id} className={`item sev-${tone(a.status)}`}>
          <summary>
            <span className="title">{a.name}</span>
            {a.wasLive && (
              <span className="chip chip-bad" title="This attack was reproduced against a real build before it was addressed.">
                was live
              </span>
            )}
            <span className="prov">{a.category}</span>
            <span className={`chip chip-${tone(a.status)}`}>{label(a.status)}</span>
          </summary>
          <div className="body">
            <div className="field">
              <span className="k">How it stands</span>
              <div className="v">{a.evidence}</div>
            </div>
            {a.evidenceTests.length > 0 ? (
              <div className="field">
                <span className="k">Tests</span>
                <div className="tests">
                  {a.evidenceTests.map((e) => (
                    <span key={e.name} className="t" title={e.file}>
                      {e.name}
                    </span>
                  ))}
                </div>
              </div>
            ) : (
              <div className="field">
                <span className="k">Tests</span>
                <div className="v">
                  <em>None. This row is a written assessment, not a tested one.</em>
                </div>
              </div>
            )}
          </div>
        </details>
      ))}
    </>
  );
}
