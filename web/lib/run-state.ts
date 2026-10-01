// Turns the event stream of a run into the state the dashboard draws.
// reduce() is a pure function, so a run can be replayed to any point by
// folding the first N events.

import type {
  Commit,
  HeallEvent,
  Payloads,
  Stage,
  Verdict,
} from "./events";

export const STAGES: Stage[] = [
  "triage",
  "reproduce",
  "locate",
  "heal",
  "deliver",
];

export type StageStatus = "pending" | "running" | "ok" | "escalated" | "error";

export type ReproduceRun = Payloads["reproduce_run"] & { good: boolean };

export type Round = {
  round: number;
  // The window the round started with, and what it narrowed to.
  lo: number;
  hi: number;
  probes: string[];
  after?: { lo: number; hi: number };
};

export type TestedCommit = {
  verdict: Verdict;
  round: number;
  durationMs: number;
};

export type LocateState = {
  commits: Commit[];
  index: Record<string, number>;
  workers: number;
  // The current search window: lo is the newest commit known good, hi the
  // oldest known bad.
  lo: number;
  hi: number;
  rounds: Round[];
  testing: Record<string, number>;
  tested: Record<string, TestedCommit>;
  culprit?: Payloads["culprit_found"];
};

export type TimelineItem =
  | { type: "thought"; seq: number; text: string; staged: boolean }
  | {
      type: "tool";
      seq: number;
      id: string;
      name: string;
      input: unknown;
      ok?: boolean;
      summary?: string;
    }
  | {
      type: "patch";
      seq: number;
      attempt: number;
      diff: string;
      staged: boolean;
      guardrails?: Payloads["guardrail_checked"];
      verify?: Payloads["verify_done"];
    }
  | { type: "log"; seq: number; level: string; message: string };

export type AgentState = {
  model: string;
  maxAttempts: number;
  items: TimelineItem[];
  done?: Payloads["agent_done"];
};

export type RunState = {
  runId: string;
  startedAt?: string;
  lastAt?: string;
  run?: Payloads["run_started"];
  stages: Record<Stage, { status: StageStatus; durationMs?: number }>;
  triage?: Payloads["triage_done"];
  reproduce: { runs: ReproduceRun[]; done?: Payloads["reproduce_done"] };
  locate?: LocateState;
  agent?: AgentState;
  escalated?: Payloads["escalated"] & { stage: Stage };
  deliver?: Payloads["deliver_done"];
  notes: { stage: Stage; level: string; message: string }[];
  done?: Payloads["run_done"];
};

export function initialState(): RunState {
  return {
    runId: "",
    stages: {
      run: { status: "pending" },
      triage: { status: "pending" },
      reproduce: { status: "pending" },
      locate: { status: "pending" },
      heal: { status: "pending" },
      deliver: { status: "pending" },
    },
    reproduce: { runs: [] },
    notes: [],
  };
}

// The staged red-team patch is announced by a thought that says so.
const STAGED = "staged by heall";

