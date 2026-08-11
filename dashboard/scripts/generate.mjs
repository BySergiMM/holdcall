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
}

// Cross-links have to resolve, or a milestone page shows an empty guarantee.
const guaranteeIds = new Set(state.guarantees.map((g) => g.id));
for (const m of state.milestones) {
  for (const id of m.guarantees ?? []) {
    if (!guaranteeIds.has(id)) fail(`milestone ${m.id}: references unknown guarantee "${id}"`);
  }
}
for (const layer of state.architecture.layers) {
  for (const id of layer.guarantees ?? []) {
    if (!guaranteeIds.has(id)) fail(`architecture layer "${layer.name}": unknown guarantee "${id}"`);
  }
}

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
