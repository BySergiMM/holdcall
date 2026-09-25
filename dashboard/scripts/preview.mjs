// Serves dashboard/out on localhost with the same headers vercel.json sets,
// so what you look at locally is what would be served -- including the CSP,
// which is the setting most likely to break the page silently in production
// and not at all in `next dev`.
//
// Binds to 127.0.0.1 only. This page is a list of Holdcall's weaknesses; it should
// not become reachable on the local network because someone previewed it.

import { createServer } from "node:http";
import { readFileSync, existsSync, statSync } from "node:fs";
import { join, extname, dirname, normalize } from "node:path";
import { fileURLToPath } from "node:url";

const out = join(dirname(fileURLToPath(import.meta.url)), "..", "out");
const port = Number(process.env.PORT ?? 4321);

if (!existsSync(out)) {
  console.error("  No build to preview. Run `npm run build` first.");
  process.exit(1);
}

const TYPES = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".txt": "text/plain; charset=utf-8",
  ".svg": "image/svg+xml",
  ".ico": "image/x-icon",
  ".woff2": "font/woff2",
};

// Kept in step with vercel.json by hand. If they drift, the preview is
// reassuring about a configuration that is not the one being served.
const { headers: rules } = JSON.parse(readFileSync(join(out, "..", "vercel.json"), "utf8"));
const SECURITY = Object.fromEntries(rules[0].headers.map((h) => [h.key, h.value]));

createServer((req, res) => {
  // Resolve inside out/ only. A preview server that serves ../../.ssh because
  // it was "just for looking at" is its own small version of this project's
  // whole subject.
  const url = decodeURIComponent((req.url ?? "/").split("?")[0]);
  let path = normalize(join(out, url));
  if (!path.startsWith(out)) {
    res.writeHead(403).end("outside the build directory");
    return;
  }
  if (existsSync(path) && statSync(path).isDirectory()) path = join(path, "index.html");
  if (!existsSync(path)) {
    path = join(out, "404.html");
    if (!existsSync(path)) {
      res.writeHead(404, SECURITY).end("not found");
      return;
    }
    res.writeHead(404, { ...SECURITY, "Content-Type": TYPES[".html"] }).end(readFileSync(path));
    return;
  }
  res.writeHead(200, { ...SECURITY, "Content-Type": TYPES[extname(path)] ?? "application/octet-stream" });
  res.end(readFileSync(path));
}).listen(port, "127.0.0.1", () => {
  console.log(`\n  Holdcall control plane, production build, local only:\n`);
  console.log(`      http://127.0.0.1:${port}\n`);
  console.log(`  Serving ${out}`);
  console.log(`  With the vercel.json headers applied, CSP included. Ctrl-C to stop.\n`);
});
