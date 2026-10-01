// TypeScript mirror of the heall event stream. contracts/README.md is the
// source of truth; cli/internal/events/events.go is the Go side.

export const VERSION = 1;

export type Verdict = "pass" | "fail" | "flaky" | "skipped";

export type Stage =
  | "run"
  | "triage"
  | "reproduce"
  | "locate"
  | "heal"
  | "deliver";

export type Commit = {
  sha: string;
  subject: string;
  author: string;
  date: string;
};

export type GuardrailCheck = {
  name: string;
  passed: boolean;
  detail: string;
};

export type Payloads = {
  run_started: {
    repo: string;
    branch: string;
    good: string;
    bad: string;
    dry_run: boolean;
  };
  stage_started: Record<string, never>;
  stage_done: { status: "ok" | "escalated" | "error"; duration_ms: number };
  triage_done: {
    test_name: string;
    test_file: string;
    suspect_files: string[];
    excerpt: string;
  };
  reproduce_run: {
    sha: string;
    attempt: number;
    verdict: Verdict;
    duration_ms: number;
  };
  reproduce_done: {
    reproduced: boolean;
    flaky: boolean;
    runs: number;
    failures: number;
    good_passes: boolean;
  };
  // Commits are oldest first: the first is known good, the last is bad.
  locate_started: { commits: Commit[]; workers: number };
  // lo and hi index into locate_started.commits: lo is the newest commit
  // known good, hi the oldest known bad.
  round_started: { round: number; lo: number; hi: number; probes: string[] };
  commit_testing: { sha: string; round: number; worker: number };
  commit_tested: {
    sha: string;
    round: number;
    worker: number;
    verdict: Verdict;
    duration_ms: number;
  };
  round_done: { round: number; lo: number; hi: number };
  culprit_found: {
    commit: Commit;
    diff: string;
    rounds: number;
    tested: number;
    duration_ms: number;
  };
  agent_started: { model: string; max_attempts: number };
  agent_thought: { text: string };
  tool_call: { id: string; name: string; input: unknown };
  tool_result: { id: string; name: string; ok: boolean; summary: string };
  patch_submitted: { attempt: number; diff: string };
  guardrail_checked: {
    attempt: number;
    passed: boolean;
    checks: GuardrailCheck[];
  };
  verify_done: {
    attempt: number;
    status: "verified" | "rejected" | "failed";
    target_passed: boolean;
    suite_passed: boolean;
    new_failures: string[];
  };
  agent_done: {
    outcome: "fixed" | "escalated";
    attempts: number;
    root_cause: string;
    patch: string;
    reason: string;
  };
  deliver_done: {
    outcome: "pr" | "patch_file" | "diagnosis";
    url: string;
    path: string;
  };
  escalated: { reason: string; diagnosis: string };
  run_done: { outcome: "fixed" | "escalated" | "error"; duration_ms: number };
  log: { level: "info" | "warn" | "error"; message: string };
};

export type Kind = keyof Payloads;

// A discriminated union: narrowing on `kind` gives the matching `data` type.
export type HeallEvent = {
  [K in Kind]: {
    v: number;
    seq: number;
    ts: string;
    run_id: string;
    stage: Stage;
    kind: K;
    data: Payloads[K];
  };
}[Kind];

// Required payload fields per kind, used to validate events at runtime.
export const FIELDS: { [K in Kind]: readonly (keyof Payloads[K])[] } = {
  run_started: ["repo", "branch", "good", "bad", "dry_run"],
  stage_started: [],
  stage_done: ["status", "duration_ms"],
  triage_done: ["test_name", "test_file", "suspect_files", "excerpt"],
  reproduce_run: ["sha", "attempt", "verdict", "duration_ms"],
  reproduce_done: ["reproduced", "flaky", "runs", "failures", "good_passes"],
  locate_started: ["commits", "workers"],
  round_started: ["round", "lo", "hi", "probes"],
  commit_testing: ["sha", "round", "worker"],
  commit_tested: ["sha", "round", "worker", "verdict", "duration_ms"],
  round_done: ["round", "lo", "hi"],
  culprit_found: ["commit", "diff", "rounds", "tested", "duration_ms"],
  agent_started: ["model", "max_attempts"],
  agent_thought: ["text"],
  tool_call: ["id", "name", "input"],
  tool_result: ["id", "name", "ok", "summary"],
  patch_submitted: ["attempt", "diff"],
  guardrail_checked: ["attempt", "passed", "checks"],
  verify_done: [
    "attempt",
    "status",
    "target_passed",
    "suite_passed",
    "new_failures",
  ],
  agent_done: ["outcome", "attempts", "root_cause", "patch", "reason"],
  deliver_done: ["outcome", "url", "path"],
  escalated: ["reason", "diagnosis"],
  run_done: ["outcome", "duration_ms"],
  log: ["level", "message"],
};

const ENVELOPE = ["v", "seq", "ts", "run_id", "stage", "kind", "data"];

// Parses one line of the stream. Throws on anything outside the contract so
// a mismatch with the CLI shows up as an error instead of a blank panel.
export function parseEvent(line: string): HeallEvent {
  const ev: unknown = JSON.parse(line);
  if (typeof ev !== "object" || ev === null || Array.isArray(ev)) {
    throw new Error("event is not an object");
  }
  const rec = ev as Record<string, unknown>;
  for (const key of ENVELOPE) {
    if (!(key in rec)) throw new Error(`event is missing "${key}"`);
  }
  if (rec.v !== VERSION) {
    throw new Error(`event version ${String(rec.v)}, expected ${VERSION}`);
  }
  const kind = rec.kind as Kind;
  if (!Object.hasOwn(FIELDS, kind)) {
    throw new Error(`unknown event kind "${String(rec.kind)}"`);
  }
  const data = rec.data;
  if (typeof data !== "object" || data === null || Array.isArray(data)) {
    throw new Error(`${kind}: data is not an object`);
  }
  const want = [...(FIELDS[kind] as readonly string[])].sort();
  const got = Object.keys(data).sort();
  if (want.join() !== got.join()) {
    throw new Error(`${kind}: fields [${got}] do not match contract [${want}]`);
  }
  return ev as HeallEvent;
}
