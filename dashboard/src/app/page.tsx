import Waitlist from "@/components/Waitlist";
import { state } from "@/lib/state";
import "./landing.css";

const { summary, derived } = state;

// A real session, recorded by Holdcall on 2026-09-25 against the relay rig's
// server: every row below is a line of `holdcall log`, oldest first. Nothing here
// is invented, which is the only reason it belongs on the front page.
const LEDGER = [
  { n: 6, at: "20:29:45", tool: "add", decision: "allow", what: "forwarded, answered in 63 ms" },
  { n: 8, at: "20:29:45", tool: "echo", decision: "allow", what: "forwarded, answered in 1 ms" },
  { n: 10, at: "20:29:45", tool: "explode", decision: "deny", what: "a rule denies this tool; the server never saw it" },
  { n: 11, at: "20:29:45", tool: "add", decision: "allow", what: "forwarded" },
  { n: 13, at: "20:29:45", tool: "add", decision: "allow", what: "forwarded, third of three allowed" },
  { n: 15, at: "20:29:45", tool: "add", decision: "deny", what: "budget of 3 calls spent this session" },
  { n: 16, at: "20:29:47", tool: "dangerous_tool", decision: "rejected", what: "held for a human, who read the arguments and said no" },
] as const;

const DECISION_LABEL: Record<string, string> = {
  allow: "allow",
  deny: "deny",
  rejected: "rejected",
};

// Structured data for the product page: what it is, that it costs nothing
// for one person, and where it runs. Nothing here that the page does not
// already say in prose.
const JSON_LD = {
  "@context": "https://schema.org",
  "@type": "SoftwareApplication",
  name: "Holdcall",
  applicationCategory: "DeveloperApplication",
  operatingSystem: "macOS",
  description:
    "A local relay that decides every MCP tool call on your machine, holds the dangerous ones for a human who sees the real arguments, and keeps a record you can verify offline.",
  url: "https://holdcall.vercel.app/",
  offers: { "@type": "Offer", price: "0", priceCurrency: "EUR", description: "Free for one person and one machine" },
};

