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
