// The product page's one function: it stores an email address and the time,
// nothing else, in a private Blob store. It lives outside Next on purpose --
// the page itself stays a static export with no server behind it -- and it
// holds no secret of its own: the store's token is a Vercel environment
// variable, never in this repository.
import { put } from "@vercel/blob";
import { createHash } from "node:crypto";

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/;

export default async function handler(req, res) {
  res.setHeader("Cache-Control", "no-store");
  if (req.method !== "POST") {
    res.status(405).json({ error: "POST an email as JSON" });
    return;
  }
  if (!process.env.BLOB_READ_WRITE_TOKEN) {
    // No store behind this function yet. Said plainly rather than swallowed:
    // the page turns this into "sign-ups open in October".
    res.status(503).json({ error: "sign-ups are not open yet" });
    return;
  }
  const email = typeof req.body?.email === "string" ? req.body.email.trim() : "";
  if (!EMAIL.test(email) || email.length > 254) {
    res.status(400).json({ error: "that does not look like an email address" });
    return;
  }
  // One record per address: the path is a hash of the lower-cased email, so
  // signing up twice overwrites rather than duplicates, and the listing of the
  // store shows no address in any name.
  const id = createHash("sha256").update(email.toLowerCase()).digest("hex").slice(0, 32);
  await put(`waitlist/${id}.json`, JSON.stringify({ email, at: new Date().toISOString() }), {
    access: "private",
    contentType: "application/json",
    addRandomSuffix: false,
    allowOverwrite: true,
  });
  res.status(200).json({ ok: true });
}
