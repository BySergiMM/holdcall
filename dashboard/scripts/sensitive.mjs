// The one list of shapes that must never reach the page.
//
// Two scanners use it: generate.mjs walks every string in the declared data
// before anything is built, and scan-output.mjs walks every byte of out/ after
// the build. They used to carry their own copies, and the copies drifted -- a
// JWT was caught after the build and not before it, so `npm run check`, which
// the docs name as the gate for editing a claim, passed a JWT that only
// `npm run build` refused. One list, imported twice, cannot drift.
//
// Each entry names what it catches, so the refusal can say so.

export const SENSITIVE = [
  ["a GitHub token", /gh[pousr]_[A-Za-z0-9]{16,}/],
  ["a GitHub fine-grained token", /github_pat_[A-Za-z0-9_]{22,}/],
  ["an Anthropic API key", /sk-ant-[A-Za-z0-9_-]{20,}/],
  ["an OpenAI-style secret key", /\bsk-[A-Za-z0-9_-]{20,}/],
  ["an AWS key id", /AKIA[0-9A-Z]{16}/],
  ["a Slack token", /xox[abprs]-[A-Za-z0-9-]{10,}/],
  ["a private key block", /-----BEGIN [A-Z ]*PRIVATE KEY-----/],
  ["a JWT", /eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\./],
  ["a bearer token", /bearer\s+[A-Za-z0-9._-]{20,}/i],
  ["an assignment that looks like a secret", /(SECRET|TOKEN|PASSWORD|API_?KEY|PRIVATE_?KEY)\s*[=:]\s*["']?[A-Za-z0-9._\-/+]{12,}/],
  ["an absolute macOS user path", /\/Users\/[A-Za-z0-9._-]+\//],
  ["an absolute Linux user path", /\/home\/[A-Za-z0-9._-]+\//],
  ["a Windows user path", /[A-Z]:\\Users\\[A-Za-z0-9._-]+/],
];

// Holdcall's own runtime artefacts. None of these should be reachable from a build
// step, so a path to one in the output means something read it. The page
// legitimately discusses them by name -- "the journal, holdcall.db" is the subject
// matter -- so the bare filenames are allowed and anything with a directory
// in front is not.
export const RUNTIME_PATHS = [
  ["a path to the journal database", /[\w./-]*holdcall\.db\b/],
  ["a path to the daemon socket", /[\w./-]*holdcall\.sock\b/],
];

export const ALLOWED_BARE = new Set(["holdcall.db", "holdcall.sock"]);

// Sample values for each shape, so a test can prove every pattern bites. Not
// real credentials: each is the shape with the payload spelled out.
export const SAMPLES = {
  "a GitHub token": "ghp_" + "A".repeat(20),
  "a GitHub fine-grained token": "github_pat_" + "B".repeat(30),
  "an Anthropic API key": "sk-ant-api03-" + "C".repeat(24),
  "an OpenAI-style secret key": "sk-" + "D".repeat(24),
  "an AWS key id": "AKIA" + "E".repeat(16),
  "a Slack token": "xoxb-" + "1".repeat(12),
  "a private key block": "-----BEGIN RSA PRIVATE KEY-----",
  "a JWT": "eyJ" + "F".repeat(12) + ".eyJ" + "G".repeat(12) + ".sig",
  "a bearer token": "Bearer " + "H".repeat(24),
  "an assignment that looks like a secret": "API_KEY=" + "I".repeat(16),
  "an absolute macOS user path": "/Users/someone/holdcall",
  "an absolute Linux user path": "/home/someone/holdcall",
  "a Windows user path": "C:\\Users\\someone",
};
