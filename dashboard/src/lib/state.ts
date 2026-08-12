import raw from "@/data/generated.json";

// The shapes below describe what scripts/generate.mjs produces. It is the only
// thing that writes this file, and it refuses to write it at all if a claim
// cites evidence that is not in the repository -- so anything reaching these
// types has already been checked for existence. Not for accuracy: see the
// "What this dashboard cannot tell you" section, which says so on the page.

export type Provenance = "declared" | "derived" | "ci" | "runtime" | "unknown";
export type Tone = "ok" | "warn" | "bad" | "none";

export type Evidence = { name: string; file: string; platform: string };

export type Guarantee = {
  id: string;
  title: string;
  status: "verified" | "partial" | "unverified" | "not_tested" | "unsupported";
  statement: string;
  notGuaranteed: string;
  implementation: string[];
  tests: string[];
  evidenceTests: Evidence[];
  platforms: Record<string, string>;
  lastVerified: string;
  provenance: Provenance;
};

export type Attack = {
  id: string;
  name: string;
  category: string;
  status: "pass" | "partial" | "fail" | "not_tested" | "not_applicable";
  wasLive: boolean;
  /** Human-written prose explaining how this stands. */
  evidence: string;
  /** Resolved by the generator from `tests`; every entry provably exists. */
  evidenceTests: Evidence[];
  tests: string[];
};

export type Finding = {
  id: string;
  severity: "critical" | "high" | "medium" | "low";
  title: string;
  problem: string;
  impact: string;
  evidence: string;
  status: "open" | "accepted" | "fixed";
  nextAction: string;
};

export type Decision = {
  id: string;
  title: string;
  question: string;
  status: "open" | "resolved";
  resolution: string;
};

export type Milestone = {
  id: string;
  name: string;
  status: "done" | "in_progress" | "planned" | "blocked";
  objective: string;
  deliverables: string[];
  guarantees: string[];
  limitations: string[];
  pending: string[];
};

export const state = raw as unknown as {
  project: { name: string; tagline: string; currentMilestone: string; currentPhase: string; statusNote: string };
  ciSnapshot: {
    workflow: string; conclusion: string; commit: string; runId: string; takenAt: string;
    stale: boolean; jobs: { name: string; conclusion: string; note?: string }[];
  };
  runtime: { available: boolean; reason: string };
  architecture: {
    nodes: { id: string; label: string; sub: string; note: string }[];
    layers: { name: string; state: string; detail: string; guarantees: string[] }[];
  };
  guarantees: Guarantee[];
  attacks: Attack[];
  findings: Finding[];
  decisions: Decision[];
  milestones: Milestone[];
  derived: {
    commit: string; commitSubject: string; commitDate: string; branch: string; clean: boolean;
    commitCount: number | string; goVersion: string; testCount: number; excludedFromTestCount: number;
    benchmarkCount: number;
    packageCount: number; goFileCount: number; docs: string[];
  };
  summary: {
    guarantees: { total: number; verified: number; partial: number };
    attacks: {
      total: number; pass: number; partial: number; fail: number;
      notTested: number; notApplicable: number; liveBefore: number;
    };
    findings: { total: number; open: number; accepted: number; fixed: number; high: number; medium: number; low: number };
    decisions: { total: number; open: number };
    milestones: { total: number; done: number; inProgress: number; planned: number; blocked: number };
  };
};

// One place decides what colour a word gets. Scattering this would be how a
// "fail" eventually renders green somewhere.
const TONES: Record<string, Tone> = {
  verified: "ok", pass: "ok", done: "ok", resolved: "ok", implemented: "ok", success: "ok", fixed: "ok",
  partial: "warn", in_progress: "warn", open: "warn", planned: "none",
  fail: "bad", blocked: "bad", critical: "bad", high: "bad",
  medium: "warn", low: "none",
  not_tested: "none", unverified: "none", unsupported: "none", not_applicable: "none",
  accepted: "none", unknown: "none",
};

export const tone = (s: string): Tone => TONES[s] ?? "none";
export const label = (s: string) => s.replace(/_/g, " ");
