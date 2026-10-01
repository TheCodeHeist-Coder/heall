import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { FIELDS, parseEvent } from "./events.ts";

const examples = new URL(
  "../../contracts/examples/events.jsonl",
  import.meta.url,
);

test("the shared example stream parses and covers every kind", () => {
  const lines = readFileSync(examples, "utf8").trim().split("\n");
  const seen = new Set<string>();
  lines.forEach((line, i) => {
    const ev = parseEvent(line);
    assert.equal(ev.seq, i + 1);
    seen.add(ev.kind);
  });
  assert.deepEqual([...seen].sort(), Object.keys(FIELDS).sort());
});

test("events outside the contract are rejected", () => {
  const base = { v: 1, seq: 1, ts: "t", run_id: "r", stage: "run" };
  const bad = [
    { ...base, kind: "nope", data: {} },
    { ...base, kind: "log", data: { level: "info" } },
    { ...base, kind: "log", data: { level: "info", message: "m", extra: 1 } },
    { ...base, v: 2, kind: "stage_started", data: {} },
    { ...base, kind: "toString", data: {} },
  ];
  for (const ev of bad) {
    assert.throws(() => parseEvent(JSON.stringify(ev)), JSON.stringify(ev));
  }
});
