import { tone, label, type Evidence, type Provenance } from "@/lib/state";

/** A status word, coloured by the one table in lib/state that decides tone. */
export function Status({ value }: { value: string }) {
  return <span className={`chip chip-${tone(value)}`}>{label(value)}</span>;
}

/**
 * Where a datum came from. Present on everything, deliberately: a page that
 * labels only its weak claims teaches readers that an unlabelled one is solid.
 */
export function Prov({ of }: { of: Provenance }) {
  return <span className="prov" title={PROV_MEANING[of]}>{of}</span>;
}

const PROV_MEANING: Record<Provenance, string> = {
  derived: "Computed from the repository at build time.",
  declared: "Written by hand in dashboard/data/state.json; every test and file it cites is checked to exist at build time.",
  ci: "A snapshot of a CI run, carrying its own commit and timestamp. Not live.",
  runtime: "Only knowable on a machine running Nim. Never available to this page.",
  unknown: "Not determined.",
};

export function Field({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="field">
      <span className="k">{k}</span>
      <div className="v">{children}</div>
    </div>
  );
}

/** The half of a guarantee that is not the guarantee. */
export function Limitation({ children }: { children: React.ReactNode }) {
  return (
    <div className="field limitation">
      <span className="k">Not guaranteed</span>
      <div className="v">{children}</div>
    </div>
  );
}

export function Tests({ evidence }: { evidence: Evidence[] }) {
  if (evidence.length === 0) {
    return (
      <Field k="Tests">
        <em>None. Nothing in the suite establishes this.</em>
      </Field>
    );
  }
  return (
    <Field k={`Tests (${evidence.length}, each verified to exist at build time)`}>
      <div className="tests">
        {evidence.map((e) => (
          <span key={e.name} className="t" title={`${e.file}${e.platform === "all" ? "" : ` — ${e.platform} only`}`}>
            {e.name}
            {e.platform !== "all" && <span style={{ opacity: 0.6 }}> · {e.platform}</span>}
          </span>
        ))}
      </div>
    </Field>
  );
}

export function Section({
  id, title, count, lede, children,
}: {
  id: string; title: string; count?: string; lede?: React.ReactNode; children: React.ReactNode;
}) {
  return (
    <section id={id}>
      <div className="section-head">
        <h2>{title}</h2>
        {count && <span className="count">{count}</span>}
      </div>
      {lede && <div className="lede">{lede}</div>}
      {children}
    </section>
  );
}

export function Stat({ k, v, sub, small }: { k: string; v: string | number; sub?: string; small?: boolean }) {
  return (
    <div className="card stat">
      <span className="k">{k}</span>
      <div className={`v${small ? " small" : ""}`}>{v}</div>
      {sub && <div className="sub">{sub}</div>}
    </div>
  );
}
