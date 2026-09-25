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
import { SENSITIVE, RUNTIME_PATHS, ALLOWED_BARE } from "./sensitive.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const out = join(here, "..", "out");

if (!existsSync(out)) {
  console.error("  no out/ directory -- run the build first");
  process.exit(2);
}

// The shapes come from sensitive.mjs, the same list generate.mjs checks the
// declared data against, plus the runtime artefacts only an output scan can
// meaningfully look for.
const CHECKS = [...SENSITIVE, ...RUNTIME_PATHS];

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
  for (const [what, re] of CHECKS) {
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
