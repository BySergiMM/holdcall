// Builds src/data/generated.json from two sources:
//
//   1. data/state.json  -- written by hand, checked here
//   2. the repository    -- read here, never written by hand
//
// And, more importantly, it REFUSES to produce either if a claim in (1) cites
// evidence that is not in (2). A guarantee that names a test must name a test
// that exists. An attack that cites a test must cite a real one. A file listed
// as an implementation must be a file.
//
// That is the only structural defence this dashboard has against becoming a
// second place where things that are not true get written down. It is a real
// defence and it is also a narrow one: it proves a cited test EXISTS, not that
// the test proves what the sentence next to it claims. Nothing automatic can
// establish the second thing, so the dashboard says so on its own page rather
// than letting the checkmarks imply otherwise.

import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync, readdirSync, statSync, mkdirSync, existsSync } from "node:fs";
import { join, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const dashboard = join(here, "..");
const repo = join(dashboard, "..");
const engine = join(repo, "engine");

const problems = [];
const fail = (m) => problems.push(m);

// ---------------------------------------------------------------- repository

// git() returns UNKNOWN rather than throwing. A dashboard built from a tarball
// with no .git is a dashboard with less information, not a broken build --
// but it must say UNKNOWN rather than quietly showing nothing.
function git(...args) {
  try {
    return execFileSync("git", args, { cwd: repo, encoding: "utf8" }).trim();
  } catch {
    return null;
  }
}

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    if (name === "node_modules" || name === ".git") continue;
    const p = join(dir, name);
    const s = statSync(p);
    if (s.isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

const goFiles = existsSync(engine) ? walk(engine).filter((p) => p.endsWith(".go")) : [];
if (goFiles.length === 0) fail("no Go sources found under engine/ -- nothing could be verified");

// Every test function in the tree, by name, with the file it lives in. This is
// the set every declared test reference is checked against.
const testFunctions = new Map();
const benchmarks = new Set();
const packages = new Set();

// Not every `func TestXxx` is a test. The helper-process pattern re-executes
// the test binary to get a real separate process, and its entry point has to
// be named TestSomething to be reachable -- but it asserts nothing. Counting
// it would inflate the only number on this page that looks like a measure of
// rigour, so it is excluded and the exclusion is reported.
const notReallyATest = (name) => /^TestHelperProcess/.test(name);
let excludedFromTestCount = 0;

for (const file of goFiles) {
  const src = readFileSync(file, "utf8");
  const rel = relative(repo, file);
  packages.add(dirname(rel));
  for (const m of src.matchAll(/^func (Test[A-Za-z0-9_]*)\(/gm)) {
    if (notReallyATest(m[1])) {
      excludedFromTestCount++;
      continue;
    }
    testFunctions.set(m[1], rel);
  }
  for (const m of src.matchAll(/^func (Benchmark[A-Za-z0-9_]*)\(/gm)) {
    benchmarks.add(m[1]);
  }
}

// Build tags decide whether a test compiles on a given platform. A test behind
// //go:build unix does not exist on Windows, and a dashboard that says
// "verified" on the strength of it should be able to say where.
const platformOf = (rel) => {
  const src = readFileSync(join(repo, rel), "utf8");
  const tag = src.match(/^\/\/go:build (.+)$/m)?.[1] ?? "";
  const name = rel.split("/").pop();
  if (/_darwin(_test)?\.go$/.test(name) || tag === "darwin") return "darwin";
  if (/_linux(_test)?\.go$/.test(name) || tag === "linux") return "linux";
  if (/_windows(_test)?\.go$/.test(name) || tag === "windows") return "windows";
  if (/_unix(_test)?\.go$/.test(name) || tag === "unix") return "unix";
  return "all";
};

const docs = existsSync(join(repo, "docs"))
  ? walk(join(repo, "docs")).map((p) => relative(repo, p)).filter((p) => p.endsWith(".md")).sort()
  : [];

const head = git("rev-parse", "HEAD");
const derived = {
  generatedAtCommit: head,
  commit: head ? head.slice(0, 7) : "UNKNOWN",
  commitSubject: git("log", "-1", "--format=%s") ?? "UNKNOWN",
  commitDate: git("log", "-1", "--format=%cI") ?? "UNKNOWN",
  branch: git("rev-parse", "--abbrev-ref", "HEAD") ?? "UNKNOWN",
  // A dirty tree means the page describes something that is not committed.
  // Worth saying out loud rather than hiding.
  clean: git("status", "--porcelain") === "",
  commitCount: Number(git("rev-list", "--count", "HEAD") ?? 0) || "UNKNOWN",
  goVersion: readFileSync(join(engine, "go.mod"), "utf8").match(/^go (.+)$/m)?.[1] ?? "UNKNOWN",
  testCount: testFunctions.size,
  // Reported so the number above is auditable rather than merely asserted.
  excludedFromTestCount,
  benchmarkCount: benchmarks.size,
  packageCount: packages.size,
  goFileCount: goFiles.length,
  docs,
};

// -------------------------------------------------------------------- checks

const state = JSON.parse(readFileSync(join(dashboard, "data", "state.json"), "utf8"));

const STATUS = ["verified", "partial", "unverified", "not_tested", "unsupported"];
const ATTACK_STATUS = ["pass", "partial", "fail", "not_tested", "not_applicable"];
const MILESTONE_STATUS = ["done", "in_progress", "planned", "blocked"];
const DECISION_STATUS = ["open", "resolved"];
const LAYER_STATE = ["implemented", "partial", "planned", "absent"];
const CI_CONCLUSION = ["success", "failure", "cancelled", "skipped", "unknown"];

// Anything not on a known list renders through tone(), which falls back to a
// neutral grey -- so an invented status word does not look wrong, it looks
// calm. Every enum on the page is therefore checked by name.
const enumCheck = (value, allowed, where) => {
  if (!allowed.includes(value)) {
    fail(`${where}: "${value}" is not one of ${allowed.join(", ")}`);
  }
};

// ---------------------------------------------------------------- no secrets
//
// The page is public-by-construction only because nothing private is ever put
// in it. That holds for what the generator READS -- it never opens the
// journal, a socket or a credential store -- but state.json is written by
// hand, so a secret or a local path can arrive that way. Every string in it
// is walked here rather than trusted, and the build fails rather than
// publishing one.

const SENSITIVE = [
  ["a GitHub token", /gh[pousr]_[A-Za-z0-9]{16,}/],
  ["an AWS key id", /AKIA[0-9A-Z]{16}/],
  ["a Slack token", /xox[abprs]-[A-Za-z0-9-]{10,}/],
  ["a private key block", /-----BEGIN [A-Z ]*PRIVATE KEY-----/],
  ["a bearer token", /bearer\s+[A-Za-z0-9._-]{20,}/i],
  ["an assignment that looks like a secret", /(SECRET|TOKEN|PASSWORD|API_?KEY|PRIVATE_?KEY)\s*[=:]\s*["']?[A-Za-z0-9._\-/+]{12,}/],
  ["an absolute macOS user path", /\/Users\/[A-Za-z0-9._-]+\//],
  ["an absolute Linux user path", /\/home\/[A-Za-z0-9._-]+\//],
  ["a Windows user path", /[A-Z]:\\Users\\[A-Za-z0-9._-]+/],
];

function scanStrings(node, path = "state") {
  if (typeof node === "string") {
    for (const [what, re] of SENSITIVE) {
      if (re.test(node)) fail(`${path}: contains what looks like ${what}`);
    }
  } else if (Array.isArray(node)) {
    node.forEach((v, i) => scanStrings(v, `${path}[${i}]`));
  } else if (node && typeof node === "object") {
    for (const [k, v] of Object.entries(node)) scanStrings(v, `${path}.${k}`);
  }
}

// Where a test lives, or null if it does not exist. Returning null is what
// turns an unsubstantiated claim into a build failure.
function locate(name) {
  return testFunctions.get(name) ?? null;
}

function checkFiles(paths, where) {
  for (const p of paths ?? []) {
    if (!existsSync(join(repo, p))) fail(`${where}: implementation file "${p}" does not exist`);
  }
}

// A guarantee is only allowed to say "verified" if it cites at least one test
// that exists. Anything weaker has to say so.
for (const g of state.guarantees) {
  const where = `guarantee "${g.id}"`;
  if (!STATUS.includes(g.status)) fail(`${where}: unknown status "${g.status}"`);
  checkFiles(g.implementation, where);

  g.evidenceTests = [];
  for (const name of g.tests ?? []) {
    const file = locate(name);
    if (!file) {
      fail(`${where}: cites test ${name}, which does not exist in the repository`);
      continue;
    }
    g.evidenceTests.push({ name, file, platform: platformOf(file) });
  }
  if (g.status === "verified" && g.evidenceTests.length === 0) {
    fail(`${where}: claims "verified" with no test behind it`);
  }
  for (const [plat, claim] of Object.entries(g.platforms ?? {})) {
    if (!STATUS.includes(claim)) fail(`${where}: unknown platform status "${claim}" for ${plat}`);
    // A platform cannot be called verified on the strength of a test that
    // does not compile there.
    if (claim === "verified") {
      const covers = g.evidenceTests.filter(
        (e) => e.platform === "all" || e.platform === plat || (e.platform === "unix" && plat !== "windows")
      );
      if (g.evidenceTests.length > 0 && covers.length === 0) {
        fail(`${where}: claims verified on ${plat}, but no cited test compiles there`);
      }
    }
  }
}

for (const a of state.attacks) {
  const where = `attack "${a.id}"`;
  if (!ATTACK_STATUS.includes(a.status)) fail(`${where}: unknown status "${a.status}"`);

  // Two statuses -- not_applicable and fail -- need no test to be legitimate,
  // which makes them the cheapest place to park an inconvenient row. They
  // still have to be argued in prose, so that downgrading something is at
  // least as much work as defending it and leaves a sentence to disagree with.
  if (typeof a.evidence !== "string" || a.evidence.trim().length < 40) {
    fail(`${where}: no written assessment. Every row must argue its status, not just assert it.`);
  }
  a.evidenceTests = [];
  for (const name of a.tests ?? []) {
    const file = locate(name);
    if (!file) {
      fail(`${where}: cites test ${name}, which does not exist in the repository`);
      continue;
    }
    a.evidenceTests.push({ name, file, platform: platformOf(file) });
  }
  // "pass" is the strongest thing this table says. It has to be backed.
  if (a.status === "pass" && a.evidenceTests.length === 0) {
    fail(`${where}: reported as defended with no test behind it`);
  }
  // And the inverse: an entry with tests must not be sitting at not_tested.
  if (a.status === "not_tested" && a.evidenceTests.length > 0) {
    fail(`${where}: marked not_tested but cites ${a.evidenceTests.length} test(s)`);
  }
}

for (const f of state.findings) {
  const where = `finding "${f.id}"`;
  if (!["critical", "high", "medium", "low"].includes(f.severity)) fail(`${where}: unknown severity`);
  if (!["open", "accepted", "fixed"].includes(f.status)) fail(`${where}: unknown status`);
  // "accepted" is the status that costs nothing to assign, so it is the one
  // that has to carry an argument and a way out.
  for (const field of ["problem", "impact", "evidence", "nextAction"]) {
    if (typeof f[field] !== "string" || f[field].trim().length < 20) {
      fail(`${where}: "${field}" is empty or too thin to mean anything`);
    }
  }
}

// Cross-links have to resolve, or a milestone page shows an empty guarantee.
const guaranteeIds = new Set(state.guarantees.map((g) => g.id));
for (const m of state.milestones) {
  enumCheck(m.status, MILESTONE_STATUS, `milestone ${m.id}`);
  for (const id of m.guarantees ?? []) {
    if (!guaranteeIds.has(id)) fail(`milestone ${m.id}: references unknown guarantee "${id}"`);
  }
  // The one self-contradiction worth catching mechanically: "done" is the
  // status that gets applied optimistically, and a milestone that still lists
  // outstanding work is not done regardless of what the field says.
  if (m.status === "done" && (m.pending ?? []).length > 0) {
    fail(`milestone ${m.id}: marked done while still listing ${m.pending.length} outstanding item(s)`);
  }
}
for (const layer of state.architecture.layers) {
  enumCheck(layer.state, LAYER_STATE, `architecture layer "${layer.name}"`);
  for (const id of layer.guarantees ?? []) {
    if (!guaranteeIds.has(id)) fail(`architecture layer "${layer.name}": unknown guarantee "${id}"`);
  }
}
for (const d of state.decisions) {
  enumCheck(d.status, DECISION_STATUS, `decision ${d.id}`);
  for (const field of ["question", "resolution"]) {
    if (typeof d[field] !== "string" || d[field].trim().length < 20) {
      fail(`decision ${d.id}: "${field}" is empty or too thin to mean anything`);
    }
  }
  // Marking a question resolved is otherwise free -- flip a word and an open
  // problem disappears from the count. A decision that was actually taken was
  // taken on a day, so saying when is the cost of claiming it.
  if (d.status === "resolved" && !/^\d{4}-\d{2}-\d{2}$/.test(d.decidedOn ?? "")) {
    fail(`decision ${d.id}: marked resolved without a decidedOn date (YYYY-MM-DD)`);
  }
  if (d.status === "open" && d.decidedOn) {
    fail(`decision ${d.id}: still open but carries a decidedOn date`);
  }
}

// An id appearing twice double-counts in the summary and collides as a render
// key, so a row can be duplicated to inflate a total or shadow another.
for (const [kind, rows] of [
  ["guarantee", state.guarantees],
  ["attack", state.attacks],
  ["finding", state.findings],
  ["decision", state.decisions],
  ["milestone", state.milestones],
]) {
  const seen = new Set();
  for (const row of rows) {
    if (seen.has(row.id)) fail(`${kind} "${row.id}": id appears more than once`);
    seen.add(row.id);
  }
}

// Runtime state is not merely absent, it is forbidden. The page renders a
// fixed NOT AVAILABLE panel today, so flipping this flag changes nothing --
// which is exactly why it needs a check now, before someone wires the flag up
// and a field called `sessions` starts meaning something.
{
  const allowed = new Set(["$comment", "available", "reason", "provenance"]);
  if (state.runtime.available !== false) {
    fail(`runtime.available is ${JSON.stringify(state.runtime.available)}; this page can never see runtime state`);
  }
  for (const k of Object.keys(state.runtime)) {
    if (!allowed.has(k)) {
      fail(`runtime.${k}: runtime data must not appear here at all, not even as a placeholder`);
    }
  }
}

// A CI snapshot is transcribed by hand from a run, which is the step where a
// red job becomes a green one.
enumCheck(ciSnapshotConclusion(), CI_CONCLUSION, "ciSnapshot.conclusion");
function ciSnapshotConclusion() {
  return state.ciSnapshot.conclusion;
}
for (const j of state.ciSnapshot.jobs) {
  enumCheck(j.conclusion, CI_CONCLUSION, `ciSnapshot job "${j.name}"`);
}
if (state.ciSnapshot.conclusion === "success" && state.ciSnapshot.jobs.some((j) => j.conclusion === "failure")) {
  fail("ciSnapshot: reported success while listing a failed job");
}

scanStrings(state);

// A CI snapshot describing a different commit is stale, and the page has to
// know so it can label it rather than implying the current tree is green.
const ci = state.ciSnapshot;
ci.stale = derived.commit !== "UNKNOWN" && ci.commit !== derived.commit;

// ------------------------------------------------------------------- summary

const count = (xs, p) => xs.filter(p).length;
const summary = {
  guarantees: {
    total: state.guarantees.length,
    verified: count(state.guarantees, (g) => g.status === "verified"),
    partial: count(state.guarantees, (g) => g.status === "partial"),
  },
  attacks: {
    total: state.attacks.length,
    pass: count(state.attacks, (a) => a.status === "pass"),
    partial: count(state.attacks, (a) => a.status === "partial"),
    fail: count(state.attacks, (a) => a.status === "fail"),
    notTested: count(state.attacks, (a) => a.status === "not_tested"),
    notApplicable: count(state.attacks, (a) => a.status === "not_applicable"),
    liveBefore: count(state.attacks, (a) => a.wasLive),
  },
  findings: {
    total: state.findings.length,
    open: count(state.findings, (f) => f.status === "open"),
    accepted: count(state.findings, (f) => f.status === "accepted"),
    fixed: count(state.findings, (f) => f.status === "fixed"),
    high: count(state.findings, (f) => f.severity === "high"),
    medium: count(state.findings, (f) => f.severity === "medium"),
    low: count(state.findings, (f) => f.severity === "low"),
  },
  decisions: {
    total: state.decisions.length,
    open: count(state.decisions, (d) => d.status === "open"),
  },
  milestones: {
    total: state.milestones.length,
    done: count(state.milestones, (m) => m.status === "done"),
    inProgress: count(state.milestones, (m) => m.status === "in_progress"),
    planned: count(state.milestones, (m) => m.status === "planned"),
    blocked: count(state.milestones, (m) => m.status === "blocked"),
  },
  // Deliberately not a percentage. There is no honest denominator: the
  // milestones after M4 have no deliverables written down, so any percentage
  // would be a number invented to look like progress.
  tests: derived.testCount,
};

// ------------------------------------------------------------------- verdict

if (problems.length > 0) {
  console.error("\n  The dashboard refuses to build. Claims without evidence:\n");
  for (const p of problems) console.error(`    - ${p}`);
  console.error(
    `\n  ${problems.length} problem(s). Either the claim is wrong, or the evidence moved.\n` +
      `  Fix data/state.json -- do not relax this check.\n`
  );
  process.exit(1);
}

const out = join(dashboard, "src", "data");
mkdirSync(out, { recursive: true });
writeFileSync(
  join(out, "generated.json"),
  JSON.stringify({ ...state, derived, summary, checks: { testReferencesChecked: true } }, null, 2)
);

console.log(
  `  ok  ${state.guarantees.length} guarantees, ${state.attacks.length} attacks, ` +
    `${state.findings.length} findings -- every cited test exists ` +
    `(${derived.testCount} tests, ${derived.benchmarkCount} benchmarks in the tree)`
);