export default function Landing() {
  return (
    <div className="lp">
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: JSON.stringify(JSON_LD) }} />
      <header className="lp-top">
        <a className="lp-wordmark" href="/" aria-label="Holdcall, home">
          holdcall
        </a>
        <nav className="lp-nav" aria-label="Site">
          <a href="#how">How it works</a>
          <a href="#honest">What it does not promise</a>
          <a href="/status/">Status</a>
          <a className="lp-nav-cta" href="#access">
            Early access
          </a>
        </nav>
      </header>

      <main>
        <section className="lp-hero" aria-labelledby="lp-title">
          <p className="lp-eyebrow">For teams running agents with MCP tools</p>
          <h1 id="lp-title">The control that stays on your machine.</h1>
          <p className="lp-lede">
            Holdcall sits between your agent and its MCP servers. It decides every tool call where the call happens, shows
            you the real arguments before anything dangerous runs, and keeps a record you can verify offline. The
            arguments never leave the machine. Neither does the decision.
          </p>
          <div className="lp-actions">
            <a className="lp-btn lp-btn-primary" href="#access">
              Get early access
            </a>
            <a className="lp-btn" href="/status/">
              See what is verified
            </a>
          </div>

          <figure className="lp-ledger" aria-label="A real session recorded by Holdcall">
            <figcaption>
              <span className="lp-ledger-title">holdcall log</span>
              <span className="lp-ledger-sub">
                one agent, one connector, seven calls, 25 September 2026. Nothing here is a mock-up.
              </span>
            </figcaption>
            <div className="lp-ledger-scroll">
              <table>
                <thead>
                  <tr>
                    <th scope="col">#</th>
                    <th scope="col">when</th>
                    <th scope="col">agent</th>
                    <th scope="col">connector</th>
                    <th scope="col">tool</th>
                    <th scope="col">decision</th>
                    <th scope="col">what happened</th>
                  </tr>
                </thead>
                <tbody>
                  {LEDGER.map((row, i) => (
                    <tr key={row.n} className="lp-row" style={{ ["--i" as string]: i }}>
                      <td className="lp-dim">{row.n}</td>
                      <td className="lp-dim">{row.at}</td>
                      <td>claude-code</td>
                      <td>github</td>
                      <td>{row.tool}</td>
                      <td>
                        <span className={`lp-decision lp-d-${row.decision}`}>{DECISION_LABEL[row.decision]}</span>
                      </td>
                      <td className="lp-what">{row.what}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </figure>
        </section>

        <section className="lp-three" aria-label="What Holdcall does">
          <div>
            <h2>Decides on the machine</h2>
            <p>
              Rules that deny, allow, or hold a tool for a human, scoped to an agent and a connector. Budgets that cap
              how many calls a session may make. Every way of not getting a decision is a denial: no daemon, no
              decision, no call.
            </p>
          </div>
          <div>
            <h2>Approves with the real arguments</h2>
            <p>
              A held call waits until someone runs <code>holdcall approve</code> and reads exactly what the server would
              receive, every byte shown as itself. Never a summary, never text the model wrote about the call.
            </p>
          </div>
          <div>
            <h2>Records what you can verify</h2>
            <p>
              Each call, decision and policy change is one entry in a hash-chained journal on disk.{" "}
              <code>holdcall verify</code> checks it without a network, and the format is documented so a second program
              can check it too.
            </p>
          </div>
        </section>

        <section className="lp-moment" id="moment" aria-labelledby="lp-moment-title">
          <div className="lp-moment-text">
            <h2 id="lp-moment-title">The moment that matters</h2>
            <p>
              The agent asked to run <code>dangerous_tool</code> on a release branch, with force. The rule for that
              tool says <em>ask</em>. Holdcall held the call, the operator read the arguments and rejected it, and the
              agent was told so in words it does not retry.
            </p>
            <p className="lp-dim">
              The daemon wrote the rejection to the journal before the relay was told. The server never received the
              call.
            </p>
          </div>
          <div className="lp-terminals">
            <pre className="lp-term" aria-label="Output of holdcall approve">
              <span className="lp-prompt">$ holdcall approve</span>
              {"\n"}
              {"id         483e0508febce5dcc97fb26becf27180-7\n"}
              {"age        2s\n"}
              {"agent      claude-code\n"}
              {"connector  github\n"}
              {"tool       dangerous_tool\n"}
              {"arguments:\n"}
              {"  {\n"}
              {'    "branch": "release/2026-09",\n'}
              {'    "force": true\n'}
              {"  }\n"}
              <span className="lp-prompt">$ holdcall reject 483e0508febce5dcc97fb26becf27180-7 --reason &quot;not that branch&quot;</span>
              {"\n"}
              {"rejected dangerous_tool for agent claude-code on connector github (recorded in the journal)\n"}
              {"reason: not that branch"}
            </pre>
            <pre className="lp-term lp-term-agent" aria-label="What the agent received">
              <span className="lp-prompt">what the agent received</span>
              {"\n"}
              {"A human reviewing this call's real arguments rejected it. Do not retry automatically."}
            </pre>
          </div>
        </section>

        <section className="lp-how" id="how" aria-labelledby="lp-how-title">
          <h2 id="lp-how-title">How it fits</h2>
          <ol className="lp-flow">
            <li>
              <span className="lp-flow-name">Your client</span>
              <span className="lp-flow-note">Claude Code, Cursor, Claude Desktop. Pointed at Holdcall by <code>holdcall init</code>.</span>
            </li>
            <li className="lp-flow-holdcall">
              <span className="lp-flow-name">holdcall serve</span>
              <span className="lp-flow-note">
                Relays every byte unchanged. A <code>tools/call</code> is decided before it is forwarded.
              </span>
            </li>
            <li>
              <span className="lp-flow-name">Your MCP server</span>
              <span className="lp-flow-note">Receives exactly what the client sent, or nothing.</span>
            </li>
          </ol>
          <p className="lp-how-under">
            Under the relay, one daemon per user decides against rules and budgets in SQLite, keeps credentials in the
            OS store bound to one command, and writes the journal. It answers only to processes that are Holdcall, and it
            works with no network. The agent&apos;s identity is derived from the executable that spawned the relay,
            as the kernel reports it; nothing on the wire can claim to be Claude Code.
          </p>
          <figure className="lp-shot">
            <img src="/console.png" alt="The local console's Held view: one call to dangerous_tool held for a human, showing the agent, the connector, its real arguments with a bidirectional override made visible, and the approve and reject commands to copy" width="1440" height="900" loading="lazy" />
            <figcaption>The local console, read-only, on loopback: a held call with its real arguments. Approving stays on the command line.</figcaption>
          </figure>
        </section>

        <section className="lp-honest" id="honest" aria-labelledby="lp-honest-title">
          <h2 id="lp-honest-title">What it does not promise</h2>
          <dl>
            <div>
              <dt>Not tamper-proof</dt>
              <dd>
                The chain has no key. Anyone who can write the journal file can rewrite it and it will verify. What
                turns the record into evidence is <code>holdcall verify --expect-head</code> against a head you recorded
                somewhere else. We do not use the words audit log.
              </dd>
            </div>
            <div>
              <dt>Verified on macOS</dt>
              <dd>
                Linux and Windows are compiled and vetted, not yet run. The status page says which claims hold on
                which platform, per test.
              </dd>
            </div>
            <div>
              <dt>Rules match names, not arguments</dt>
              <dd>
                A rule is keyed on agent, connector and tool. Conditions on what a call carries are not built; a human
                reading the arguments is how that gap is covered today.
              </dd>
            </div>
          </dl>
        </section>

        <section className="lp-proof" aria-label="Evidence">
          <ul>
            <li>
              <b>{derived.testCount}</b>
              <span>tests, run with the race detector on every change</span>
            </li>
            <li>
              <b>{summary.attacks.total}</b>
              <span>attacks tried against real builds, {summary.attacks.liveBefore} of them live before they were closed</span>
            </li>
            <li>
              <b>{summary.guarantees.total}</b>
              <span>guarantees, each citing the tests that hold it</span>
            </li>
            <li>
              <b>{summary.findings.total}</b>
              <span>findings published, {summary.findings.open} still open</span>
            </li>
          </ul>
          <p>
            All of it is on the <a href="/status/">status page</a>, which refuses to build on a claim without a test
            behind it.
          </p>
        </section>

        <section className="lp-access" id="access" aria-labelledby="lp-access-title">
          <div className="lp-inner">
            <h2 id="lp-access-title">Early access</h2>
            <p>
              The engine is free for one person and one machine, and always will be. The team console, with the
              journal of every machine, policy in one place and approvals from your phone, opens to a small group
              first.
            </p>
            <Waitlist />
          </div>
        </section>
      </main>

      <footer className="lp-foot">
        <span>Holdcall, {new Date(derived.commitDate).getUTCFullYear()}. Built from commit {derived.commit}.</span>
        <nav aria-label="Footer">
          <a href="/status/">Status</a>
          <a href="/status/#architecture">Architecture</a>
          <a href="/status/#decisions">Decisions</a>
        </nav>
      </footer>
    </div>
  );
}
