import AttackMatrix from "@/components/AttackMatrix";
import { Field, Limitation, Prov, Section, Stat, Status, Tests } from "@/components/bits";
import { state, tone, label } from "@/lib/state";

const { project, derived, summary, ciSnapshot, runtime, architecture } = state;

const NAV = [
  ["overview", "Overview", ""],
  ["architecture", "Architecture", ""],
  ["guarantees", "Guarantees", `${summary.guarantees.total}`],
  ["break", "Break Nim", `${summary.attacks.total}`],
  ["findings", "Findings", `${summary.findings.total}`],
  ["milestones", "Milestones", `${summary.milestones.done}/${summary.milestones.total}`],
  ["decisions", "Open questions", `${summary.decisions.open}`],
  ["runtime", "Runtime", "—"],
  ["ci", "CI", ""],
  ["limits", "What this cannot tell you", ""],
];

export default function Page() {
  return (
    <div className="shell">
      <aside className="rail">
        <div className="rail-group">
          <div className="rail-title">Nim</div>
          <nav>
            {NAV.map(([id, text, n]) => (
              <a key={id} href={`#${id}`}>
                <span>{text}</span>
                {n && <span className="n">{n}</span>}
              </a>
            ))}
          </nav>
        </div>
      </aside>

      <main>
        <header className="masthead">
          <h1>{project.name} — Control Plane</h1>
          <p className="tag">{project.tagline}</p>
          <div className="meta">
            <span>
              <b>{derived.branch}</b> @ <b>{derived.commit}</b>
            </span>
            <span>{derived.commitSubject}</span>
            <span>{derived.commitDate.slice(0, 10)}</span>
            <span>
              <b>{derived.commitCount}</b> commits
            </span>
            <span>go {derived.goVersion}</span>
            {!derived.clean && (
              <span className="chip chip-warn" title="Uncommitted changes were present when this page was built.">
                tree dirty at build
              </span>
            )}
          </div>
          <div className="meta legend" style={{ marginTop: "1rem" }}>
            <span className="row">
              <Prov of="derived" /> read from the repository
            </span>
            <span className="row">
              <Prov of="declared" /> written by hand, references checked
            </span>
            <span className="row">
              <Prov of="ci" /> snapshot of a run
            </span>
            <span className="row">
              <Prov of="runtime" /> not knowable here
            </span>
          </div>
        </header>

        {/* ------------------------------------------------------- overview */}
        <Section id="overview" title="Overview">
          <div className="grid grid-3" style={{ marginBottom: "1rem" }}>
            <Stat k="Milestone" v={project.currentMilestone} sub={project.currentPhase} />
            <Stat
              k="Guarantees"
              v={`${summary.guarantees.verified} / ${summary.guarantees.total}`}
              sub={`${summary.guarantees.partial} partial · each backed by tests that exist`}
            />
            <Stat
              k="Attacks not defended"
              v={summary.attacks.fail}
              sub={`plus ${summary.attacks.partial} partial and ${summary.attacks.notTested} never tested, of ${summary.attacks.total}`}
            />
            <Stat
              k="Tests in the tree"
              v={derived.testCount}
              sub={`${derived.benchmarkCount} benchmarks · ${derived.packageCount} packages · ${derived.excludedFromTestCount} helper-process entry point not counted as a test`}
            />
            <Stat
              k="Open findings"
              v={summary.findings.open}
              sub={`${summary.findings.accepted} accepted · ${summary.findings.high} high severity`}
            />
            <Stat k="Unresolved questions" v={summary.decisions.open} sub="decisions that block later work" />
          </div>

          <div className="notice">
            <h3>
              Where this actually stands <Prov of="declared" />
            </h3>
            <p>{project.statusNote}</p>
            <p>
              There is no completion percentage on this page. The milestones after M4 have no deliverables written
              down, so any percentage would need a denominator nobody has chosen — a number invented to look like
              progress. The counts above are what can be counted.
            </p>
          </div>
        </Section>

        {/* --------------------------------------------------- architecture */}
        <Section
          id="architecture"
          title="Architecture"
          lede="Where Nim sits, and what each layer does or does not yet do."
        >
          <div className="flow" style={{ marginBottom: "1.5rem" }}>
            {architecture.nodes.map((n, i) => (
              <div key={n.id} style={{ display: "contents" }}>
                {i > 0 && <div className="arrow">→</div>}
                <div className="node">
                  <div className="l">{n.label}</div>
                  <div className="s">{n.sub}</div>
                  <div className="n">{n.note}</div>
                </div>
              </div>
            ))}
          </div>

          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Layer</th>
                  <th>State</th>
                  <th>What that means today</th>
                </tr>
              </thead>
              <tbody>
                {architecture.layers.map((l) => (
                  <tr key={l.name}>
                    <td style={{ fontWeight: 500, whiteSpace: "nowrap" }}>{l.name}</td>
                    <td>
                      <Status value={l.state} />
                    </td>
                    <td style={{ color: "var(--text-2)" }}>{l.detail}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Section>

        {/* ----------------------------------------------------- guarantees */}
        <Section
          id="guarantees"
          title="Guarantees"
          count={`${summary.guarantees.total} claims · ${summary.guarantees.verified} verified`}
          lede={
            <>
              Every entry states what is guaranteed <em>and</em> what is not, because a security property read without
              its boundary is worse than no property at all. The build fails if any of these cites a test that does not
              exist in the repository.
            </>
          }
        >
          {state.guarantees.map((g) => (
            <details key={g.id} className={`item sev-${tone(g.status)}`}>
              <summary>
                <span className="title">{g.title}</span>
                <Prov of={g.provenance} />
                <Status value={g.status} />
              </summary>
              <div className="body">
                <Field k="Guaranteed">{g.statement}</Field>
                <Limitation>{g.notGuaranteed}</Limitation>
                <Field k="Platforms">
                  <div style={{ display: "flex", gap: "0.4rem", flexWrap: "wrap" }}>
                    {Object.entries(g.platforms).map(([p, v]) => (
                      <span key={p} className={`chip chip-${tone(v)}`}>
                        {p}: {label(v)}
                      </span>
                    ))}
                  </div>
                </Field>
                <Tests evidence={g.evidenceTests} />
                <Field k="Implementation">
                  <div className="tests">
                    {g.implementation.map((f) => (
                      <span key={f} className="t">
                        {f}
                      </span>
                    ))}
                  </div>
                </Field>
              </div>
            </details>
          ))}
        </Section>

        {/* ---------------------------------------------------------- break */}
        <Section
          id="break"
          title="Break Nim"
          count={`${summary.attacks.total} attempts · ${summary.attacks.liveBefore} once worked`}
          lede={
            <>
              Not a list of wins. Every row is an attempt to make Nim fail, and{" "}
              <strong>{summary.attacks.liveBefore} of them succeeded against a real build</strong> before they were
              addressed. Rows sort worst-first. A row with no test is a written assessment, and says so.
              <br />
              <br />
              These statuses describe <strong>macOS and Linux</strong>. Nim has never been executed on Windows, and has
              no peer verification there at all (F-002) — so every row in the <span className="prov">identity</span>{" "}
              category should be read as undefended on that platform.
            </>
          }
        >
          <AttackMatrix />
        </Section>

        {/* ------------------------------------------------------- findings */}
        <Section
          id="findings"
          title="Findings"
          count={`${summary.findings.open} open · ${summary.findings.accepted} accepted`}
          lede="Known weaknesses. Accepted means understood and deliberately not fixed yet — not fixed."
        >
          {state.findings.map((f) => (
            <details key={f.id} className={`item sev-${tone(f.severity)}`}>
              <summary>
                <span className="mono" style={{ color: "var(--text-3)" }}>
                  {f.id}
                </span>
                <span className="title">{f.title}</span>
                <Status value={f.severity} />
                <Status value={f.status} />
              </summary>
              <div className="body">
                <Field k="Problem">{f.problem}</Field>
                <Field k="Impact">{f.impact}</Field>
                <Field k="Evidence">{f.evidence}</Field>
                <Field k="Next action">{f.nextAction}</Field>
              </div>
            </details>
          ))}
        </Section>

        {/* ------------------------------------------------------ milestones */}
        <Section
          id="milestones"
          title="Milestones"
          count={`${summary.milestones.done} done · ${summary.milestones.inProgress} in progress · ${summary.milestones.blocked} blocked`}
          lede="What each milestone delivered, and what it deliberately left behind."
        >
          {state.milestones.map((m) => (
            <details key={m.id} className={`item sev-${tone(m.status)}`} open={m.status === "in_progress"}>
              <summary>
                <span className="mono" style={{ color: "var(--text-3)", minWidth: "2.6rem" }}>
                  {m.id}
                </span>
                <span className="title">{m.name}</span>
                <Status value={m.status} />
              </summary>
              <div className="body">
                <Field k="Objective">{m.objective}</Field>
                {m.deliverables.length > 0 && (
                  <Field k="Delivered">
                    <div className="tests">
                      {m.deliverables.map((d) => (
                        <span key={d} className="t">
                          {d}
                        </span>
                      ))}
                    </div>
                  </Field>
                )}
                {m.limitations.length > 0 && (
                  <Limitation>
                    <ul style={{ margin: 0, paddingLeft: "1.1rem" }}>
                      {m.limitations.map((l) => (
                        <li key={l}>{l}</li>
                      ))}
                    </ul>
                  </Limitation>
                )}
                {m.pending.length > 0 && (
                  <Field k="Still outstanding">
                    <ul style={{ margin: 0, paddingLeft: "1.1rem" }}>
                      {m.pending.map((p) => (
                        <li key={p}>{p}</li>
                      ))}
                    </ul>
                  </Field>
                )}
              </div>
            </details>
          ))}
        </Section>

        {/* ------------------------------------------------------- decisions */}
        <Section
          id="decisions"
          title="Open questions"
          count={`${summary.decisions.open} of ${summary.decisions.total} unresolved`}
          lede="Decisions that cannot be made by writing code, and that later work depends on."
        >
          {state.decisions.map((d) => (
            <details key={d.id} className={`item sev-${tone(d.status)}`}>
              <summary>
                <span className="mono" style={{ color: "var(--text-3)" }}>
                  {d.id}
                </span>
                <span className="title">{d.title}</span>
                <Status value={d.status} />
              </summary>
              <div className="body">
                <Field k="Question">{d.question}</Field>
                <Field k={d.status === "resolved" ? "Decided" : "Where it stands"}>{d.resolution}</Field>
              </div>
            </details>
          ))}
        </Section>

        {/* --------------------------------------------------------- runtime */}
        <Section id="runtime" title="Runtime state">
          <div className="notice hard">
            <h3>
              Not available <span className="chip chip-none">no data</span> <Prov of="runtime" />
            </h3>
            <p>{runtime.reason}</p>
            <p>
              This section is empty on purpose and will stay empty. Live sessions, journal entries, enrolled agents and
              daemon health are not shown here because this page cannot see them — and a page that filled the gap with
              plausible numbers would be the exact failure mode the rest of the dashboard exists to avoid.
            </p>
            <div className="table-wrap" style={{ marginTop: "0.4rem" }}>
              <table>
                <thead>
                  <tr>
                    <th>Runtime datum</th>
                    <th>Here</th>
                    <th>Where it actually lives</th>
                  </tr>
                </thead>
                <tbody>
                  {[
                    ["Daemon up / down", "nim status"],
                    ["Journal entries and chain head", "nim log, nim verify"],
                    ["Journal integrity right now", "nim verify --expect-head"],
                    ["Enrolled agents", "nim agent list"],
                    ["Registered connectors", "nim connector list"],
                    ["Calls allowed and refused", "nim log"],
                  ].map(([what, where]) => (
                    <tr key={what}>
                      <td>{what}</td>
                      <td>
                        <span className="chip chip-none">not available</span>
                      </td>
                      <td className="mono" style={{ color: "var(--text-2)" }}>
                        {where}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </Section>

        {/* -------------------------------------------------------------- ci */}
        <Section id="ci" title="Continuous integration">
          <div className="notice" style={{ marginBottom: "1rem" }}>
            <h3>
              {ciSnapshot.workflow} <Status value={ciSnapshot.conclusion} />
              {ciSnapshot.stale ? (
                <span className="chip chip-warn">stale — ran on {ciSnapshot.commit}, this page is {derived.commit}</span>
              ) : (
                <span className="chip chip-ok">same commit as this page</span>
              )}
              <Prov of="ci" />
            </h3>
            <p>
              A snapshot taken at {ciSnapshot.takenAt}, not a live status. Nothing on this page polls GitHub; a green
              badge that had gone red hours ago would be worse than none.
            </p>
            <p>
              Recording a run means committing, and that commit is one CI has not yet run — so a hand-recorded snapshot
              is <em>structurally</em> at least one commit behind, and will often read as stale. That is the honest
              state, not a bug to chase: it says CI was green at the named commit and has not yet spoken about this one.
              Anything that made this box always look current would be doing so by not checking.
            </p>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Job</th>
                  <th>Result</th>
                </tr>
              </thead>
              <tbody>
                {ciSnapshot.jobs.map((j) => (
                  <tr key={j.name}>
                    <td className="mono">{j.name}</td>
                    <td>
                      <Status value={j.conclusion} />
                      {j.note && (
                        <div style={{ fontSize: 12, color: "var(--text-2)", marginTop: "0.3rem", maxWidth: "48ch" }}>
                          {j.note}
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p style={{ marginTop: "0.7rem", fontSize: 13, color: "var(--text-3)" }}>
            Cross-compile jobs build only — they do not run tests or <code>go vet</code>, which is finding F-009. And CI
            does not run with <code>-v</code>, so a skipped adversarial test is indistinguishable from a passing one in
            the log (F-010).
          </p>
        </Section>

        {/* ---------------------------------------------------------- limits */}
        <Section id="limits" title="What this dashboard cannot tell you">
          <div className="grid grid-2">
            <div className="card">
              <h3 style={{ fontSize: "0.92rem", marginBottom: "0.5rem" }}>The check has a hard edge</h3>
              <p style={{ color: "var(--text-2)", fontSize: 14 }}>
                The build fails if a guarantee or attack cites a test that is not in the repository — that is how the
                nine claims this page was first written with got corrected. It proves a cited test <em>exists</em>. It
                cannot prove the test establishes the sentence printed next to it. A wrong summary beside a real test
                name would pass.
              </p>
            </div>
            <div className="card">
              <h3 style={{ fontSize: "0.92rem", marginBottom: "0.5rem" }}>Nothing here is live</h3>
              <p style={{ color: "var(--text-2)", fontSize: 14 }}>
                Every value is baked in at build time. The commit and test counts are true of{" "}
                <span className="mono">{derived.commit}</span> and of nothing else; the CI block is a snapshot with its
                own timestamp. If the page is older than the tree, it is wrong, and it tells you which commit it
                describes so you can find out.
              </p>
            </div>
            <div className="card">
              <h3 style={{ fontSize: "0.92rem", marginBottom: "0.5rem" }}>&quot;Verified&quot; means a test passes</h3>
              <p style={{ color: "var(--text-2)", fontSize: 14 }}>
                It does not mean audited, reviewed by anyone outside this project, or proven. No third party has looked
                at Nim. The tests were written by the same process that wrote the code they check.
              </p>
            </div>
            <div className="card">
              <h3 style={{ fontSize: "0.92rem", marginBottom: "0.5rem" }}>The journal chain is not authenticity</h3>
              <p style={{ color: "var(--text-2)", fontSize: 14 }}>
                Wherever this page says the journal detects alteration, it means an <em>unkeyed</em> hash chain. Anyone
                who can write the database can recompute every hash and the result verifies cleanly. That is F-003, and
                it is why M8 is blocked rather than merely unstarted.
              </p>
            </div>
            <div className="card">
              <h3 style={{ fontSize: "0.92rem", marginBottom: "0.5rem" }}>Windows is unrun</h3>
              <p style={{ color: "var(--text-2)", fontSize: 14 }}>
                Nim cross-compiles for Windows and has never been executed on it. Where a platform column says
                &quot;unsupported&quot;, that is a deliberate stub returning an error, not a gap someone forgot.
              </p>
            </div>
            <div className="card">
              <h3 style={{ fontSize: "0.92rem", marginBottom: "0.5rem" }}>Platform columns are weakly checked</h3>
              <p style={{ color: "var(--text-2)", fontSize: 14 }}>
                The build refuses a platform marked verified when every cited test is build-tagged away from it. It
                cannot refuse a test that <em>compiles</em> on a platform without establishing anything there — the
                path-swap test compiles on Windows and proves nothing on it. Those columns rest on judgement, not on
                the check.
              </p>
            </div>
            <div className="card">
              <h3 style={{ fontSize: "0.92rem", marginBottom: "0.5rem" }}>Absence of an attack is not safety</h3>
              <p style={{ color: "var(--text-2)", fontSize: 14 }}>
                The Break Nim table lists {summary.attacks.total} attempts someone thought of. It is not the set of
                attacks that exist. Rows are added when a new one is imagined, which means the table grows when Nim gets
                more scrutiny, not when it gets worse. The same applies upward: the {summary.guarantees.total}{" "}
                guarantees are the properties someone chose to write down, so a property nobody listed cannot appear
                here as unverified.
              </p>
            </div>
          </div>
        </Section>

        <footer>
          <div>
            Generated from <span className="mono">{derived.commit}</span> — {derived.goFileCount} Go files,{" "}
            {derived.testCount} tests, {derived.packageCount} packages. Static page: no server, no API, no runtime data,
            no secrets.
          </div>
          <div>
            Source of the declared half: <span className="mono">dashboard/data/state.json</span>. Checked by{" "}
            <span className="mono">dashboard/scripts/generate.mjs</span>, which exits non-zero rather than publish a
            claim whose evidence it cannot find.
          </div>
        </footer>
      </main>
    </div>
  );
}
