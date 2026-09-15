// Tests for the thing that decides whether this dashboard is allowed to make a
// claim. If these pass but the check does not actually bite, the dashboard is
// worse than no dashboard -- it is a page of checkmarks with no basis.
//
// So most of these are mutation tests: corrupt data/state.json in a specific
// way, run the real generator, and require it to refuse. A check that cannot
// be shown to fail has not been shown to work.

import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync, mkdtempSync, cpSync, existsSync, rmSync } from "node:fs";
import { join, dirname } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { SAMPLES, SENSITIVE } from "./sensitive.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const dashboard = join(here, "..");
const repo = join(dashboard, "..");

/**
 * Runs the real generator against a mutated copy of state.json.
 *
 * The copy is a whole fake dashboard directory placed inside the repo, so the
 * generator still reads the real engine/ tree -- the point is to change only
 * the claims, never the evidence.
 */
function runWith(mutate) {
  const dir = mkdtempSync(join(repo, ".dashboard-test-"));
  try {
    cpSync(join(dashboard, "data"), join(dir, "data"), { recursive: true });
    cpSync(join(dashboard, "scripts"), join(dir, "scripts"), { recursive: true });
    const p = join(dir, "data", "state.json");
    const state = JSON.parse(readFileSync(p, "utf8"));
    mutate(state);
    writeFileSync(p, JSON.stringify(state, null, 2));

    try {
      const stdout = execFileSync("node", [join(dir, "scripts", "generate.mjs")], {
        encoding: "utf8",
        stdio: ["ignore", "pipe", "pipe"],
      });
      return { ok: true, out: stdout, wrote: existsSync(join(dir, "src", "data", "generated.json")) };
    } catch (e) {
      return { ok: false, out: `${e.stdout ?? ""}${e.stderr ?? ""}`, wrote: existsSync(join(dir, "src", "data", "generated.json")) };
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const unchanged = () => {};

test("the real state.json passes as it stands", () => {
  const r = runWith(unchanged);
  assert.ok(r.ok, `the committed state does not build:\n${r.out}`);
  assert.ok(r.wrote, "it reported success without writing anything");
});

test("a guarantee citing a test that does not exist is refused", () => {
  const r = runWith((s) => {
    s.guarantees[0].tests = ["TestSomethingNobodyEverWrote"];
  });
  assert.equal(r.ok, false, "an invented test name was accepted");
  assert.match(r.out, /TestSomethingNobodyEverWrote/);
  assert.equal(r.wrote, false, "it wrote output despite refusing");
});

test("an attack citing a test that does not exist is refused", () => {
  const r = runWith((s) => {
    s.attacks[0].tests = ["TestThisIsNotReal"];
  });
  assert.equal(r.ok, false, "an invented test name was accepted in the attack matrix");
  assert.match(r.out, /TestThisIsNotReal/);
});

test("a guarantee cannot claim verified with no test at all", () => {
  const r = runWith((s) => {
    const g = s.guarantees.find((x) => x.status === "verified");
    g.tests = [];
  });
  assert.equal(r.ok, false, "a bare assertion was published as verified");
  assert.match(r.out, /no test behind it/);
});

test("an attack cannot be reported defended with no test at all", () => {
  const r = runWith((s) => {
    const a = s.attacks.find((x) => x.status === "pass");
    a.tests = [];
  });
  assert.equal(r.ok, false, "an undefended row was published as defended");
  assert.match(r.out, /no test behind it/);
});

test("a guarantee naming an implementation file that does not exist is refused", () => {
  const r = runWith((s) => {
    s.guarantees[0].implementation = ["engine/internal/nowhere/imaginary.go"];
  });
  assert.equal(r.ok, false, "a nonexistent source file was accepted");
  assert.match(r.out, /imaginary\.go/);
});

// The direction that matters most for honesty: it must be impossible to mark
// something untested while quietly citing tests, because that is how a row
// gets to look modest while carrying weight it has not earned -- and the
// reverse mistake (real tests, understated status) hides real coverage.
test("an attack marked not_tested cannot also cite tests", () => {
  const r = runWith((s) => {
    const a = s.attacks.find((x) => x.status === "not_tested");
    a.tests = ["TestWithNoDaemonEverythingIsRefused"];
  });
  assert.equal(r.ok, false);
  assert.match(r.out, /marked not_tested but cites/);
});

// not_applicable and fail need no test to be legitimate, which makes them the
// cheapest place to park an inconvenient row. Requiring prose means downgrading
// something costs at least as much as defending it.
test("a row cannot be downgraded to not_applicable without arguing it", () => {
  const r = runWith((s) => {
    const a = s.attacks.find((x) => x.status === "fail");
    a.status = "not_applicable";
    a.evidence = "n/a";
  });
  assert.equal(r.ok, false, "an inconvenient row was silently parked as not applicable");
  assert.match(r.out, /no written assessment/);
});

test("an attack with no written assessment is refused whatever its status", () => {
  const r = runWith((s) => {
    s.attacks[0].evidence = "";
  });
  assert.equal(r.ok, false);
  assert.match(r.out, /no written assessment/);
});

test("a finding cannot be accepted without an impact and a way out", () => {
  const r = runWith((s) => {
    const f = s.findings.find((x) => x.status === "accepted");
    f.nextAction = "n/a";
  });
  assert.equal(r.ok, false, "a finding was accepted with no route to fixing it");
  assert.match(r.out, /nextAction/);
});

test("an unknown status word is refused rather than rendered as neutral", () => {
  const r = runWith((s) => {
    s.attacks[0].status = "probably_fine";
  });
  assert.equal(r.ok, false, "an unrecognised status was allowed through");
  assert.match(r.out, /probably_fine/);
});

// The reach of this rule is narrow and worth stating exactly: it catches a
// platform claimed verified when every cited test is BUILD-TAGGED away from
// that platform. It cannot catch a test that compiles on a platform without
// establishing anything there -- pathswap_test.go compiles on Windows and
// proves nothing there, and only a human knows that. The page says so under
// "What this dashboard cannot tell you".
test("a platform cannot be called verified when its evidence is tagged away", () => {
  const r = runWith((s) => {
    const g = s.guarantees.find((x) => x.id === "credential-never-in-argv");
    // store_darwin_test.go only; nothing here can run on linux.
    g.tests = ["TestSetNeverPutsTheSecretInArgv"];
    g.platforms.linux = "verified";
  });
  assert.equal(r.ok, false, "linux was called verified on darwin-only evidence");
  assert.match(r.out, /linux/);
});

test("a platform claim the evidence does support is accepted", () => {
  const r = runWith((s) => {
    const g = s.guarantees.find((x) => x.id === "credential-never-in-argv");
    g.tests = ["TestSetNeverPutsTheSecretInArgv"];
    g.platforms = { darwin: "verified" };
  });
  assert.ok(r.ok, `a supported platform claim was wrongly refused:\n${r.out}`);
});

test("a milestone referencing an unknown guarantee is refused", () => {
  const r = runWith((s) => {
    s.milestones[0].guarantees = ["no-such-guarantee"];
  });
  assert.equal(r.ok, false);
  assert.match(r.out, /unknown guarantee/);
});

test("a finding with an unknown severity is refused", () => {
  const r = runWith((s) => {
    s.findings[0].severity = "catastrophic";
  });
  assert.equal(r.ok, false);
  assert.match(r.out, /unknown severity/);
});

// ---------------------------------------------------------------------------
// Regressions from the red-team pass of 2026-08-12. Every one of these was a
// working bypass before the check that now refuses it -- sixteen attempts, of
// which fourteen got through. They are kept as tests rather than as a note,
// because the note would not have failed a build.
// ---------------------------------------------------------------------------

test("a forged milestone status is refused", () => {
  const r = runWith((s) => {
    s.milestones[0].status = "totally_done";
  });
  assert.equal(r.ok, false, "an invented milestone status rendered as neutral grey");
  assert.match(r.out, /totally_done/);
});

// "done" is the status applied optimistically, so the self-contradiction is
// worth catching: a milestone still listing outstanding work is not done.
// This one bit on real data the moment it existed -- M2 was marked done while
// carrying a pending item that was actually M4's work.
test("a milestone cannot be marked done while listing outstanding work", () => {
  const r = runWith((s) => {
    // Any milestone that still lists work. It used to pick the in-progress
    // one, which stopped existing the day M4 was finished.
    const m = s.milestones.find((x) => x.status !== "done" && (x.pending ?? []).length > 0);
    assert.ok(m, "no milestone with outstanding work to mutate");
    m.status = "done";
  });
  assert.equal(r.ok, false, "outstanding work was relabelled done");
  assert.match(r.out, /still listing/);
});

// Synthesised rather than found: since D-004 was decided no milestone in
// the data is blocked, and a test that only works while one is would
// silently stop guarding the rule the day the roadmap clears.
test("a blocked milestone cannot be relabelled done", () => {
  const blocked = {
    id: "M99", name: "Synthetic", status: "blocked", objective: "A thing that waits on a decision.",
    deliverables: [], guarantees: [], limitations: [], pending: ["decide something first"],
  };
  const legitimate = runWith((s) => { s.milestones.push({ ...blocked }); });
  assert.equal(legitimate.ok, true, `a blocked milestone with outstanding work is a legitimate state:\n${legitimate.out}`);
  const r = runWith((s) => { s.milestones.push({ ...blocked, status: "done" }); });
  assert.equal(r.ok, false);
  assert.match(r.out, /still listing/);
});

test("a forged decision status is refused", () => {
  const r = runWith((s) => {
    s.decisions[0].status = "handled";
  });
  assert.equal(r.ok, false);
  assert.match(r.out, /handled/);
});

// Marking a question resolved is otherwise free: flip a word and an open
// problem leaves the count. A decision that was really taken was taken on a
// day, so naming the day is the price of claiming it.
test("an open decision cannot be marked resolved without a date", () => {
  const open = {
    id: "D-099", title: "A synthetic question", question: "Is this still open?",
    status: "open", resolution: "Undecided.", provenance: "declared",
  };
  const legitimate = runWith((s) => { s.decisions.push({ ...open }); });
  assert.equal(legitimate.ok, true, `an open decision is a legitimate state:\n${legitimate.out}`);
  const r = runWith((s) => { s.decisions.push({ ...open, status: "resolved" }); });
  assert.equal(r.ok, false, "an open question was silently closed");
  assert.match(r.out, /decidedOn/);
});

test("a forged architecture layer state is refused", () => {
  const r = runWith((s) => {
    s.architecture.layers[0].state = "bulletproof";
  });
  assert.equal(r.ok, false);
  assert.match(r.out, /bulletproof/);
});

test("a forged CI job conclusion is refused", () => {
  const r = runWith((s) => {
    s.ciSnapshot.jobs[0].conclusion = "definitely_fine";
  });
  assert.equal(r.ok, false);
  assert.match(r.out, /definitely_fine/);
});

// Transcribing a run by hand is the step where a red job becomes a green one.
test("a CI snapshot cannot report success while listing a failed job", () => {
  const r = runWith((s) => {
    s.ciSnapshot.conclusion = "success";
    s.ciSnapshot.jobs[0].conclusion = "failure";
  });
  assert.equal(r.ok, false, "a failed job was published under a green heading");
  assert.match(r.out, /listing a failed job/);
});

// Runtime state is forbidden rather than merely absent. The flag is inert
// today, which is exactly why it needs the check now -- before someone wires
// it up and a field called `sessions` starts meaning something.
test("runtime cannot be declared available", () => {
  const r = runWith((s) => {
    s.runtime.available = true;
  });
  assert.equal(r.ok, false);
  assert.match(r.out, /runtime state/);
});

test("invented runtime data is refused outright", () => {
  const r = runWith((s) => {
    s.runtime.sessions = 412;
    s.runtime.journalEntries = 9981;
  });
  assert.equal(r.ok, false, "fabricated runtime numbers were accepted");
  assert.match(r.out, /runtime\.(sessions|journalEntries)/);
});

test("a duplicated id is refused", () => {
  for (const kind of ["attacks", "guarantees", "findings"]) {
    const r = runWith((s) => {
      s[kind].push(JSON.parse(JSON.stringify(s[kind][0])));
    });
    assert.equal(r.ok, false, `a duplicated ${kind} id inflated the totals`);
    assert.match(r.out, /appears more than once/);
  }
});

// state.json is hand-written, so a secret or a local path can arrive that way
// even though the generator never reads one.
test("a secret smuggled into declared prose is refused", () => {
  for (const value of [
    "The token is ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA and it leaks",
    "AKIAIOSFODNN7EXAMPLE was in the log",
    "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
    "-----BEGIN RSA PRIVATE KEY-----",
    "NIM_API_KEY=s3cr3tvalue12345",
  ]) {
    const r = runWith((s) => {
      s.findings[0].evidence = value;
    });
    assert.equal(r.ok, false, `a secret shape was published: ${value.slice(0, 30)}`);
    assert.match(r.out, /looks like/);
  }
});

test("a local path smuggled into declared prose is refused", () => {
  for (const value of [
    "Built from /Users/someone/Documents/GitHub/nim on this machine, which is fine",
    "The journal lives at /home/someone/.nim/nim.db on that host",
    "It resolves to C:\\Users\\someone\\nim on windows",
  ]) {
    const r = runWith((s) => {
      s.project.statusNote = value;
    });
    assert.equal(r.ok, false, `a local path was published: ${value}`);
    assert.match(r.out, /path/);
  }
});

// Derived facts are computed after the declared ones are spread in, so a
// state.json key of the same name cannot win. That is load-bearing -- it is
// what stops the test count and the commit being asserted by hand -- and it
// is one property-ordering edit away from silently reversing.
test("state.json cannot override the derived facts", () => {
  const r = runWith((s) => {
    s.derived = { testCount: 9999, commit: "deadbee", clean: true, benchmarkCount: 999 };
  });
  assert.ok(r.ok, "overriding derived should not fail the build, only be ignored");
  const out = JSON.parse(readFileSync(join(dashboard, "src", "data", "generated.json"), "utf8"));
  assert.notEqual(out.derived.testCount, 9999, "a hand-written test count won over the real one");
  assert.notEqual(out.derived.commit, "deadbee", "a hand-written commit won over the real one");
});

test("state.json cannot override the computed summary", () => {
  const r = runWith((s) => {
    s.summary = { attacks: { fail: 0, total: 40, pass: 40 } };
  });
  assert.ok(r.ok);
  const out = JSON.parse(readFileSync(join(dashboard, "src", "data", "generated.json"), "utf8"));
  assert.notEqual(out.summary.attacks.fail, 0, "the undefended count was zeroed by hand");
  assert.equal(out.summary.attacks.fail, out.attacks.filter((a) => a.status === "fail").length);
});

// Not a validation rule but a property of the output: the resolved test list
// must not destroy the human-written prose it sits beside.
test("resolving tests does not overwrite the written assessment", () => {
  execFileSync("node", [join(here, "generate.mjs")], { stdio: "ignore" });
  const out = JSON.parse(readFileSync(join(dashboard, "src", "data", "generated.json"), "utf8"));
  for (const a of out.attacks) {
    assert.equal(typeof a.evidence, "string", `attack ${a.id} lost its written assessment`);
    assert.ok(a.evidence.length > 0, `attack ${a.id} has an empty assessment`);
    assert.ok(Array.isArray(a.evidenceTests), `attack ${a.id} has no resolved test list`);
  }
});

// The CI block is a snapshot. If it ever silently described a different commit
// than the page, a reader would take a green tick for the current tree.
test("a CI snapshot from another commit is flagged stale", () => {
  const r = runWith((s) => {
    s.ciSnapshot.commit = "0000000";
  });
  assert.ok(r.ok, "changing the CI commit should not fail the build, only mark it");
  const out = JSON.parse(readFileSync(join(dashboard, "src", "data", "generated.json"), "utf8"));
  assert.equal(typeof out.ciSnapshot.stale, "boolean");
});

// Everything on the page is public-by-construction only because nothing
// private is ever read. This asserts that literally: the output must contain
// no absolute path from this machine, no home directory, and none of the
// obvious shapes of a secret.
test("the generated data carries nothing from outside the repository", () => {
  execFileSync("node", [join(here, "generate.mjs")], { stdio: "ignore" });
  const text = readFileSync(join(dashboard, "src", "data", "generated.json"), "utf8");

  assert.ok(!text.includes(process.env.HOME ?? " __nope__"), "the output contains a home directory path");
  assert.ok(!/\/Users\/[a-z]/i.test(text), "the output contains an absolute macOS user path");
  assert.ok(!/\/home\/[a-z]/i.test(text), "the output contains an absolute Linux user path");

  for (const [what, re] of [
    ["a GitHub token", /gh[pousr]_[A-Za-z0-9]{16,}/],
    ["an AWS key id", /AKIA[0-9A-Z]{16}/],
    ["a private key block", /-----BEGIN [A-Z ]*PRIVATE KEY-----/],
    ["a bearer token", /bearer\s+[A-Za-z0-9._-]{20,}/i],
    ["an env assignment that looks like a secret", /(?:SECRET|TOKEN|PASSWORD|API_KEY)\s*[=:]\s*["']?[A-Za-z0-9._-]{12,}/],
  ]) {
    assert.ok(!re.test(text), `the output looks like it contains ${what}`);
  }
});

// The journal, credential store and socket are the three things that must
// never be read by a build step. Nothing in the generator should even name a
// path into them.
test("the generator never reads runtime state", () => {
  const src = readFileSync(join(here, "generate.mjs"), "utf8");
  for (const forbidden of ["nim.db", "nim.sock", ".nim/", "security find-generic-password", "secret-tool"]) {
    assert.ok(!src.includes(forbidden), `the generator references runtime state: ${forbidden}`);
  }
});


// Every shape the shared list names is refused when it appears in declared
// prose -- each one, not a representative. The list gained the key shapes an
// Anthropic project is most likely to paste (sk-ant-, github_pat_, sk-) after
// a red-team pass found both scanners silent on all three, and this is what
// keeps the next addition from being silent.
test("every sensitive shape is refused in declared prose", () => {
  for (const [what] of SENSITIVE) {
    assert.ok(SAMPLES[what], `no sample for "${what}": add one so the pattern is proven to bite`);
    const r = runWith((s) => {
      s.findings[0].evidence = `Quoting the leak for the record: ${SAMPLES[what]} -- and that is all.`;
    });
    assert.equal(r.ok, false, `${what} passed the declared-data scan: ${SAMPLES[what]}`);
    assert.match(r.out, new RegExp(what.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")), `the refusal does not name ${what}`);
  }
});

// The two scanners read one list. A pattern present in one and absent from the
// other is how `npm run check` passed a JWT that only the build refused.
test("the declared-data scan and the output scan share one list", () => {
  const generate = readFileSync(join(dashboard, "scripts", "generate.mjs"), "utf8");
  const scan = readFileSync(join(dashboard, "scripts", "scan-output.mjs"), "utf8");
  for (const src of [generate, scan]) {
    assert.match(src, /from "\.\/sensitive\.mjs"/, "a scanner stopped importing the shared list");
    assert.doesNotMatch(src, /^const SENSITIVE = \[/m, "a scanner grew a private copy of the list");
  }
});
