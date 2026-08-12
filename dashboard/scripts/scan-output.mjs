// Scans what is actually about to be published.
//
// generate.mjs already refuses a secret or a local path written into
// state.json. That is not the whole surface: the page also contains prose
// written straight into the .tsx files, Next.js embeds a serialised copy of
// the props in the HTML, and the build inlines chunk contents. Checking the
// data file proves nothing about any of that.
//
// So this runs over every byte of dashboard/out after the build, which is the
// only artefact whose contents are the thing being served.

import { readFileSync, readdirSync, statSync, existsSync } from "node:fs";
import { join, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const out = join(here, "..", "out");

if (!existsSync(out)) {
  console.error("  no out/ directory -- run the build first");
  process.exit(2);
}

const SENSITIVE = [
  ["a GitHub token", /gh[pousr]_[A-Za-z0-9]{16,}/],
  ["an AWS key id", /AKIA[0-9A-Z]{16}/],
  ["a Slack token", /xox[abprs]-[A-Za-z0-9-]{10,}/],
  ["a private key block", /-----BEGIN [A-Z ]*PRIVATE KEY-----/],
  ["a JWT", /eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\./],
  ["a bearer token", /bearer\s+[A-Za-z0-9._-]{20,}/i],
  ["an assignment that looks like a secret", /(SECRET|TOKEN|PASSWORD|API_?KEY|PRIVATE_?KEY)\s*[=:]\s*["']?[A-Za-z0-9._\-/+]{12,}/],
  ["an absolute macOS user path", /\/Users\/[A-Za-z0-9._-]+\//],
  ["an absolute Linux user path", /\/home\/[A-Za-z0-9._-]+\//],
  ["a Windows user path", /[A-Z]:\\Users\\[A-Za-z0-9._-]+/],
  // Nim's own runtime artefacts. None of these should be reachable from a
  // build step, so a path to one in the output means something read it.
  ["a path to the journal database", /[\w./-]*nim\.db\b/],
  ["a path to the daemon socket", /[\w./-]*nim\.sock\b/],
];

// The page legitimately discusses these by name -- "the journal, nim.db" is
// the subject matter. What must never appear is a resolvable path to one, so
// the bare filenames are allowed and anything with a directory in front is
// not.
const ALLOWED_BARE = new Set(["nim.db", "nim.sock"]);

function walk(dir, acc = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, acc);
    else acc.push(p);
  }
  return acc;
}

const problems = [];
let bytes = 0;

for (const file of walk(out)) {
  const text = readFileSync(file, "utf8");
  bytes += text.length;
  const rel = relative(out, file);
  for (const [what, re] of SENSITIVE) {
    for (const m of text.matchAll(new RegExp(re, re.flags.includes("g") ? re.flags : re.flags + "g"))) {
      if (ALLOWED_BARE.has(m[0])) continue;
      problems.push(`${rel}: ${what} -- ${JSON.stringify(m[0].slice(0, 60))}`);
    }
  }
}

if (problems.length > 0) {
  console.error("\n  Refusing to publish. The built output contains:\n");
  for (const p of problems) console.error(`    - ${p}`);
  console.error("\n  Nothing private may leave the host. Remove it; do not relax this scan.\n");
  process.exit(1);
}

console.log(`  ok  scanned ${walk(out).length} published files (${Math.round(bytes / 1024)} KiB), nothing sensitive`);
