import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { parseEvent } from "./events.ts";
import { fold, parseStream, replayDelay, STAGES } from "./run-state.ts";

const sample = (name: string) =>
  parseStream(
    readFileSync(new URL(`../public/samples/${name}.jsonl`, import.meta.url), "utf8"),
    parseEvent,
  );

test("every line of the recorded samples is a valid event", () => {
  for (const name of ["off-by-one", "outdated-test", "flaky"]) {
    const raw = readFileSync(new URL(`../public/samples/${name}.jsonl`, import.meta.url), "utf8");
    const lines = raw.trim().split("\n");
    assert.equal(sample(name).length, lines.length, name);
  }
});

test("a fixed run ends with a culprit, a verified patch and a delivery", () => {
  const s = fold(sample("off-by-one"));
  assert.equal(s.done?.outcome, "fixed");
  assert.deepEqual(STAGES.map((st) => s.stages[st].status), ["ok", "ok", "ok", "ok", "ok"]);
  assert.equal(s.triage?.test_name, "paginate returns a full page");

  assert.equal(s.reproduce.done?.reproduced, true);
  assert.equal(s.reproduce.runs.filter((r) => r.good).length, 1);
  assert.equal(s.reproduce.runs.filter((r) => !r.good && r.verdict === "fail").length, 8);

  const loc = s.locate!;
  assert.equal(loc.commits.length, 121);
  assert.equal(loc.index[loc.culprit!.commit.sha], 53);
  assert.deepEqual([loc.lo, loc.hi], [52, 53]);
  assert.equal(loc.rounds.length, 3);
  assert.deepEqual(loc.rounds[0].after, { lo: 51, hi: 85 });
  assert.equal(Object.keys(loc.testing).length, 0, "nothing is left marked as in progress");
  const verdicts = Object.values(loc.tested).map((t) => t.verdict);
  assert.equal(verdicts.length, loc.culprit!.tested);
  assert.equal(verdicts.filter((v) => v === "skipped").length, 2);

  const patches = s.agent!.items.filter((i) => i.type === "patch");
  assert.equal(patches.length, 2);
  assert.equal(patches[0].staged, true);
  assert.equal(patches[0].guardrails?.passed, false);
  assert.equal(patches[0].verify?.status, "rejected");
  assert.equal(patches[1].staged, false);
  assert.equal(patches[1].guardrails?.passed, true);
  assert.equal(patches[1].verify?.status, "verified");
  assert.equal(s.agent!.done?.outcome, "fixed");
  assert.equal(s.deliver?.outcome, "patch_file");
  assert.equal(s.escalated, undefined);
});

test("an escalation at heal keeps the culprit and records the diagnosis", () => {
  const s = fold(sample("outdated-test"));
  assert.equal(s.done?.outcome, "escalated");
  assert.equal(s.stages.heal.status, "escalated");
  assert.equal(s.escalated?.stage, "heal");
  assert.ok(s.escalated!.diagnosis.length > 50);
  assert.equal(s.locate!.index[s.locate!.culprit!.commit.sha], 62);
  assert.equal(s.deliver?.outcome, "diagnosis");
  assert.equal(s.agent!.items.filter((i) => i.type === "patch").length, 0);
});

test("a flaky test stops at reproduce and never starts the search", () => {
  const s = fold(sample("flaky"));
  assert.equal(s.escalated?.stage, "reproduce");
  assert.equal(s.reproduce.done?.flaky, true);
  assert.equal(s.locate, undefined);
  assert.equal(s.agent, undefined);
  assert.deepEqual(
    STAGES.map((st) => s.stages[st].status),
    ["ok", "escalated", "pending", "pending", "ok"],
  );
});

test("folding a prefix gives the state at that moment", () => {
  const events = sample("off-by-one");
  const firstRound = events.findIndex((e) => e.kind === "round_done" && e.data.round === 1);
  const mid = fold(events, firstRound + 1);
  assert.equal(mid.stages.locate.status, "running");
  assert.equal(mid.done, undefined);
  assert.equal(mid.locate!.culprit, undefined);
  assert.deepEqual([mid.locate!.lo, mid.locate!.hi], [51, 85]);
  assert.equal(Object.keys(mid.locate!.tested).length, 6);

  // While a commit is being tested it is marked as in progress.
  const testing = events.findIndex((e) => e.kind === "commit_testing");
  assert.equal(Object.keys(fold(events, testing + 1).locate!.testing).length, 1);

  assert.equal(fold(events, 0).runId, "");
  assert.deepEqual(fold(events, 9999), fold(events));
});

test("reduce does not change the state it is given", () => {
  const events = sample("off-by-one");
  const at = events.findIndex((e) => e.kind === "commit_tested");
  const before = fold(events, at);
  const snapshot = JSON.stringify(before);
  fold(events, at + 5);
  assert.equal(JSON.stringify(before), snapshot);
});

test("replay keeps real pacing but caps long waits", () => {
  const events = sample("off-by-one");
  assert.equal(replayDelay(events, 0, 1), 0);
  for (let i = 1; i < events.length; i++) {
    const d = replayDelay(events, i, 1);
    assert.ok(d >= 0 && d <= 900, `event ${i} waits ${d}ms`);
    assert.equal(replayDelay(events, i, 4), d / 4);
  }
});

test("a damaged line does not lose the rest of the file", () => {
  const raw = readFileSync(new URL("../public/samples/flaky.jsonl", import.meta.url), "utf8");
  const lines = raw.trim().split("\n");
  const damaged = [lines[0], "{not json", '{"v":1}', ...lines.slice(1)].join("\n");
  assert.equal(parseStream(damaged, parseEvent).length, lines.length);
});