// reduce returns a new state; it never changes the one it is given.
export function reduce(state: RunState, ev: HeallEvent): RunState {
  const s: RunState = {
    ...state,
    runId: ev.run_id,
    startedAt: state.startedAt ?? ev.ts,
    lastAt: ev.ts,
  };
  switch (ev.kind) {
    case "run_started":
      s.run = ev.data;
      break;
    case "stage_started":
      s.stages = { ...s.stages, [ev.stage]: { status: "running" } };
      break;
    case "stage_done":
      s.stages = {
        ...s.stages,
        [ev.stage]: {
          status: ev.data.status,
          durationMs: ev.data.duration_ms,
        },
      };
      break;
    case "triage_done":
      s.triage = ev.data;
      break;
    case "reproduce_run":
      s.reproduce = {
        ...s.reproduce,
        runs: [
          ...s.reproduce.runs,
          { ...ev.data, good: s.run ? ev.data.sha === s.run.good : false },
        ],
      };
      break;
    case "reproduce_done":
      s.reproduce = { ...s.reproduce, done: ev.data };
      break;
    case "locate_started": {
      const index: Record<string, number> = {};
      ev.data.commits.forEach((c, i) => (index[c.sha] = i));
      s.locate = {
        commits: ev.data.commits,
        index,
        workers: ev.data.workers,
        lo: 0,
        hi: ev.data.commits.length - 1,
        rounds: [],
        testing: {},
        tested: {},
      };
      break;
    }
    case "round_started":
      if (s.locate) {
        s.locate = {
          ...s.locate,
          lo: ev.data.lo,
          hi: ev.data.hi,
          rounds: [...s.locate.rounds, { ...ev.data }],
        };
      }
      break;
    case "commit_testing":
      if (s.locate) {
        s.locate = {
          ...s.locate,
          testing: { ...s.locate.testing, [ev.data.sha]: ev.data.worker },
        };
      }
      break;
    case "commit_tested":
      if (s.locate) {
        const testing = { ...s.locate.testing };
        delete testing[ev.data.sha];
        s.locate = {
          ...s.locate,
          testing,
          tested: {
            ...s.locate.tested,
            [ev.data.sha]: {
              verdict: ev.data.verdict,
              round: ev.data.round,
              durationMs: ev.data.duration_ms,
            },
          },
        };
      }
      break;
    case "round_done":
      if (s.locate) {
        s.locate = {
          ...s.locate,
          lo: ev.data.lo,
          hi: ev.data.hi,
          rounds: s.locate.rounds.map((r) =>
            r.round === ev.data.round
              ? { ...r, after: { lo: ev.data.lo, hi: ev.data.hi } }
              : r,
          ),
        };
      }
      break;
    case "culprit_found":
      if (s.locate) {
        const at = s.locate.index[ev.data.commit.sha];
        s.locate = {
          ...s.locate,
          culprit: ev.data,
          lo: at === undefined ? s.locate.lo : at - 1,
          hi: at === undefined ? s.locate.hi : at,
        };
      }
      break;
    case "agent_started":
      s.agent = {
        model: ev.data.model,
        maxAttempts: ev.data.max_attempts,
        items: [],
      };
      break;
    case "agent_thought":
      s.agent = push(s.agent, {
        type: "thought",
        seq: ev.seq,
        text: ev.data.text,
        staged: ev.data.text.includes(STAGED),
      });
      break;
    case "tool_call":
      s.agent = push(s.agent, {
        type: "tool",
        seq: ev.seq,
        id: ev.data.id,
        name: ev.data.name,
        input: ev.data.input,
      });
      break;
    case "tool_result":
      s.agent = update(
        s.agent,
        "tool",
        (item) => item.id === ev.data.id,
        (item) => ({ ...item, ok: ev.data.ok, summary: ev.data.summary }),
      );
      break;
    case "patch_submitted": {
      // A patch that directly follows the staging announcement is the
      // staged one.
      const last = s.agent?.items.at(-1);
      s.agent = push(s.agent, {
        type: "patch",
        seq: ev.seq,
        attempt: ev.data.attempt,
        diff: ev.data.diff,
        staged: last?.type === "thought" && last.staged,
      });
      break;
    }
    case "guardrail_checked":
      s.agent = update(
        s.agent,
        "patch",
        (item) => !item.guardrails,
        (item) => ({ ...item, guardrails: ev.data }),
      );
      break;
    case "verify_done":
      s.agent = update(
        s.agent,
        "patch",
        (item) => !item.verify,
        (item) => ({ ...item, verify: ev.data }),
      );
      break;
    case "agent_done":
      if (s.agent) s.agent = { ...s.agent, done: ev.data };
      break;
    case "escalated":
      s.escalated = { ...ev.data, stage: ev.stage };
      break;
    case "deliver_done":
      s.deliver = ev.data;
      break;
    case "run_done":
      s.done = ev.data;
      break;
    case "log":
      if (ev.stage === "heal" && s.agent) {
        s.agent = push(s.agent, {
          type: "log",
          seq: ev.seq,
          level: ev.data.level,
          message: ev.data.message,
        });
      } else {
        s.notes = [...s.notes, { stage: ev.stage, ...ev.data }];
      }
      break;
  }
  return s;
}

function push(agent: AgentState | undefined, item: TimelineItem): AgentState {
  const base = agent ?? { model: "", maxAttempts: 0, items: [] };
  return { ...base, items: [...base.items, item] };
}

// update changes the last item of a type that matches; events refer to the
// latest tool call or patch.
function update<T extends TimelineItem["type"]>(
  agent: AgentState | undefined,
  type: T,
  match: (item: Extract<TimelineItem, { type: T }>) => boolean,
  change: (item: Extract<TimelineItem, { type: T }>) => TimelineItem,
): AgentState | undefined {
  if (!agent) return agent;
  type Item = Extract<TimelineItem, { type: T }>;
  const at = agent.items.findLastIndex((item) => item.type === type && match(item as Item));
  if (at < 0) return agent;
  const items = [...agent.items];
  items[at] = change(items[at] as Item);
  return { ...agent, items };
}

export function fold(events: readonly HeallEvent[], count?: number): RunState {
  let state = initialState();
  const n = Math.min(count ?? events.length, events.length);
  for (let i = 0; i < n; i++) state = reduce(state, events[i]);
  return state;
}

// parseStream reads a JSONL event file, skipping lines that are not events.
export function parseStream(
  text: string,
  parse: (line: string) => HeallEvent,
): HeallEvent[] {
  const out: HeallEvent[] = [];
  for (const line of text.split("\n")) {
    if (!line.trim()) continue;
    try {
      out.push(parse(line));
    } catch {
      // A line from a newer or damaged file must not break the rest.
    }
  }
  return out;
}

// How long to wait before showing event i during a replay: the real gap,
// capped so that waiting on a rate limit or a slow test does not stall it.
export function replayDelay(
  events: readonly HeallEvent[],
  i: number,
  speed: number,
  maxGapMs = 900,
): number {
  if (i <= 0 || i >= events.length) return 0;
  const gap = Date.parse(events[i].ts) - Date.parse(events[i - 1].ts);
  if (!Number.isFinite(gap) || gap <= 0) return 0;
  return Math.min(gap, maxGapMs) / speed;
}
